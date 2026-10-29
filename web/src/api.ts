import { mockResponse } from "./mock";

export interface ClusterInfo {
  connected: boolean;
  mode?: "in-cluster" | "kubeconfig";
  context?: string;
  serverVersion?: string;
  error?: string;
}

export interface VersionInfo {
  version: string;
  commit: string;
}

export type Status = "healthy" | "idle" | "degraded" | "down" | "unknown";

export interface PodState {
  name: string;
  namespace: string;
  node?: string;
  phase: string;
  ready: boolean;
  readyContainers: string;
  restarts: number;
  reason?: string;
  stuck: boolean;
  startedAt?: string;
}

export type Category =
  | "app"
  | "database"
  | "queue"
  | "cache"
  | "search"
  | "storage"
  | "ingress"
  | "network"
  | "dns"
  | "monitoring"
  | "security"
  | "platform"
  | "job";

export interface Owner {
  apiVersion: string;
  kind: string;
  name: string;
}

export interface Problem {
  kind: string;
  severity: "critical" | "warning";
  title: string;
  detail?: string;
  pods?: string[];
}

export interface ClusterEvent {
  reason: string;
  message: string;
  object: string;
  count: number;
  lastSeen: string;
}

export interface Link {
  from: string;
  to: string;
  via: string;
  evidence: string;
}

export interface Address {
  name: string;
  namespace: string;
  type: string;
  ports: string[];
  external?: string[];
}

export interface Entrypoint {
  host?: string;
  path?: string;
  tls: boolean;
  ingress: string;
  class?: string;
}

export interface Service {
  id: string;
  namespace: string;
  name: string;
  title: string;
  description?: string;
  descriptionSource?: "annotation" | "operator" | "image" | "ai";
  category: Category;
  app?: string;
  managedBy?: Owner;
  status: Status;
  ready: number;
  desired: number;
  workloads: string[];
  problems: Problem[];
  addresses: Address[];
  entrypoints: Entrypoint[];
}

export interface Workload {
  id: string;
  kind: "Deployment" | "StatefulSet" | "DaemonSet" | "Job" | "CronJob" | "ReplicaSet" | "Pod";
  namespace: string;
  name: string;
  status: Status;
  desired: number;
  ready: number;
  available: number;
  updated: number;
  image?: string;
  imageTag?: string;
  createdAt: string;
  message?: string;
  autoscaling?: { min: number; max: number; current: number; managedBy: "hpa" | "keda" };
  owner?: Owner;
  app?: string;
  schedule?: string;
  suspended?: boolean;
  lastRun?: string;
  pods: PodState[];
  problems: Problem[];
  events: ClusterEvent[];
}

export interface Snapshot {
  version: number;
  fetchedAt: string;
  summary: {
    services: number;
    workloads: number;
    needAttention: number;
    pods: number;
    readyPods: number;
    restarts: number;
    namespaces: number;
  };
  services: Service[];
  links: Link[];
  workloads: Workload[];
}

export interface NodePod {
  name: string;
  namespace: string;
  workloadId?: string;
  workload: string;
  phase: string;
  ready: boolean;
  terminating: boolean;
  reason?: string;
  restarts: number;
  cpuRequestMillicores: number;
  memoryRequestBytes: number;
  cpuUsageMillicores?: number;
  memoryUsageBytes?: number;
}

export interface ClusterNode {
  name: string;
  ready: boolean;
  unschedulable: boolean;
  pressure: string[];
  instanceType?: string;
  zone?: string;
  pool?: string;
  kubeletVersion?: string;
  cpuAllocatableMillicores: number;
  memoryAllocatableBytes: number;
  podsAllocatable: number;
  cpuRequestedMillicores: number;
  memoryRequestedBytes: number;
  cpuUsageMillicores?: number;
  memoryUsageBytes?: number;
  pods: NodePod[];
}

export interface Nodes {
  fetchedAt: string;
  metrics: boolean;
  nodes: ClusterNode[];
  unscheduled: NodePod[];
}

export interface LogLine {
  timestamp?: string;
  text: string;
}

export interface PodLogs {
  namespace: string;
  pod: string;
  container: string;
  containers: string[];
  previous: boolean;
  lines: LogLine[];
  notice?: string;
}

async function getJSON<T>(path: string): Promise<T> {
  const mocked = await mockResponse(path);
  if (mocked !== undefined) return mocked as T;
  const res = await fetch(path);
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(body.error ?? `${path}: ${res.status}`);
  }
  return (await res.json()) as T;
}

export const getCluster = () => getJSON<ClusterInfo>("api/v1/cluster");
export const getVersion = () => getJSON<VersionInfo>("api/v1/version");
export const getSnapshot = () => getJSON<Snapshot>("api/v1/snapshot");
export const getNodes = () => getJSON<Nodes>("api/v1/nodes");

function logsPath(ns: string, pod: string) {
  return `api/v1/namespaces/${encodeURIComponent(ns)}/pods/${encodeURIComponent(pod)}/logs`;
}

export function getLogs(ns: string, pod: string, container: string, previous: boolean) {
  const q = new URLSearchParams({ tailLines: "500" });
  if (container) q.set("container", container);
  if (previous) q.set("previous", "true");
  return getJSON<PodLogs>(`${logsPath(ns, pod)}?${q}`);
}

export function followLogsURL(ns: string, pod: string, container: string) {
  const q = new URLSearchParams({ tailLines: "1" });
  if (container) q.set("container", container);
  return `${logsPath(ns, pod)}/follow?${q}`;
}

export interface ActionsConfig {
  enabled: boolean;
  namespaces: string[];
  maxReplicas: number;
}

export interface AuditEntry {
  time: string;
  actor: string;
  action: string;
  target: string;
  detail?: string;
  ok: boolean;
  error?: string;
}

export const getActionsConfig = () => getJSON<ActionsConfig>("api/v1/actions");

async function act(path: string, body?: unknown): Promise<AuditEntry> {
  const mocked = await mockResponse(path);
  if (mocked !== undefined) return mocked as AuditEntry;
  const res = await fetch(path, {
    method: "POST",
    headers: { "X-Clusterview-Action": "1", "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const json = (await res.json().catch(() => ({}))) as AuditEntry & { error?: string };
  if (!res.ok) throw new Error(json.error ?? `${res.status}`);
  return json;
}

const seg = encodeURIComponent;
export const restartPod = (ns: string, pod: string) => act(`api/v1/namespaces/${seg(ns)}/pods/${seg(pod)}/restart`);
export const restartWorkload = (kind: string, ns: string, name: string) =>
  act(`api/v1/workloads/${seg(kind)}/${seg(ns)}/${seg(name)}/restart`);
export const scaleWorkload = (kind: string, ns: string, name: string, replicas: number) =>
  act(`api/v1/workloads/${seg(kind)}/${seg(ns)}/${seg(name)}/scale`, { replicas });
export const runCronJob = (ns: string, name: string) => act(`api/v1/workloads/CronJob/${seg(ns)}/${seg(name)}/run`);

export interface Me {
  name: string;
  groups: string[];
  role: "" | "viewer" | "operator";
  mode: "none" | "proxy" | "oidc" | "password";
}

export const getMe = () => getJSON<Me>("api/v1/me");

export interface AIInfo {
  enabled: boolean;
  model?: string;
  describe: boolean;
}

export interface Diagnosis {
  serviceId: string;
  summary: string;
  cause: string;
  evidence: string[];
  fix: string[];
  confidence: "high" | "medium" | "low";
  model: string;
  askedAt: string;
  sent: string;
  redactions: number;
}

export const getAI = () => getJSON<AIInfo>("api/v1/ai");

export async function findError(serviceId: string, fresh: boolean): Promise<Diagnosis> {
  const mocked = await mockResponse("api/v1/diagnose", { serviceId });
  if (mocked !== undefined) return mocked as Diagnosis;
  const res = await fetch("api/v1/diagnose", {
    method: "POST",
    headers: { "X-Clusterview-Action": "1", "Content-Type": "application/json" },
    body: JSON.stringify({ serviceId, fresh }),
  });
  const json = (await res.json().catch(() => ({}))) as Diagnosis & { error?: string };
  if (!res.ok) throw new Error(json.error ?? `${res.status}`);
  return json;
}
