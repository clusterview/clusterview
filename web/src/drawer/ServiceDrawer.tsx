import { useMemo, useState } from "react";
import { Maximize2, Minimize2, ScrollText, X } from "lucide-react";
import type { ActionsConfig, AIInfo, Me, PodState, Problem, Service, Snapshot, Workload } from "../api";
import { CategoryIcon, categoryLabel } from "../components/CategoryIcon";
import { PodPips, toneOf } from "../components/ServiceCard";
import { StatusPill, Tabs } from "../components/ui";
import { age } from "../format";
import { describeSchedule } from "../format-cron";
import { FindError } from "./FindError";
import { LogsTab } from "./LogsTab";
import { Connections } from "./Connections";
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
  const [wide, setWide] = useState(false);

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

  const openLogs = (pod: string) => {
    setLogsPod(pod);
    setTab("logs");
  };

  return (
    <aside className={`cv-drawer${wide ? " is-wide" : ""}`} aria-label={`${s.title} details`}>
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
        <button className="cv-icon-btn cv-drawer-expand" onClick={() => setWide(!wide)} aria-label={wide ? "Restore details width" : "Expand details"} title={wide ? "Restore width" : "Expand for more room"}>
          {wide ? <Minimize2 size={16} /> : <Maximize2 size={16} />}
        </button>
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

      <div className={`cv-drawer-body${tab === "logs" ? " has-logs" : ""}`}>
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
                <h3 className="cv-pods-heading">
                  Pods <span className="cv-count">{pods.length}</span>
                  <button className="cv-btn is-ghost is-sm cv-all-logs" onClick={() => { setLogsPod(null); setTab("logs"); }}>
                    <ScrollText size={14} /> All logs
                  </button>
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

        {tab === "logs" && <LogsTab key={`${s.id}:${logsPod ?? "all"}`} serviceId={s.id} pods={pods} initialPod={logsPod} />}

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
  return <li className="cv-pod">
    <div className="cv-pod-main">
      <span className="cv-mono cv-pod-name" title={`${p.namespace}/${p.name}${p.node ? ` · Node: ${p.node}` : ""}`}>{p.name}</span>
      <div className="cv-pod-meta">
        <span className={`cv-pod-state${p.ready ? " is-ready" : p.stuck ? " is-stuck" : ""}`}><span aria-hidden="true" />{p.reason ?? (p.ready ? "Running" : p.phase)}</span>
        <span>{p.readyContainers} ready</span>
        <span className={p.restarts > 0 ? "cv-warn-text" : undefined}>{p.restarts} restart{p.restarts === 1 ? "" : "s"}</span>
        {p.startedAt && <span>{age(p.startedAt)} old</span>}
      </div>
    </div>
    <div className="cv-pod-actions">
      <button className="cv-btn is-secondary is-sm" onClick={onLogs} aria-label={`Logs for ${p.name}`}>Logs</button>
      {action}
    </div>
  </li>;
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

