package cluster

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"

	"github.com/clusterview/clusterview/internal/kube"
)

const (
	// debounce groups bursts of changes (a rollout touches dozens of objects)
	// into one update for the browser.
	debounce = 250 * time.Millisecond
	// metricsEvery is how often metrics-server is read; it only refreshes
	// about every 15 seconds itself.
	metricsEvery = 15 * time.Second
)

// Cache watches the cluster and keeps everything the dashboard shows in
// memory. Each change bumps a version; subscribers hear about it.
type Cache struct {
	kube *kube.Client
	log  *slog.Logger
	opts Options
	// scopes are the watches: one for the whole cluster, or one per namespace.
	scopes []scope
	// nodes watches the cluster's nodes; nil when the nodes view is off.
	nodes   informers.SharedInformerFactory
	synced  atomic.Bool
	version atomic.Uint64
	changed chan struct{}

	guesser Guesser

	mu         sync.Mutex
	subs       map[chan uint64]struct{}
	snap       *Snapshot
	healthSnap *Snapshot
	nodeMet    []metricsv1beta1.NodeMetrics
	podMet     []metricsv1beta1.PodMetrics
	hasMet     bool
	metError   string
}

// Options choose what the cache watches.
type Options struct {
	// Namespaces limits watching to these; empty means the whole cluster.
	Namespaces []string
	// Nodes watches the cluster's nodes for the nodes view. It needs
	// cluster-wide read access to nodes.
	Nodes bool
}

// scope is the watches for one namespace, or "" for all of them.
type scope struct {
	namespace string
	factory   informers.SharedInformerFactory
	// warnings watches only Warning events; Normal ones are most of the
	// traffic and never shown.
	warnings informers.SharedInformerFactory
}

// NewCache prepares watches on every kind the dashboard reads. Call Start.
func NewCache(k *kube.Client, opts Options, log *slog.Logger) *Cache {
	c := &Cache{
		kube:    k,
		log:     log,
		opts:    opts,
		changed: make(chan struct{}, 1),
		subs:    map[chan uint64]struct{}{},
	}
	touch := cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { c.touch() },
		UpdateFunc: func(any, any) { c.touch() },
		DeleteFunc: func(any) { c.touch() },
	}
	namespaces := opts.Namespaces
	if len(namespaces) == 0 {
		namespaces = []string{metav1.NamespaceAll}
	}
	for _, ns := range namespaces {
		f := informers.NewSharedInformerFactoryWithOptions(k.Clientset, 0,
			informers.WithNamespace(ns),
			// managedFields are large and never shown; dropping them halves memory.
			informers.WithTransform(stripManagedFields))
		w := informers.NewSharedInformerFactoryWithOptions(k.Clientset, 0,
			informers.WithNamespace(ns),
			informers.WithTransform(stripManagedFields),
			informers.WithTweakListOptions(func(o *metav1.ListOptions) { o.FieldSelector = "type=Warning" }))
		for _, inf := range []cache.SharedIndexInformer{
			f.Apps().V1().Deployments().Informer(),
			f.Apps().V1().ReplicaSets().Informer(),
			f.Apps().V1().StatefulSets().Informer(),
			f.Apps().V1().DaemonSets().Informer(),
			f.Batch().V1().Jobs().Informer(),
			f.Batch().V1().CronJobs().Informer(),
			f.Core().V1().Pods().Informer(),
			f.Autoscaling().V2().HorizontalPodAutoscalers().Informer(),
			f.Core().V1().PersistentVolumeClaims().Informer(),
			f.Core().V1().Services().Informer(),
			f.Networking().V1().Ingresses().Informer(),
			w.Core().V1().Events().Informer(),
		} {
			_, _ = inf.AddEventHandler(touch)
		}
		c.scopes = append(c.scopes, scope{namespace: ns, factory: f, warnings: w})
	}
	if opts.Nodes {
		c.nodes = informers.NewSharedInformerFactoryWithOptions(k.Clientset, 0, informers.WithTransform(stripManagedFields))
		_, _ = c.nodes.Core().V1().Nodes().Informer().AddEventHandler(touch)
	}
	return c
}

// SetGuesser describes unknown services with g. Call before Start.
func (c *Cache) SetGuesser(g Guesser) { c.guesser = g }

// Refresh rebuilds the view, for when something outside the cluster (a
// guesser's answers) changed it.
func (c *Cache) Refresh() { c.touch() }

// NodesEnabled reports whether the nodes view is on.
func (c *Cache) NodesEnabled() bool { return c.nodes != nil }

func (c *Cache) factories() []informers.SharedInformerFactory {
	var out []informers.SharedInformerFactory
	for _, s := range c.scopes {
		out = append(out, s.factory, s.warnings)
	}
	if c.nodes != nil {
		out = append(out, c.nodes)
	}
	return out
}

func stripManagedFields(obj any) (any, error) {
	if m, err := meta.Accessor(obj); err == nil {
		m.SetManagedFields(nil)
	}
	return obj, nil
}

// Clientset is the client the cache reads with, for one-off calls like logs.
func (c *Cache) Clientset() kubernetes.Interface { return c.kube.Clientset }

// Start runs the watches until ctx ends. It returns once the first full read
// of the cluster is in memory.
func (c *Cache) Start(ctx context.Context) bool {
	for _, f := range c.factories() {
		f.Start(ctx.Done())
	}
	go c.broadcast(ctx)
	go c.pollMetrics(ctx)
	for _, f := range c.factories() {
		for typ, ok := range f.WaitForCacheSync(ctx.Done()) {
			if !ok {
				c.log.Error("watch never synced", "type", typ.String())
				return false
			}
		}
	}
	c.synced.Store(true)
	c.touch()
	c.log.Info("cluster cache synced")
	return true
}

// Ready reports whether the first full read has finished.
func (c *Cache) Ready() bool { return c.synced.Load() }

// Version is bumped on every change the dashboard could show.
func (c *Cache) Version() uint64 { return c.version.Load() }

func (c *Cache) touch() {
	select {
	case c.changed <- struct{}{}:
	default: // an update is already pending
	}
}

func (c *Cache) broadcast(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.changed:
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(debounce):
		}
		if !c.synced.Load() {
			continue
		}
		v := c.version.Add(1)
		c.mu.Lock()
		for ch := range c.subs {
			select {
			case ch <- v:
			default: // slow reader; it will catch up on the next version
			}
		}
		c.mu.Unlock()
	}
}

// Subscribe returns a channel of new versions, and a function to stop.
func (c *Cache) Subscribe() (<-chan uint64, func()) {
	ch := make(chan uint64, 1)
	c.mu.Lock()
	c.subs[ch] = struct{}{}
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		delete(c.subs, ch)
		c.mu.Unlock()
	}
}

func (c *Cache) objects() Objects {
	all := labels.Everything()
	var o Objects
	for _, sc := range c.scopes {
		f := sc.factory
		o.Deployments = append(o.Deployments, items(f.Apps().V1().Deployments().Lister().List(all))...)
		o.ReplicaSets = append(o.ReplicaSets, items(f.Apps().V1().ReplicaSets().Lister().List(all))...)
		o.StatefulSets = append(o.StatefulSets, items(f.Apps().V1().StatefulSets().Lister().List(all))...)
		o.DaemonSets = append(o.DaemonSets, items(f.Apps().V1().DaemonSets().Lister().List(all))...)
		o.Jobs = append(o.Jobs, items(f.Batch().V1().Jobs().Lister().List(all))...)
		o.CronJobs = append(o.CronJobs, items(f.Batch().V1().CronJobs().Lister().List(all))...)
		o.Pods = append(o.Pods, items(f.Core().V1().Pods().Lister().List(all))...)
		o.HPAs = append(o.HPAs, items(f.Autoscaling().V2().HorizontalPodAutoscalers().Lister().List(all))...)
		o.PVCs = append(o.PVCs, items(f.Core().V1().PersistentVolumeClaims().Lister().List(all))...)
		o.KubeServices = append(o.KubeServices, items(f.Core().V1().Services().Lister().List(all))...)
		o.Ingresses = append(o.Ingresses, items(f.Networking().V1().Ingresses().Lister().List(all))...)
		o.Events = append(o.Events, items(sc.warnings.Core().V1().Events().Lister().List(all))...)
	}
	if c.nodes != nil {
		o.Nodes, _ = c.nodes.Core().V1().Nodes().Lister().List(all)
	}
	return o
}

// items drops a lister's error; listers read memory and don't fail.
func items[T any](list []T, _ error) []T { return list }

// Snapshot returns every workload with AI descriptions for the dashboard.
func (c *Cache) Snapshot() Snapshot {
	v := c.Version()
	c.mu.Lock()
	if c.snap != nil && c.snap.Version == v {
		s := *c.snap
		c.mu.Unlock()
		return s
	}
	c.mu.Unlock()

	s := c.HealthSnapshot()
	if c.guesser != nil {
		// Enrichment changes services. Keep the health cache immutable and
		// safe for concurrent alert polling and dashboard reads.
		s.Services = slices.Clone(s.Services)
		applyGuesses(&s, c.guesser)
	}
	c.mu.Lock()
	c.snap = &s
	c.mu.Unlock()
	return s
}

// HealthSnapshot returns the scoped cluster view without applying or
// requesting AI descriptions. Built once per version, it is safe for
// background consumers that must not trigger model calls.
func (c *Cache) HealthSnapshot() Snapshot {
	v := c.Version()
	c.mu.Lock()
	if c.healthSnap != nil && c.healthSnap.Version == v {
		s := *c.healthSnap
		c.mu.Unlock()
		return s
	}
	c.mu.Unlock()

	s := BuildSnapshot(c.objects(), v, time.Now().UTC())
	c.mu.Lock()
	c.healthSnap = &s
	c.mu.Unlock()
	return s
}

// Nodes returns every node with its pods and usage.
func (c *Cache) Nodes() Nodes {
	o := c.objects()
	c.mu.Lock()
	objs := NodeObjects{
		Nodes: o.Nodes, Pods: o.Pods, ReplicaSets: o.ReplicaSets, Jobs: o.Jobs,
		NodeMetrics: c.nodeMet, PodMetrics: c.podMet, HasMetrics: c.hasMet,
	}
	c.mu.Unlock()
	return BuildNodes(objs, time.Now().UTC())
}

// Pod looks a pod up in memory.
func (c *Cache) Pod(namespace, name string) (*corev1.Pod, error) {
	for _, sc := range c.scopes {
		if sc.namespace != metav1.NamespaceAll && sc.namespace != namespace {
			continue
		}
		if p, err := sc.factory.Core().V1().Pods().Lister().Pods(namespace).Get(name); err == nil {
			return p, nil
		}
	}
	return nil, ErrNotFound
}

// pollMetrics reads metrics-server when it's installed. Without it the
// dashboard still works; usage figures are just missing.
func (c *Cache) pollMetrics(ctx context.Context) {
	tick := time.NewTicker(metricsEvery)
	defer tick.Stop()
	for {
		c.readMetrics(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (c *Cache) readMetrics(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	m := c.kube.Metrics.MetricsV1beta1()
	var nodeItems []metricsv1beta1.NodeMetrics
	var podItems []metricsv1beta1.PodMetrics
	var err error
	if c.nodes != nil {
		var nodes *metricsv1beta1.NodeMetricsList
		if nodes, err = m.NodeMetricses().List(ctx, metav1.ListOptions{}); err == nil {
			nodeItems = nodes.Items
		}
	}
	for _, sc := range c.scopes {
		if err != nil {
			break
		}
		var pods *metricsv1beta1.PodMetricsList
		if pods, err = m.PodMetricses(sc.namespace).List(ctx, metav1.ListOptions{}); err == nil {
			podItems = append(podItems, pods.Items...)
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		if c.metError != err.Error() {
			c.log.Info("metrics unavailable; usage figures hidden", "err", err)
			c.metError = err.Error()
		}
		c.hasMet, c.nodeMet, c.podMet = false, nil, nil
		return
	}
	if c.metError != "" || !c.hasMet {
		c.log.Info("metrics available")
	}
	c.metError = ""
	c.hasMet, c.nodeMet, c.podMet = true, nodeItems, podItems
	c.touch()
}
