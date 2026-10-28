# Service alerts

Alerts are optional. They notify a configured destination when a service stays
broken, then when it recovers. They run on the server, so the dashboard doesn't
need to be open in a browser. No AI key or extra Kubernetes permissions are needed.

## Configure

Create a Kubernetes Secret named `clusterview-alerts` in the dashboard's
namespace with a `webhookURL` key. Use your normal secret management process;
don't commit a Slack or Teams webhook URL to a values file. The URL contains
credentials.

```yaml
alerts:
  enabled: true
  existingSecret: clusterview-alerts
  format: slack
  for: 5m
  cluster: production
  dashboardURL: https://clusterview.example.com
```

- `format: slack` posts to a [Slack incoming webhook](https://docs.slack.dev/messaging/sending-messages-using-incoming-webhooks/).
  Messages use plain text to prevent cluster metadata from becoming mentions.
- `format: teams` posts an Adaptive Card to a
  [Teams webhook workflow](https://learn.microsoft.com/en-us/microsoftteams/platform/webhooks-and-connectors/how-to/add-incoming-webhook).
  Use a workflow that accepts webhook calls without caller authentication; the
  secret URL authenticates this sender. Tenant-authenticated workflows need an
  authenticating relay. Clusterview doesn't acquire Microsoft access tokens.
- `format: webhook` sends the structured JSON below to a custom receiver.

On a laptop the same settings are available through
`CLUSTERVIEW_ALERTS_WEBHOOK_URL`, `CLUSTERVIEW_ALERTS_FORMAT`,
`CLUSTERVIEW_ALERTS_FOR`, `CLUSTERVIEW_ALERTS_CLUSTER`, and
`CLUSTERVIEW_ALERTS_DASHBOARD_URL`. Except for the secret URL, settings also
have `--alerts-*` flags. Without an explicit cluster label, laptop mode uses
the kubeconfig context; in-cluster mode uses `cluster`.

The webhook and dashboard addresses must be HTTP(S) URLs without user
information or fragments. For an external destination use HTTPS. A configured
webhook turns alerts on in laptop mode; the Helm chart sets it only when
`alerts.enabled` is true. Invalid settings prevent startup.

## When it sends

The monitor waits for the cluster cache to sync, then checks every 15 seconds.
It sees exactly the namespaces configured for the dashboard.

1. A service is broken when its status is down or degraded, or it has a critical
   problem. Warning-only problems don't start an incident.
2. An incident starts when the monitor first observes the failure. It must
   remain broken for `alerts.for` (default `5m`) before a notification is sent.
   Healthy, idle or unknown status before that resets the window.
3. Once a firing notification succeeds, the monitor suppresses further firing
   notifications for that incident, even when the failure's symptoms change.
4. It sends recovery only after a notified service becomes healthy or idle,
   with no critical problems. Unknown status isn't recovery. If the service
   disappears, its incident is forgotten without claiming recovery.
5. After recovery, a new failure must pass through the full waiting window again.

Timing has the resolution of the polling interval and webhook delivery time.
The waiting window is based on this process's observations, not a Kubernetes
object's creation timestamp. Restarting the dashboard loses incident state:
existing failures wait through a new window and may notify again. Run one
replica; there is no coordination between multiple dashboard instances.

## Payload and delivery

A structured webhook event looks like:

```json
{
  "id": "a-stable-event-id",
  "state": "firing",
  "cluster": "production",
  "service": {
    "id": "Deployment/shop/orders",
    "namespace": "shop",
    "name": "orders",
    "title": "Orders",
    "status": "down",
    "ready": 0,
    "desired": 2
  },
  "problems": [{"kind": "crash-loop", "title": "Keeps crashing"}],
  "startedAt": "2026-10-27T12:00:00Z",
  "observedAt": "2026-10-27T12:05:00Z",
  "dashboardUrl": "https://clusterview.example.com",
  "text": "Needs attention: shop/orders on production\n0/2 replicas ready\nKeeps crashing\nhttps://clusterview.example.com"
}
```

Recovery uses `state: resolved` and the service's current health. Each POST
has `Content-Type: application/json` and `Idempotency-Key` equal to the event
ID. Retries keep that ID. A new incident and its recovery have different IDs.
Only service identity, health and critical problem kinds/titles leave the
cluster. Logs, environment values, pod specs and raw event/problem detail
aren't included. The destination URL and response bodies are never logged.

Delivery has a ten-second timeout. Any non-2xx response is a failure, including
redirects (which aren't followed). Failed deliveries back off from 15 seconds
to at most five minutes, measured from when the attempt finishes. A
`Retry-After` value can extend the wait up to one hour; a 429 pauses every
service sharing the destination. Recovery deliveries retry too. There is no
durable delivery queue, and an incident that recovers
before any firing delivery succeeds is discarded.

The receiver may accept a request even if its response is lost. Custom
receivers should deduplicate by event ID; Slack and Teams may show a duplicate
in that case. A successful HTTP response means the webhook accepted the event,
not that downstream workflow steps necessarily completed.

## Implementation and checks

- `internal/alerts/alerts.go`: incident lifecycle, polling and retry scheduling.
- `internal/cluster/cache.go`: a scoped health snapshot that doesn't enqueue
  AI descriptions; dashboard snapshots retain their AI enrichment.
- `internal/alerts/webhook.go`: payload formats and bounded HTTP delivery.
- `cmd/clusterview/main.go`: configuration and process lifecycle.
- `charts/clusterview/`: opt-in configuration and the Secret reference.

`go test -race ./internal/alerts` exercises real local HTTP receivers and
controlled health changes: delay, flapping, duplicate suppression, recovery,
deletion, retries, 429 cooldown, payload formats, redirects and safe errors.
No test posts to a real Slack or Teams destination.
