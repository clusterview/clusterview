import { useEffect, useMemo, useState } from "react";
import { ArrowDownLeft, ArrowUpRight, Globe, X } from "lucide-react";
import type { ActionsConfig, AIInfo, Me, PodState, Problem, Service, Snapshot, Workload } from "../api";
import { CategoryIcon, categoryLabel } from "../components/CategoryIcon";
import { PodPips, toneOf } from "../components/ServiceCard";
import { StatusPill, Tabs } from "../components/ui";
import { age } from "../format";
import { describeSchedule } from "../format-cron";
import { FindError } from "./FindError";
import { LogsTab } from "./LogsTab";
import { ServiceActions, PodRestartButton, canActIn } from "./actions";
import "./drawer.css";

type Tab = "overview" | "logs" | "events";

interface Props {
  service: Service;
  snapshot: Snapshot;
  me: Me | null;
  actions: ActionsConfig | null;
  ai: AIInfo | null;
  onClose: () => void;
  onSelect: (id: string) => void;
}

const sourceNote: Record<string, string> = {
  annotation: "Written by your team",
  operator: "From the operator that runs it",
  image: "From the image it runs",
  ai: "AI guess from the image name",
};

export function ServiceDrawer({ service: s, snapshot, me, actions, ai, onClose, onSelect }: Props) {
  const [tab, setTab] = useState<Tab>(() => (new URLSearchParams(window.location.search).get("tab") as Tab) || "overview");
  const [logsPod, setLogsPod] = useState<string | null>(null);

  const workloads = useMemo(() => {
    const byId = new Map(snapshot.workloads.map((w) => [w.id, w]));
    return s.workloads.map((id) => byId.get(id)).filter((w): w is Workload => !!w);
  }, [s, snapshot]);
  const pods = workloads.flatMap((w) => w.pods);
  const events = workloads
    .flatMap((w) => w.events)
    .sort((a, b) => b.lastSeen.localeCompare(a.lastSeen));
  const tone = toneOf(s);
  const canAct = canActIn(me, actions, s.namespace);

  // A new service opens on its overview.
  const [shownId, setShownId] = useState(s.id);
  useEffect(() => {
    if (s.id === shownId) return;
    setShownId(s.id);
    setTab("overview");
    setLogsPod(null);
  }, [s.id, shownId]);

  const openLogs = (pod: string) => {
    setLogsPod(pod);
    setTab("logs");
  };

  return (
    <aside className="cv-drawer" aria-label={`${s.title} details`}>
      <header className="cv-drawer-head">
        <CategoryIcon category={s.category} size={44} />
        <div className="cv-drawer-titles">
          <h2>{s.title}</h2>
          <p>
            <span className="cv-mono">{s.name}</span>
            <span className="cv-dot-sep" />
            {s.namespace}
            <span className="cv-dot-sep" />
            {s.managedBy ? `${s.managedBy.kind} (operator)` : (workloads[0]?.kind ?? "")}
          </p>
        </div>
        <button className="cv-icon-btn" onClick={onClose} aria-label="Close">
          <X size={18} />
        </button>
      </header>

      <div className="cv-drawer-status">
        <StatusPill status={tone === "warning" ? "warning" : s.status} />
        {s.desired > 0 && (
          <span className="cv-drawer-replicas">
            <PodPips pods={workloads.filter((w) => w.kind !== "Job" && w.kind !== "CronJob").flatMap((w) => w.pods)} desired={s.desired} />
            {s.ready} of {s.desired} ready
          </span>
        )}
        <span className="cv-drawer-spacer" />
        {canAct && <ServiceActions service={s} workloads={workloads} maxReplicas={actions!.maxReplicas} />}
      </div>

      <Tabs
        tabs={[
          { id: "overview", label: "Overview" },
          { id: "logs", label: "Logs" },
          { id: "events", label: "Warnings", badge: events.length },
        ]}
        value={tab}
        onChange={setTab}
      />

      <div className="cv-drawer-body">
        {tab === "overview" && (
          <>
            {s.problems.length > 0 && (
              <section className="cv-section">
                <h3>What's wrong</h3>
                {s.problems.map((p) => (
                  <ProblemCard key={p.kind} problem={p} onLogs={openLogs} />
                ))}
                {ai?.enabled && <FindError service={s} ai={ai} />}
              </section>
            )}

            {s.description && (
              <section className="cv-section">
                <h3>What it is</h3>
                <p className="cv-about">{s.description}</p>
                <p className="cv-note">
                  {categoryLabel[s.category]}
                  {s.descriptionSource && <> · {sourceNote[s.descriptionSource]}</>}
                </p>
              </section>
            )}

            {pods.length > 0 && (
              <section className="cv-section">
                <h3>
                  Pods <span className="cv-count">{pods.length}</span>
                </h3>
                <ul className="cv-pods">
                  {pods.map((p) => (
                    <PodRow
                      key={p.name}
                      pod={p}
                      onLogs={() => openLogs(p.name)}
                      action={canAct && workloads.find((w) => w.pods.includes(p))?.kind !== "Pod" ? <PodRestartButton pod={p} /> : null}
                    />
                  ))}
                </ul>
              </section>
            )}

            <Connections service={s} snapshot={snapshot} onSelect={onSelect} />

            <section className="cv-section">
              <h3>Details</h3>
              <dl className="cv-details">
                {workloads.map((w) => (
                  <WorkloadDetails key={w.id} w={w} />
                ))}
                {s.addresses.map((a) => (
                  <div key={`${a.namespace}/${a.name}`} className="cv-detail-row">
                    <dt>Address</dt>
                    <dd className="cv-mono">
                      {a.name}.{a.namespace}:{a.ports.join(", ")}
                      {a.type !== "ClusterIP" && <span className="cv-muted"> · {a.type}</span>}
                      {a.external?.map((x) => (
                        <span key={x} className="cv-muted">
                          {" "}
                          · {x}
                        </span>
                      ))}
                    </dd>
                  </div>
                ))}
              </dl>
            </section>
          </>
        )}

        {tab === "logs" && <LogsTab pods={pods} initialPod={logsPod ?? pickLogPod(pods)} />}

        {tab === "events" &&
          (events.length === 0 ? (
            <p className="cv-empty-note">No warnings from Kubernetes in the last hour.</p>
          ) : (
            <ul className="cv-events">
              {events.map((e, i) => (
                <li key={i}>
                  <div className="cv-event-head">
                    <strong>{e.reason}</strong>
                    <span className="cv-muted cv-mono">{e.object}</span>
                    <span className="cv-event-time">
                      {age(e.lastSeen)} ago{e.count > 1 && ` · ×${e.count}`}
                    </span>
                  </div>
                  <p>{e.message}</p>
                </li>
              ))}
            </ul>
          ))}
      </div>
    </aside>
  );
}

/** The pod whose logs most likely explain the service: a stuck one first. */
function pickLogPod(pods: PodState[]) {
  return (pods.find((p) => p.stuck) ?? pods.find((p) => !p.ready) ?? pods[0])?.name ?? null;
}

function ProblemCard({ problem: p, onLogs }: { problem: Problem; onLogs: (pod: string) => void }) {
  return (
    <div className={`cv-problem is-${p.severity}`}>
      <strong>{p.title}</strong>
      {p.detail && <p>{p.detail}</p>}
      {p.pods && p.pods.length > 0 && (
        <div className="cv-problem-pods">
          {p.pods.map((name) => (
            <button key={name} className="cv-chip" onClick={() => onLogs(name)} title="Show its logs">
              <span className="cv-mono">{name}</span>
              <span className="cv-chip-action">Logs</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function PodRow({ pod: p, onLogs, action }: { pod: PodState; onLogs: () => void; action: React.ReactNode }) {
  const state = p.ready ? "is-ready" : p.stuck ? "is-stuck" : "is-starting";
  return (
    <li className="cv-pod">
      <span className={`cv-pip ${state}`} />
      <div className="cv-pod-main">
        <span className="cv-mono cv-pod-name">{p.name}</span>
        <span className="cv-pod-meta">
          {p.reason ?? (p.ready ? "Running" : p.phase)}
          {p.restarts > 0 && ` · ${p.restarts} restart${p.restarts === 1 ? "" : "s"}`}
          {p.startedAt && ` · ${age(p.startedAt)}`}
          {p.node && ` · ${p.node}`}
        </span>
      </div>
      <button className="cv-btn is-ghost is-sm" onClick={onLogs}>
        Logs
      </button>
      {action}
    </li>
  );
}

function Connections({ service: s, snapshot, onSelect }: { service: Service; snapshot: Snapshot; onSelect: (id: string) => void }) {
  const byId = new Map(snapshot.services.map((x) => [x.id, x]));
  const out = snapshot.links.filter((l) => l.from === s.id).map((l) => ({ l, svc: byId.get(l.to) }));
  const into = snapshot.links.filter((l) => l.to === s.id).map((l) => ({ l, svc: byId.get(l.from) }));
  if (!out.length && !into.length && !s.entrypoints.length) return null;
  const chip = (svc: Service | undefined, title: string) =>
    svc && (
      <button key={svc.id} className="cv-conn" onClick={() => onSelect(svc.id)} title={title}>
        <CategoryIcon category={svc.category} size={22} />
        {svc.title}
        <span className={`cv-status is-${toneOf(svc)}`} />
      </button>
    );
  return (
    <section className="cv-section">
      <h3>Connections</h3>
      {s.entrypoints.length > 0 && (
        <div className="cv-conn-row">
          <span className="cv-conn-label">
            <Globe size={14} /> From outside
          </span>
          <div className="cv-conn-list">
            {s.entrypoints.map((e, i) => (
              <span key={i} className="cv-host cv-mono">
                {e.tls ? "https" : "http"}://{e.host ?? "*"}
                {e.path ?? ""}
              </span>
            ))}
          </div>
        </div>
      )}
      {out.length > 0 && (
        <div className="cv-conn-row">
          <span className="cv-conn-label">
            <ArrowUpRight size={14} /> Calls
          </span>
          <div className="cv-conn-list">{out.map(({ l, svc }) => chip(svc, `${l.via}, found in ${l.evidence}`))}</div>
        </div>
      )}
      {into.length > 0 && (
        <div className="cv-conn-row">
          <span className="cv-conn-label">
            <ArrowDownLeft size={14} /> Called by
          </span>
          <div className="cv-conn-list">{into.map(({ l, svc }) => chip(svc, `${l.via}, found in ${l.evidence}`))}</div>
        </div>
      )}
    </section>
  );
}

function WorkloadDetails({ w }: { w: Workload }) {
  return (
    <>
      <div className="cv-detail-row">
        <dt>{w.kind}</dt>
        <dd className="cv-mono">{w.name}</dd>
      </div>
      {w.image && (
        <div className="cv-detail-row">
          <dt>Image</dt>
          <dd className="cv-mono cv-break">{w.image}</dd>
        </div>
      )}
      {w.autoscaling && (
        <div className="cv-detail-row">
          <dt>Autoscaling</dt>
          <dd>
            {w.autoscaling.managedBy === "keda" ? "KEDA" : "HPA"}, {w.autoscaling.min} to {w.autoscaling.max} replicas (now {w.autoscaling.current})
          </dd>
        </div>
      )}
      {w.schedule && (
        <div className="cv-detail-row">
          <dt>Schedule</dt>
          <dd>
            {describeSchedule(w.schedule)} <span className="cv-muted cv-mono">({w.schedule})</span>
            {w.suspended && " · suspended"}
            {w.lastRun && <span className="cv-muted"> · last ran {age(w.lastRun)} ago</span>}
          </dd>
        </div>
      )}
      {w.message && (
        <div className="cv-detail-row">
          <dt>Kubernetes says</dt>
          <dd>{w.message}</dd>
        </div>
      )}
    </>
  );
}

