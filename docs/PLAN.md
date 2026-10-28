# clusterview: product and build plan

## 1. What we're building

A Kubernetes dashboard that anyone can install in their own cluster with one
command. It shows the cluster the way people think about it, as **services**,
not as Pods, ReplicaSets and YAML.

- **A service map.** Each service is a box. Its pods sit inside it, with
  replicas shown as running versus wanted.
- **Obvious trouble.** A broken service pulses red, and its box says why in
  plain words: "crashing on start", "out of memory", "image not found".
- **Fix it from the box.** Read its logs and recent events. **Find error**
  asks AI to explain the failure and suggest a fix. Restart or scale it.
- **Knows what things are.** It spots databases, queues and operator-managed
  services (RabbitMQ, Postgres, Kafka…) and gives every box a title and a
  one-line description.
- **Safe by default.** Read-only out of the box. Actions are opt-in, narrow and
  logged.

**Where we stand out:** Headlamp, Lens and Komodor show raw Kubernetes objects
in tables and expect you to know what a ReplicaSet is. We show services, health
and what to do next.

## 2. Starting point

The core began as an internal prototype: a read-only map of one cluster's
workloads with pod states, autoscaling ranges, a nodes view, pod logs and
restart actions. Its logic was ported here and its hand-written catalogue of
services replaced by discovery.

## 3. Gaps: what a generic product needs that the prototype doesn't have

1. **Discovery instead of a catalogue.** Group pods into services by following
   each pod's owner chain (Pod → ReplicaSet → Deployment, Pod → StatefulSet →
   custom resource) and reading the standard `app.kubernetes.io/*` and Helm
   labels. When the top of the chain is a custom resource, an operator manages
   that service, and its type (`rabbitmq.com`, `postgresql.cnpg.io`…) says what
   it is.
2. **Every workload kind.** Deployments, StatefulSets, DaemonSets and Jobs are
   covered since phase 2; CronJobs, bare and static pods, and ReplicaSets an
   operator runs (Argo Rollouts) since phase 3.
3. **Kubernetes events.** These aren't used today. They hold the real reasons:
   failed scheduling, failed volume mounts, image pull errors, OOM kills,
   failing health probes.
4. **Wider health signals.** Pending pods and why, PVCs that won't bind, nodes
   under pressure or not ready, failed Jobs and CronJobs, rollouts that are
   stuck.
5. **Links between services.** Today the lines are written by hand. Discover
   them from Services and from environment variables that point at another
   Service's DNS name (variable *names* and hostnames only, never Secrets).
6. **Descriptions.** In priority order: the user's annotation
   (`clusterview.io/description`) → a built-in catalogue of well-known images
   (postgres, redis, nginx, rabbitmq…) → an AI guess, marked as a guess.
7. **Live updates.** Replace 15-second polling with Kubernetes watches,
   streamed to the browser over Server-Sent Events.
8. **Who can see and do what.** The prototype relied on its host app's admin
   check. clusterview needs its own:
   - port-forward with no login by default
   - single sign-on through OIDC or an auth proxy
   - viewer and operator roles, mapped from SSO groups
   - an audit log of every action
9. **Scope.** Whole cluster, or a chosen list of namespaces, set at install time.
10. **Running outside the cluster.** A laptop mode that uses your kubeconfig, so
    people can try it without installing anything in the cluster.

## 4. Packaging and install (industry standard)

| Decision | Choice | Why |
|---|---|---|
| Backend | **Go**, using the official Kubernetes client (`client-go`) and informers | The standard for Kubernetes tools (Headlamp, k9s, Argo). Live watches, a small static binary |
| Frontend | **React + TypeScript + Vite**, built into the Go binary | One artifact, one container, no separate web server |
| Image | Distroless, non-root, read-only root filesystem, amd64 + arm64, on `ghcr.io/clusterview/clusterview` | Passes common security scans; runs on Graviton and Apple Silicon clusters |
| Main install | **Helm chart** published as an OCI artifact to `ghcr.io/clusterview/charts/clusterview` | `helm install` in one line; works with Argo CD and Flux |
| Plain install | One `install.yaml` per release | For teams without Helm: `kubectl apply -f` |
| Laptop mode | The same binary run as `clusterview` or a **kubectl plugin** via krew (`kubectl clusterview`) | Try it with no in-cluster install |
| Releases | GoReleaser + GitHub Actions; signed images (cosign) and an SBOM | What security teams ask for |
| Listing | Artifact Hub, once the repo is public | Where people look for charts |

What the chart creates:

- Deployment (1 replica)
- ServiceAccount
- Service
- Optional Ingress
- Optional NetworkPolicy
- ClusterRole + ClusterRoleBinding for read-only access to workloads, pods, pod
  logs, events, nodes, Services, PVCs, HPAs, CRD definitions and metrics. The
  default list grants no Secrets and no exec.
- A small ConfigMap or PVC for the cached descriptions

A values switch, `actions.enabled`, adds a second, narrow role. It grants:

- delete pods (restart)
- patch Deployments and StatefulSets (rolling restart, scale within a limit)
- create Jobs from a CronJob (rerun)

An optional `actions.namespaces` list limits where actions apply. The chart
ships `values.schema.json` so bad settings fail at install, and the chart is
tested on a real cluster (kind) in CI.

The install, start to finish:

```bash
helm install clusterview oci://ghcr.io/clusterview/charts/clusterview -n clusterview --create-namespace
kubectl -n clusterview port-forward svc/clusterview 8080
```

## 5. Repo layout

```
cmd/clusterview/        main: serve in-cluster or run locally from kubeconfig
internal/kube/          client, informers, cache
internal/discovery/     owner chains, service grouping, operator detection, links
internal/health/        status rules, reasons in plain words, events
internal/describe/      annotations, known-images catalogue, AI descriptions
internal/actions/       restart, scale, rerun; permission checks; audit log
internal/logs/          pod logs and follow; optional Loki
internal/ai/            Find error: gather evidence, call the model, redact
internal/auth/          none / OIDC / auth-proxy headers; viewer and operator
internal/api/           REST + SSE stream for the UI
web/                    React app (embedded into the binary at build time)
charts/clusterview/     Helm chart
deploy/                 generated install.yaml
docs/                   this plan, architecture, decisions
```

## 6. Phases

Each phase ends with something that runs, is tested in CI, and can be shown to
someone.

### Phase 0: repo and plan (this document) ✅
- [x] Repo: public at `clusterview/clusterview`
- [x] Agree this plan and the working name
- [ ] License (Apache-2.0 if we open-source)

### Phase 1: skeleton that installs ✅ (v0.1.0)
- Go module, Vite app embedded in the binary, health endpoint, placeholder page
- Dockerfile, Helm chart, `install.yaml`, GitHub Actions: lint, test, build, kind install test
- Release pipeline: tag → image + chart on ghcr

**Done when:** `helm install` on a fresh kind cluster serves the page, and CI proves it on every PR.

### Phase 2: generic core, ported from the prototype ✅
- Informers for workloads, pods, HPAs, Jobs, nodes and metrics; the snapshot API with SSE updates
- Pod state, replica counts, HPA/KEDA ranges and the nodes view, ported with their tests
- Pod logs (container picker, previous run, follow)
- Brought forward from phase 3: StatefulSets and DaemonSets, and linking pods to workloads by owner chain instead of label selectors

**Done when:** on a test cluster, the API returns every Deployment with correct replicas and pod states, and live changes reach the browser within 2 seconds.

### Phase 3: fill the gaps ✅
- Discovery: owner chains, grouping into services, operator detection, links between services
- All workload kinds, Kubernetes events, the wider health signals
- Descriptions from annotations and the known-images catalogue
- Actions behind `actions.enabled`, with the operator role and audit log
- Auth: port-forward default, OIDC / auth-proxy, viewer and operator roles
- Namespace scoping; laptop mode

**Done when:** pointed at a cluster running a sample app plus RabbitMQ, CloudNativePG and ingress-nginx installed by their operators/charts, it groups each into one named service with a description and correct health, without any config.

Shipped in #9 (services, operators, descriptions), #10 (problems and events), #11 (links), #15 (actions and audit), #13 (sign-in and roles) and #14 (namespace scoping and laptop mode). The done-when check passed on kind with the RabbitMQ Cluster Operator, CloudNativePG, ingress-nginx and cert-manager.

### Phase 4: the UI ✅
- Design pass first: a small design system (tokens, light/dark), the service box, alert states and motion
- Service map with automatic layout (ELK), grouped by namespace or app
- Detail drawer: pods, events, logs, actions
- Totals and "needs attention" list; nodes view

**Done when:** someone who doesn't know Kubernetes can spot the broken service and see why within 10 seconds on a seeded demo cluster.

Shipped in #19 (design system, service map, detail drawer) and the list/nodes/search PR. On the kind test cluster with seven broken services, the map with all seven red cards, each naming its problem, renders about 0.1 s after the page loads.

### Phase 5: AI ✅
- **Find error**: gathers the failing pod's logs, events, restart reason and spec summary, removes anything that looks like a secret, then asks the model for a plain explanation and a suggested fix
- AI descriptions for services the catalogue doesn't know, cached per image and marked "AI guess"
- Bring your own key (OpenAI, Anthropic, or a local OpenAI-compatible model); everything else works without one

**Done when:** on five seeded failures (crash loop, OOM, bad image, missing config, failing probe), Find error names the real cause in at least four.

Shipped in #21. On the kind test cluster with OpenAI `gpt-5.5`, Find error named the real cause of all five failures in `test/eval/failures.yaml`, each with high confidence, in about 25 s apiece (`hack/find-error-eval.sh`). In an earlier run one request hung until the timeout; OpenAI calls are now tried a second time. Base images such as busybox get no AI guess, since their name says nothing about the service.

### Phase 6: extensions (in progress)
- [x] Alerts to Slack / Teams / webhooks for services that stay broken
- History (what broke, when, what was done)
- Add-ons for well-known services, starting with RabbitMQ (queues, consumers, dead-letter queues)
- Multiple clusters in one view (likely the paid tier)
- Public release: open the repo, Artifact Hub, krew index, docs site

The first slice is [service alerts](features/service-alerts.md): opt-in,
five-minute sustained-failure window, one notification per incident plus
recovery, scoped to the existing cluster reader. Delivery retries with
backoff and supports Slack, Teams Workflows and structured webhooks. Tests
cover transient failures, duplicate suppression, recovery, namespace identity,
rate limiting, delivery failures and credential-safe errors.

## 7. Open questions

1. Final product name, before the repo goes public.
2. Open-source core plus paid tier, or fully internal?
3. Where state lives: none, a ConfigMap, or SQLite on a small PVC (needed for the audit log and history).
4. Which AI providers to support first.
5. Prerequisite: Go isn't installed on the dev machine yet (`brew install go`).
