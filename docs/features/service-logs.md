# Service logs

Open a service and choose **All logs** beside its pods, or open its **Logs**
tab. **All pods** combines the service's current pod logs in timestamp order,
with pod and container labels on every line. Regular and init containers are
included. Select a pod or container to narrow the view; the existing per-pod
**Logs** buttons still open that pod directly.

**Follow** streams recent history (up to 500 lines per container) and new lines
through one browser connection. Watched pod additions, replacements and container
restarts update the sources automatically. Logs already received from a removed
pod remain in the view until they fall out of its recent history. Identical
messages, including ones with equal timestamps, remain separate lines.

**Filter** matches message text, pod names and container names. **Errors only**
highlights likely failures using the same text matching as individual pod logs.
**Before last restart** reads each selected container's previous instance and
pauses following; Kubernetes may have no previous log for some containers.
Unavailable logs produce a notice naming the source while other sources remain
visible. These are container logs from Kubernetes, not an archive of deleted pods.

## Limits and reconnects

- A view opens at most 20 container streams, sorted by namespace, pod and container.
  If more exist, a notice shows the total and asks you to select a pod or container.
- The browser keeps the most recent 5,000 lines within roughly one million text
  characters, retaining a single long line when necessary.
- History reads run at most four requests concurrently, with a shared 4 MiB budget
  and a 15-second timeout. The merged response keeps at most 5,000 lines.
- The browser reconnects when its connection drops, replacing its displayed
  history with a fresh tail. If an individual container stream fails, its notice
  identifies the source; toggle **Follow** off and on to open fresh streams.
- Ordering uses Kubernetes timestamps. Clock differences between nodes can affect
  their apparent order, and late lines may move into their timestamp position.

## API and scope

`GET /api/v1/services/logs?serviceId=<id>` returns labelled lines, available
containers, selected sources, their total count and notices.
`GET /api/v1/services/logs/follow?serviceId=<id>` sends SSE `sources`, `line`,
`notice` and `end` events. Both support `container`, `tailLines` (1–10,000;
default 500) and RFC 3339 `sinceTime`. History also supports `previous=true`;
following previous logs is rejected.

The service ID comes from the snapshot. The server resolves its workloads and
pods from the watched namespace cache, rather than accepting arbitrary pod
selectors. Both endpoints use existing sign-in and viewer authorization, work
under `basePath`, and use the existing `pods/log` read permission. There are no
additional Kubernetes grants, dependencies or action requirements.
