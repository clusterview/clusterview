package cluster

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// Link says one service calls another, and how we know.
type Link struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Via is the Kubernetes Service the caller names, "<name>.<namespace>".
	Via string `json:"via"`
	// Evidence is where the address was found, such as "env RABBITMQ_URL".
	Evidence string `json:"evidence"`
}

// Address is a Kubernetes Service that reaches a service's pods.
type Address struct {
	Name      string   `json:"name"`
	Namespace string   `json:"namespace"`
	Type      string   `json:"type"`
	Ports     []string `json:"ports"`
	// External is the load balancer's hostname or IP, when it has one.
	External []string `json:"external,omitempty"`
}

// Entrypoint is a way in from outside the cluster: an Ingress rule.
type Entrypoint struct {
	Host    string `json:"host,omitempty"`
	Path    string `json:"path,omitempty"`
	TLS     bool   `json:"tls"`
	Ingress string `json:"ingress"` // "<namespace>/<name>"
	Class   string `json:"class,omitempty"`
}

type envVar struct{ name, value string }

// envOf collects the literal values a pod template passes its containers:
// env values and command-line arguments. Values from Secrets and ConfigMaps
// are never read.
func envOf(spec corev1.PodSpec) []envVar {
	var out []envVar
	for _, c := range append(slices.Clone(spec.InitContainers), spec.Containers...) {
		for _, e := range c.Env {
			if e.Value != "" {
				out = append(out, envVar{e.Name, e.Value})
			}
		}
		if args := strings.Join(append(slices.Clone(c.Command), c.Args...), " "); args != "" {
			out = append(out, envVar{"args", args})
		}
	}
	return out
}

var hostToken = regexp.MustCompile(`(?i)[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*`)

// addressy env names usually hold a host or URL; a bare service name only
// counts in one of these (or in a URL), so "redis" in a feature flag doesn't.
var addressy = regexp.MustCompile(`(?i)(HOST|URL|URI|ADDR|ENDPOINT|SERVER|DSN|BROKER|SERVICE|UPSTREAM|BACKEND)`)

// linkServices works out how services connect:
//   - which Kubernetes Services reach each service's pods (its addresses),
//   - which Ingress rules lead to it (its entrypoints),
//   - which services name another one's address in their env or arguments.
func linkServices(objs Objects, services []Service, workloads []Workload, rawPods map[string][]*corev1.Pod) []Link {
	serviceOf := map[string]int{} // workload ID → index in services
	for i, s := range services {
		for _, id := range s.Workloads {
			serviceOf[id] = i
		}
	}
	podService := map[string]map[string]int{} // namespace → pod name → service
	podsByNS := map[string][]*corev1.Pod{}
	for id, pods := range rawPods {
		i, ok := serviceOf[id]
		if !ok {
			continue
		}
		for _, p := range pods {
			if podService[p.Namespace] == nil {
				podService[p.Namespace] = map[string]int{}
			}
			podService[p.Namespace][p.Name] = i
			podsByNS[p.Namespace] = append(podsByNS[p.Namespace], p)
		}
	}

	// Which services each Kubernetes Service reaches, by its selector.
	targets := map[string][]int{} // "<ns>/<name>"
	for _, ks := range objs.KubeServices {
		if len(ks.Spec.Selector) == 0 {
			continue
		}
		sel := labels.SelectorFromSet(ks.Spec.Selector)
		seen := map[int]bool{}
		for _, p := range podsByNS[ks.Namespace] {
			if i := podService[ks.Namespace][p.Name]; sel.Matches(labels.Set(p.Labels)) && !seen[i] {
				seen[i] = true
				targets[ks.Namespace+"/"+ks.Name] = append(targets[ks.Namespace+"/"+ks.Name], i)
			}
		}
		for i := range seen {
			services[i].Addresses = append(services[i].Addresses, address(ks))
		}
	}
	for i := range services {
		slices.SortFunc(services[i].Addresses, func(a, b Address) int { return cmp.Compare(a.Name, b.Name) })
	}

	for _, ing := range objs.Ingresses {
		tlsHosts := map[string]bool{}
		for _, t := range ing.Spec.TLS {
			for _, h := range t.Hosts {
				tlsHosts[h] = true
			}
		}
		class := ""
		if ing.Spec.IngressClassName != nil {
			class = *ing.Spec.IngressClassName
		}
		add := func(host, path, backend string) {
			for _, i := range targets[ing.Namespace+"/"+backend] {
				services[i].Entrypoints = append(services[i].Entrypoints, Entrypoint{
					Host: host, Path: path, TLS: tlsHosts[host], Ingress: ing.Namespace + "/" + ing.Name, Class: class,
				})
			}
		}
		if b := ing.Spec.DefaultBackend; b != nil && b.Service != nil {
			add("", "", b.Service.Name)
		}
		for _, r := range ing.Spec.Rules {
			if r.HTTP == nil {
				continue
			}
			for _, p := range r.HTTP.Paths {
				if p.Backend.Service != nil {
					add(r.Host, p.Path, p.Backend.Service.Name)
				}
			}
		}
	}

	// DNS names that reach each Kubernetes Service.
	byDNS := map[string]string{} // full name → "<ns>/<name>"
	for _, ks := range objs.KubeServices {
		key := ks.Namespace + "/" + ks.Name
		for _, n := range []string{".", ".svc", ".svc.cluster.local"} {
			byDNS[strings.TrimSuffix(ks.Name+"."+ks.Namespace+n, ".")] = key
		}
	}

	var links []Link
	seen := map[[2]int]bool{}
	for _, w := range workloads {
		from, ok := serviceOf[w.ID]
		if !ok {
			continue
		}
		for _, e := range w.env {
			for _, host := range hostToken.FindAllString(e.value, -1) {
				host = strings.ToLower(host)
				key, ok := byDNS[host]
				if !ok && !strings.Contains(host, ".") && bareAddress(e, host) {
					key = w.Namespace + "/" + host
					_, ok = targets[key]
				}
				if !ok {
					continue
				}
				for _, to := range targets[key] {
					if to == from || seen[[2]int{from, to}] {
						continue
					}
					seen[[2]int{from, to}] = true
					ns, name, _ := strings.Cut(key, "/")
					links = append(links, Link{
						From: services[from].ID, To: services[to].ID, Via: name + "." + ns, Evidence: evidence(e),
					})
				}
			}
		}
	}
	slices.SortFunc(links, func(a, b Link) int { return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.To, b.To)) })
	if links == nil {
		links = []Link{}
	}
	return links
}

// bareAddress says whether a bare name in a value is used as an address: in
// a URL or host:port, or as the whole value of an address-like variable.
func bareAddress(e envVar, host string) bool {
	v := strings.ToLower(e.value)
	if strings.Contains(v, "://"+host) || strings.Contains(v, "@"+host) {
		return true
	}
	// host:port, starting the value or after a separator.
	for i := 0; ; {
		j := strings.Index(v[i:], host+":")
		if j < 0 {
			break
		}
		start, after := i+j, i+j+len(host)+1
		if (start == 0 || strings.ContainsRune(" ,;=", rune(v[start-1]))) && after < len(v) && v[after] >= '0' && v[after] <= '9' {
			return true
		}
		i = after
	}
	return addressy.MatchString(e.name) && v == host
}

func evidence(e envVar) string {
	if e.name == "args" {
		return "command-line arguments"
	}
	return "env " + e.name
}

func address(ks *corev1.Service) Address {
	a := Address{Name: ks.Name, Namespace: ks.Namespace, Type: string(ks.Spec.Type), Ports: []string{}}
	for _, p := range ks.Spec.Ports {
		port := fmt.Sprintf("%d", p.Port)
		if p.Name != "" {
			port += "/" + p.Name
		}
		a.Ports = append(a.Ports, port)
	}
	for _, in := range ks.Status.LoadBalancer.Ingress {
		a.External = append(a.External, cmp.Or(in.Hostname, in.IP))
	}
	return a
}
