# Resource and queue health

Cards, list rows and the drawer show coloured CPU, memory and disk meters.
Kubernetes readiness remains separate: all replicas can be ready while a resource
or queue check needs attention. Labels and explanations accompany the colours.

| Colour | Meaning |
|---|---|
| Green | Current evidence within thresholds |
| Amber | Memory/disk at 85%, CPU at 85% of its limit, or a growing/stalled queue at its warning depth |
| Red | Memory/disk at 95%, CPU throttling in at least 50% of CFS periods, a growing/stalled queue at its critical depth, or waiting messages without consumers |
| Grey | Missing/stale measurements or a missing resource limit |

A service with partial data stays unknown unless a known warning or critical
check takes priority. CPU saturation alone stays amber even above 95%: busy CPUs
can be doing useful work. Throttling warns at 25% and becomes critical at 50% of
CFS periods. Percentage thresholds for memory, disk and CPU saturation are configurable.

## Sources and coverage

CPU and memory come from metrics-server, per container, against **limits**.
The service meter shows the most constrained container, rather than an average
that could hide a saturated sidecar. Missing limits retain usage but omit the
percentage. The drawer expands each pod's container measurements.

Disk means measured filesystem usage/capacity of PVCs referenced by the service's
actual pods, using kubelet volume metrics. It does not represent requested
storage, ephemeral storage or node disks. Services without a PVC show an
unavailable disk meter. Existing Kubernetes DiskPressure checks still apply.

Optional Prometheus polling runs on the server every 15 seconds, with bounded
requests. Raw series older than two minutes are excluded before aggregation.
Metrics-server readings older than two minutes, before a pod/container start,
or far in the future are unknown. Throttling requires a five-minute rate window
after the current pod/container starts. Each refresh replaces prior readings,
including failures; missing series are never assumed to be zero.

The drawer retains up to 40 percentage points per service/metric/subject in
memory as observations are served. Histories reset on restart and are not a
replacement for Prometheus retention.

## Helm configuration

```yaml
monitoring:
  prometheusURL: http://prometheus.monitoring.svc:9090
  # Isolate this Kubernetes cluster when using shared/federated Prometheus.
  matchLabels:
    cluster: production
  resources:
    warningPercent: 85
    criticalPercent: 95
  # Optional existing Secret, with a bearerToken key. Never put tokens here.
  existingSecret: clusterview-monitoring
  rabbitmq:
    - serviceId: rabbitmq.com/RabbitmqCluster/data/events
      job: rabbitmq
      # Copy actual scrape labels identifying this logical broker cluster.
      brokerLabels:
        service: events
      vhost: /
      queue: orders
      warningMessages: 1000
      criticalMessages: 10000
      ackMode: manual
      allowNoConsumers: false
```

Copy the exact `serviceId` from `/api/v1/snapshot`; operator-managed IDs include
the API group and kind. Only discovered services in watched namespaces receive
queue readings. Targets are explicit, with a maximum of 20; clusterview does not
discover or contact brokers. `job`, `vhost` and `queue` match Prometheus labels
exactly. Every queue query also selects the exact `namespace` from `serviceId`
**before aggregation**, even when the dashboard watches all namespaces. The
scraped RabbitMQ series must carry that namespace label; absent labels leave
queue health unknown.

Use per-target `brokerLabels` when multiple brokers in a namespace share a job,
vhost and queue name. Select labels that identify the whole logical broker
cluster, such as its scrape Service label, so all its HA nodes remain included.
These equality matchers apply to every queue gauge and counter before summing.
They cannot override namespace, job, vhost, queue or conflict with global labels.
A job uniquely identifying a broker within its namespace can omit brokerLabels.

Resource queries also restrict watched namespaces. Global `matchLabels` isolate
identically named resources or queues across Kubernetes clusters and apply to
all queries.

Set `existingSecret` only if Prometheus needs bearer authentication. Its
`bearerToken` key is read from the dashboard namespace. The server never returns
the token or Prometheus URL to the browser and does not follow redirects.
Any cluster egress policies must allow the configured server.
Without `prometheusURL`, CPU/memory still work via metrics-server, while PVC
usage and queue flow have no source.

On a laptop, supply the same non-secret object as JSON in
`CLUSTERVIEW_MONITORING` (omit `existingSecret`), and optionally set
`CLUSTERVIEW_MONITORING_BEARER_TOKEN` separately.

## RabbitMQ scraping and flow

Enable RabbitMQ's Prometheus plugin and configure Prometheus to scrape each
broker's `/metrics/detailed` endpoint. Request these metric families:

```yaml
metrics_path: /metrics/detailed
params:
  family:
    - queue_coarse_metrics
    - queue_consumer_count
    - queue_delivery_metrics
    - queue_exchange_metrics
```

See [RabbitMQ's detailed metric reference](https://www.rabbitmq.com/docs/prometheus#detailed-endpoint).
The default aggregated endpoint does not supply this per-queue evidence.
Ready, unacknowledged and consumer counts use `rabbitmq_detailed_queue_*`
gauges; incoming rate sums queue/exchange published counters. Manual mode
compares incoming rate with acknowledged messages; auto mode combines automatic
consumer deliveries and basic.get counters. Rates use five minutes, so a new
series needs at least two samples. Scrape all brokers hosting the selected queue,
and avoid duplicate scrape targets that would double counts.

Depth is ready plus unacknowledged messages. A large **draining** backlog can
stay green; depth alone is not a failure. At the configured depths, zero
processing or incoming exceeding processing warns/escalates. Waiting messages
with zero consumers are critical regardless of depth. Set `allowNoConsumers`
for pull consumers or intentional delayed queues; backlog/flow checks still
apply. Select `ackMode: auto` only for queues using automatic acknowledgement;
mixed acknowledgement modes need separate monitoring policy outside this check.
Missing rates/consumer gauges leave health unknown unless available evidence
already proves a problem. Auto delivery measures broker delivery, not completion
of application work. This check does not measure message age or end-to-end latency.

## Notifications and preview

Critical resource and queue problems participate in existing sustained-failure
alerts when alerts are enabled. Amber notifications additionally require
`alerts.resourceWarnings: true`. All notifications wait for `alerts.for`, so a
brief peak can colour the UI without sending a webhook. CPU usage and CPU
throttling have separate check identities (`resource-cpu` and
`resource-cpu-throttling`). Low metrics-server usage cannot resolve a throttling
incident: fresh throttling evidence is required for the current containers.
Missing/stale throttling series remain unknown when Prometheus is configured.
Resource recovery checks require measurements for every current container or PVC.
A known warning or critical reading stays visible when another measurement is
missing, but that partial evidence cannot resolve an existing incident. Once all
measurements are available, a warning can resolve a critical-only incident;
with warning notifications enabled, it remains active until healthy.
Previously failing checks need current evidence before recovery is sent;
missing data is not recovery. See [service alerts](service-alerts.md).

Run the frontend and open `/?mock=health&ai=off` for examples of healthy,
warning, critical and unavailable readings, including a stalled RabbitMQ queue.
