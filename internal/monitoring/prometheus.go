// Package monitoring reads explicitly configured Prometheus metrics. It never
// discovers endpoints from workload annotations or sends requests to brokers.
package monitoring

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/clusterview/clusterview/internal/cluster"
)

// Config contains non-secret monitoring settings. The bearer token is supplied
// separately through the environment, never a chart value or API response.
type Config struct {
	PrometheusURL string                `json:"prometheusURL"`
	MatchLabels   map[string]string     `json:"matchLabels"`
	Resources     cluster.HealthPolicy  `json:"resources"`
	RabbitMQ      []cluster.QueueTarget `json:"rabbitmq"`
}

// Parse rejects misspelled settings rather than quietly ignoring them.
func Parse(raw string) (Config, error) {
	var cfg Config
	if raw != "" {
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			return cfg, errors.New("monitoring must be a JSON object with valid settings")
		}
		var trailing any
		if !errors.Is(dec.Decode(&trailing), io.EOF) {
			return cfg, errors.New("monitoring must contain one JSON object")
		}
	}
	if cfg.Resources.WarningPercent == 0 {
		cfg.Resources.WarningPercent = 85
	}
	if cfg.Resources.CriticalPercent == 0 {
		cfg.Resources.CriticalPercent = 95
	}
	if cfg.Resources.WarningPercent <= 0 || cfg.Resources.CriticalPercent > 100 || cfg.Resources.WarningPercent >= cfg.Resources.CriticalPercent {
		return cfg, errors.New("monitoring resource thresholds must satisfy 0 < warningPercent < criticalPercent <= 100")
	}
	return cfg, nil
}

// Client implements cluster.Telemetry. It is read-only and bounds concurrency,
// response size and request duration independently of the browser.
type Client struct {
	cfg        Config
	token      string
	http       *http.Client
	namespaces []string
}

var labelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// New validates settings even when Kubernetes is unavailable. No URL or token is
// included in errors, since URLs can themselves contain access credentials.
func New(cfg Config, token string, namespaces []string) (*Client, error) {
	if cfg.Resources.WarningPercent == 0 {
		cfg.Resources.WarningPercent = 85
	}
	if cfg.Resources.CriticalPercent == 0 {
		cfg.Resources.CriticalPercent = 95
	}
	if err := ValidatePolicy(cfg.Resources); err != nil {
		return nil, err
	}
	if cfg.PrometheusURL == "" {
		if len(cfg.RabbitMQ) > 0 {
			return nil, errors.New("monitoring.rabbitmq needs monitoring.prometheusURL")
		}
		return nil, nil
	}
	u, err := url.Parse(cfg.PrometheusURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("monitoring.prometheusURL must be an HTTP(S) base URL without user information, query or fragment")
	}
	if strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("monitoring bearer token contains an invalid header character")
	}
	if len(cfg.RabbitMQ) > 20 {
		return nil, errors.New("monitoring supports at most 20 explicitly selected RabbitMQ queues")
	}
	for k := range cfg.MatchLabels {
		if !labelName.MatchString(k) || strings.HasPrefix(k, "__") || slices.Contains([]string{"namespace", "pod", "container", "job", "vhost", "queue"}, k) {
			return nil, errors.New("monitoring.matchLabels contains an invalid or reserved label")
		}
	}
	seen := map[string]bool{}
	for i := range cfg.RabbitMQ {
		q := &cfg.RabbitMQ[i]
		parts := strings.Split(q.ServiceID, "/")
		if (len(parts) != 3 && len(parts) != 4) || q.Job == "" || q.VHost == "" || q.Queue == "" {
			return nil, errors.New("each RabbitMQ target needs a discovered serviceId, Prometheus job, vhost and queue")
		}
		ns := parts[len(parts)-2]
		if ns == "" || (len(namespaces) > 0 && !slices.Contains(namespaces, ns)) {
			return nil, errors.New("RabbitMQ target must be in a watched namespace")
		}
		for k, v := range q.BrokerLabels {
			if !labelName.MatchString(k) || strings.HasPrefix(k, "__") || slices.Contains([]string{"namespace", "job", "vhost", "queue"}, k) {
				return nil, errors.New("RabbitMQ brokerLabels contains an invalid or reserved label")
			}
			if global, ok := cfg.MatchLabels[k]; ok && global != v {
				return nil, errors.New("RabbitMQ brokerLabels conflicts with monitoring.matchLabels")
			}
		}
		if q.AckMode == "" {
			q.AckMode = "manual"
		}
		if q.AckMode != "manual" && q.AckMode != "auto" {
			return nil, errors.New("RabbitMQ ackMode must be manual or auto")
		}
		if q.WarningMessages == 0 {
			q.WarningMessages = 1000
		}
		if q.CriticalMessages == 0 {
			q.CriticalMessages = 10000
		}
		if q.WarningMessages < 1 || q.CriticalMessages <= q.WarningMessages {
			return nil, errors.New("RabbitMQ thresholds must satisfy 0 < warningMessages < criticalMessages")
		}
		key := q.ServiceID + cluster.QueueCheckKey(*q)
		if seen[key] {
			return nil, errors.New("duplicate RabbitMQ target")
		}
		seen[key] = true
	}
	cfg.PrometheusURL = strings.TrimRight(cfg.PrometheusURL, "/")
	return &Client{cfg: cfg, token: token, namespaces: slices.Clone(namespaces), http: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) selector(metric string, labels map[string]string, scoped bool) string {
	all := make(map[string]string, len(c.cfg.MatchLabels)+len(labels))
	for k, v := range c.cfg.MatchLabels {
		all[k] = v
	}
	for k, v := range labels {
		all[k] = v
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var match []string
	for _, k := range keys {
		match = append(match, k+"="+strconv.Quote(all[k]))
	}
	if scoped && len(c.namespaces) > 0 {
		ns := make([]string, len(c.namespaces))
		for i, n := range c.namespaces {
			ns[i] = regexp.QuoteMeta(n)
		}
		match = append(match, "namespace=~"+strconv.Quote(strings.Join(ns, "|")))
	}
	return metric + "{" + strings.Join(match, ",") + "}"
}

// fresh filters at the original scrape timestamp, before aggregations/rates
// replace it with the query evaluation time.
func fresh(selector string) string {
	return "(" + selector + " and (timestamp(" + selector + ") > time() - 120))"
}
func freshRate(selector string) string {
	return "(rate(" + selector + "[5m]) and (timestamp(" + selector + ") > time() - 120))"
}

type vectorValue struct {
	labels map[string]string
	sample cluster.TelemetrySample
}

func (c *Client) query(ctx context.Context, expression string) ([]vectorValue, error) {
	endpoint, err := url.Parse(c.cfg.PrometheusURL + "/api/v1/query")
	if err != nil {
		return nil, errors.New("invalid monitoring endpoint")
	}
	q := endpoint.Query()
	q.Set("query", expression)
	q.Set("timeout", "5s")
	endpoint.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, errors.New("could not create monitoring request")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, errors.New("monitoring request failed")
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("monitoring request rejected")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 4*1024*1024+1))
	if err != nil || len(body) > 4*1024*1024 {
		return nil, errors.New("monitoring response unavailable or too large")
	}
	var response struct {
		Status   string   `json:"status"`
		Warnings []string `json:"warnings"`
		Data     struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &response) != nil || response.Status != "success" || response.Data.ResultType != "vector" || len(response.Warnings) > 0 {
		return nil, errors.New("monitoring response incomplete or invalid")
	}
	out := make([]vectorValue, 0, len(response.Data.Result))
	now := time.Now()
	for _, r := range response.Data.Result {
		var stamp float64
		var number string
		if len(r.Value) != 2 || json.Unmarshal(r.Value[0], &stamp) != nil || json.Unmarshal(r.Value[1], &number) != nil {
			return nil, errors.New("invalid monitoring sample")
		}
		v, err := strconv.ParseFloat(number, 64)
		at := time.UnixMilli(int64(stamp * 1000))
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || now.Sub(at) > 2*time.Minute || at.Sub(now) > 30*time.Second {
			continue
		}
		out = append(out, vectorValue{labels: r.Metric, sample: cluster.TelemetrySample{Value: v, Time: at}})
	}
	return out, nil
}

// Read returns partial evidence when one query fails, without fabricating zeros.
func (c *Client) Read(ctx context.Context) cluster.TelemetryData {
	data := cluster.TelemetryData{Volumes: map[string]cluster.VolumeSample{}, Throttling: map[string]cluster.TelemetrySample{}}
	type task struct {
		expr  string
		apply func([]vectorValue)
	}
	var tasks []task
	used, capacity := map[string]cluster.TelemetrySample{}, map[string]cluster.TelemetrySample{}
	for _, item := range []struct {
		metric string
		dst    map[string]cluster.TelemetrySample
	}{{"kubelet_volume_stats_used_bytes", used}, {"kubelet_volume_stats_capacity_bytes", capacity}} {
		selector := c.selector(item.metric, nil, true)
		dst := item.dst
		tasks = append(tasks, task{"max by (namespace,persistentvolumeclaim) " + fresh(selector), func(rows []vectorValue) {
			for _, r := range rows {
				ns, pvc := r.labels["namespace"], r.labels["persistentvolumeclaim"]
				if ns != "" && pvc != "" {
					dst[ns+"/"+pvc] = r.sample
				}
			}
		}})
	}
	throttled := c.selector("container_cpu_cfs_throttled_periods_total", nil, true)
	periods := c.selector("container_cpu_cfs_periods_total", nil, true)
	tasks = append(tasks, task{"max by (namespace,pod,container) (100 * " + freshRate(throttled) + " / clamp_min(" + freshRate(periods) + ", 0.000001))", func(rows []vectorValue) {
		for _, r := range rows {
			ns, pod, container := r.labels["namespace"], r.labels["pod"], r.labels["container"]
			if ns != "" && pod != "" && container != "" {
				data.Throttling[ns+"/"+pod+"/"+container] = r.sample
			}
		}
	}})
	data.Queues = make([]cluster.QueueHealth, len(c.cfg.RabbitMQ))
	for i, target := range c.cfg.RabbitMQ {
		result := &data.Queues[i]
		result.QueueTarget = target
		parts := strings.Split(target.ServiceID, "/")
		labels := map[string]string{"namespace": parts[len(parts)-2], "job": target.Job, "vhost": target.VHost, "queue": target.Queue}
		for k, v := range target.BrokerLabels {
			labels[k] = v
		}
		add := func(metric string, rate bool, dst **float64) {
			sel := c.selector(metric, labels, false)
			expression := fresh(sel)
			if rate {
				expression = freshRate(sel)
			}
			tasks = append(tasks, task{"sum " + expression, func(rows []vectorValue) {
				if len(rows) != 1 {
					return
				}
				v := rows[0].sample.Value
				*dst = &v
				at := rows[0].sample.Time
				if result.ObservedAt == nil || at.Before(*result.ObservedAt) {
					result.ObservedAt = &at
				}
			}})
		}
		add("rabbitmq_detailed_queue_messages_ready", false, &result.Ready)
		add("rabbitmq_detailed_queue_messages_unacked", false, &result.Unacked)
		add("rabbitmq_detailed_queue_consumers", false, &result.Consumers)
		add("rabbitmq_detailed_queue_exchange_messages_published_total", true, &result.PublishRate)
		if target.AckMode == "auto" {
			// Auto-ack delivery and basic.get are independent counters. Both are
			// required so an absent family cannot masquerade as a zero rate.
			a, b := new(*float64), new(*float64)
			add("rabbitmq_detailed_queue_messages_delivered_total", true, a)
			add("rabbitmq_detailed_queue_get_total", true, b)
			tasks = append(tasks, task{apply: func([]vectorValue) {
				if *a != nil && *b != nil {
					v := **a + **b
					result.ProcessRate = &v
				}
			}})
		} else {
			add("rabbitmq_detailed_queue_messages_acked_total", true, &result.ProcessRate)
		}
	}
	// Apply callbacks only after requests finish: maps and per-queue fields have
	// one writer, while at most four requests are in flight.
	results := make([][]vectorValue, len(tasks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, t := range tasks {
		if t.expr == "" {
			continue
		}
		wg.Add(1)
		go func(i int, expr string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			rows, err := c.query(ctx, expr)
			if err == nil {
				results[i] = rows
			}
		}(i, t.expr)
	}
	wg.Wait()
	for i, t := range tasks {
		t.apply(results[i])
	}
	for key, u := range used {
		if total, ok := capacity[key]; ok {
			data.Volumes[key] = cluster.VolumeSample{Used: u, Capacity: total}
		}
	}
	return data
}

// ValidatePolicy checks programmatically constructed settings as well as JSON.
func ValidatePolicy(p cluster.HealthPolicy) error {
	if p.WarningPercent <= 0 || p.CriticalPercent > 100 || p.WarningPercent >= p.CriticalPercent || math.IsNaN(p.WarningPercent) || math.IsNaN(p.CriticalPercent) {
		return fmt.Errorf("invalid monitoring resource percentage thresholds")
	}
	return nil
}
