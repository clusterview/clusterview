// Dev-only fixtures, so the UI can be designed without a cluster:
// http://localhost:5173/?mock=demo (a small shop app) or ?mock=kind (a real
// kind cluster with a lot broken). import.meta.env.DEV is false in builds, so
// none of this reaches the binary.
import type { NodePod, Nodes, Snapshot } from "./api";

export const mockName = import.meta.env.DEV ? new URLSearchParams(window.location.search).get("mock") : null;

export async function mockResponse(path: string, body?: { serviceId?: string }): Promise<unknown | undefined> {
  if (!import.meta.env.DEV || !mockName) return undefined;
  const route = path.split("?")[0];
  switch (route) {
    case "/api/v1/snapshot":
      return mockName === "kind" ? (await import("./mock/kind.json")).default : (await import("./mock/demo.json")).default;
    case "/api/v1/nodes":
      return mockNodes((mockName === "kind" ? (await import("./mock/kind.json")).default : (await import("./mock/demo.json")).default) as unknown as Snapshot);
    case "/api/v1/cluster":
      return { connected: true, mode: "kubeconfig", context: mockName === "kind" ? "kind-clusterview" : "acme-prod", serverVersion: "v1.37.0" };
    case "/api/v1/version":
      return { version: "dev", commit: "mock" };
    case "/api/v1/me":
      return { name: "ann@acme.com", groups: ["platform"], role: "operator", mode: "oidc" };
    case "/api/v1/actions":
      return { enabled: true, namespaces: [], maxReplicas: 20 };
    case "/api/v1/audit":
      return [];
    case "/api/v1/ai":
      return { enabled: new URLSearchParams(window.location.search).get("ai") !== "off", model: "Anthropic · claude-opus-5-5", describe: true };
    case "/api/v1/diagnose":
      await new Promise((r) => setTimeout(r, 2200));
      return mockDiagnosis(body?.serviceId ?? "");
  }
  const logs = route.match(/^\/api\/v1\/namespaces\/([^/]+)\/pods\/([^/]+)\/logs$/);
  if (logs) return mockLogs(logs[1], decodeURIComponent(logs[2]), path.includes("previous=true"));
  const action = route.match(/\/(restart|scale|run)$/);
  if (action) return { time: new Date().toISOString(), actor: "ann@acme.com", action: { restart: "Restarted", scale: "Scaled", run: "Ran now" }[action[1]], target: route, ok: true };
  return undefined;
}

function mockDiagnosis(serviceId: string) {
  return {
    serviceId,
    summary: "The payments API crashes on start because the payment provider rejects its API key.",
    cause:
      "Each time the container starts it connects to the database fine, then the payment provider answers 401 Unauthorized to its API key and the app exits. Kubernetes restarts it and waits longer each time, which is the crash loop you see. The key comes from the Secret payments, key provider-key, so that Secret most likely holds an old or revoked key.",
    evidence: [
      "ERROR Payment provider rejected the API key: 401 Unauthorized (key ends …4f2a)",
      "FATAL could not start: payment provider unavailable",
      "container app: ready false, 7 restarts, waiting: CrashLoopBackOff; last run ended: Error, exit code 1",
      "env PROVIDER_KEY = from Secret payments, key provider-key",
    ],
    fix: [
      "Check which key the Secret holds: kubectl -n shop get secret payments -o jsonpath='{.data.provider-key}' | base64 -d | tail -c 4",
      "Create a new key in the payment provider's dashboard and put it in the Secret: kubectl -n shop create secret generic payments --from-literal=provider-key=<new key> --dry-run=client -o yaml | kubectl apply -f -",
      "Restart the deployment so the pods read the new key: kubectl -n shop rollout restart deployment/payments-api",
    ],
    confidence: "high",
    model: "Anthropic · claude-opus-5-5",
    askedAt: new Date().toISOString(),
    sent: "Service: Payments API (Kubernetes name payments-api) in namespace shop\nStatus: down, 0 of 2 ready\n\n## Problems clusterview detected\n- [critical] Keeps crashing: …\n\n## Log of payments-api-7c9f-x2, container app, before its last restart (last 80 lines)\n<log>\nERROR Payment provider rejected the API key: 401 Unauthorized (key ends …4f2a)\n…\n</log>",
    redactions: 2,
  };
}

function mockLogs(namespace: string, pod: string, previous: boolean) {
  const t = (s: number) => new Date(Date.UTC(2026, 9, 27, 9, 58, s)).toISOString().replace(".000Z", ".000000000Z");
  const crash = pod.startsWith("payments-api");
  const text = crash
    ? [
        "Starting payments-api 3.2.0",
        "Loading config from /etc/payments/config.yaml",
        "Connecting to postgres at orders-db-rw.data:5432",
        "Connected to postgres",
        "Initialising payment provider client",
        "ERROR Payment provider rejected the API key: 401 Unauthorized (key ends …4f2a)",
        "Traceback (most recent call last):",
        '  File "/app/payments/provider.py", line 88, in connect',
        "    raise ProviderAuthError(resp.status, resp.reason)",
        "payments.provider.ProviderAuthError: 401 Unauthorized",
        "FATAL could not start: payment provider unavailable",
      ]
    : ["Server listening on :8080", "GET /healthz 200 0.4ms", "GET /orders/8812 200 12.1ms", "POST /orders 201 48.0ms", "GET /healthz 200 0.3ms"];
  return {
    namespace,
    pod,
    container: "app",
    containers: ["app"],
    previous,
    lines: crash && !previous ? [] : text.map((x, i) => ({ timestamp: t(i * 3), text: x })),
    notice: crash && !previous ? "container \"app\" in pod is waiting to start: back-off restarting failed container" : undefined,
  };
}

/** Nodes made up from the snapshot's pods: each pod's node, plausible sizes. */
function mockNodes(snap: Snapshot): Nodes {
  const byNode = new Map<string, NodePod[]>();
  const unscheduled: NodePod[] = [];
  for (const w of snap.workloads) {
    for (const p of w.pods) {
      const np: NodePod = {
        name: p.name,
        namespace: p.namespace,
        workloadId: w.id,
        workload: w.name,
        phase: p.phase,
        ready: p.ready,
        terminating: false,
        reason: p.reason,
        restarts: p.restarts,
        cpuRequestMillicores: 250,
        memoryRequestBytes: 512 * 2 ** 20,
        cpuUsageMillicores: p.ready ? 120 : 5,
        memoryUsageBytes: (p.ready ? 300 : 40) * 2 ** 20,
      };
      if (!p.node) unscheduled.push(np);
      else byNode.set(p.node, [...(byNode.get(p.node) ?? []), np]);
    }
  }
  const zones = ["us-west-1a", "us-west-1b", "us-west-1c"];
  const nodes = [...byNode.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([name, pods], i) => {
    const req = pods.reduce((n, p) => n + p.cpuRequestMillicores, 0);
    const mem = pods.reduce((n, p) => n + p.memoryRequestBytes, 0);
    return {
      name,
      ready: true,
      unschedulable: false,
      pressure: i === 1 && pods.length > 6 ? ["MemoryPressure"] : [],
      instanceType: "m7g.xlarge",
      zone: zones[i % 3],
      pool: "general",
      kubeletVersion: "v1.37.0",
      cpuAllocatableMillicores: 3920,
      memoryAllocatableBytes: 15 * 2 ** 30,
      podsAllocatable: 58,
      cpuRequestedMillicores: req,
      memoryRequestedBytes: mem,
      cpuUsageMillicores: pods.reduce((n, p) => n + (p.cpuUsageMillicores ?? 0), 0) + 400,
      memoryUsageBytes: pods.reduce((n, p) => n + (p.memoryUsageBytes ?? 0), 0) + 2 * 2 ** 30,
      pods,
    };
  });
  return { fetchedAt: snap.fetchedAt, metrics: true, nodes, unscheduled };
}
