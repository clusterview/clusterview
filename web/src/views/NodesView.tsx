import { useMemo } from "react";
import { AlertTriangle, Server } from "lucide-react";
import { getNodes, type ClusterNode, type NodePod, type Snapshot } from "../api";
import { useLive } from "../useLive";
import { cpu, memory, percent } from "../format";
import "./views.css";

interface Props {
  snapshot: Snapshot;
  onSelect: (serviceId: string) => void;
}

/** The machines, how full they are, and what runs on each. */
export function NodesView({ snapshot, onSelect }: Props) {
  const { data, error } = useLive(getNodes);

  // A pod's square opens the service it belongs to.
  const serviceOf = useMemo(() => {
    const m = new Map<string, string>();
    for (const s of snapshot.services) for (const w of s.workloads) m.set(w, s.id);
    return m;
  }, [snapshot]);
  const open = (p: NodePod) => {
    const id = p.workloadId && serviceOf.get(p.workloadId);
    if (id) onSelect(id);
  };

  if (error && !data) {
    return (
      <div className="cv-empty">
        <p className="cv-empty-title">The nodes view isn't available</p>
        <p className="cv-empty-detail">{error}</p>
      </div>
    );
  }
  if (!data) return <div className="cv-empty"><p className="cv-empty-title">Reading the nodes…</p></div>;

  const notReady = data.nodes.filter((n) => !n.ready).length;

  return (
    <div className="cv-view">
      <div className="cv-view-inner is-wide">
        <div className="cv-view-bar">
          <span className="cv-view-title">
            <Server size={16} /> {data.nodes.length} node{data.nodes.length === 1 ? "" : "s"}
            {notReady > 0 && <span className="cv-pill is-down">{notReady} not ready</span>}
          </span>
          {!data.metrics && (
            <span className="cv-muted">metrics-server isn't installed, so only reserved figures are shown, not live use.</span>
          )}
        </div>

        {data.unscheduled.length > 0 && (
          <section className="cv-waiting">
            <h2>
              <AlertTriangle size={16} /> {data.unscheduled.length} pod{data.unscheduled.length === 1 ? " is" : "s are"} waiting for a node
            </h2>
            <ul>
              {data.unscheduled.map((p) => (
                <li key={`${p.namespace}/${p.name}`}>
                  <button className="cv-link" onClick={() => open(p)}>
                    {p.workload}
                  </button>
                  <span className="cv-muted cv-mono">
                    {p.namespace}/{p.name}
                  </span>
                  <span className="cv-muted">{p.reason ?? "Pending"}</span>
                </li>
              ))}
            </ul>
          </section>
        )}

        <div className="cv-nodes">
          {data.nodes.map((n) => (
            <NodeCard key={n.name} node={n} onPod={open} />
          ))}
        </div>
      </div>
    </div>
  );
}

function NodeCard({ node: n, onPod }: { node: ClusterNode; onPod: (p: NodePod) => void }) {
  const trouble = !n.ready || n.pressure.length > 0;
  const tone = !n.ready ? "down" : n.pressure.length ? "degraded" : "healthy";
  return (
    <article className={`cv-node${trouble ? " is-trouble" : ""}`}>
      <header className="cv-node-head">
        <span className={`cv-status is-${tone}`} />
        <h3 className="cv-mono">{n.name}</h3>
      </header>
      <div className="cv-node-tags">
        {!n.ready && <span className="cv-pill is-down">Not ready</span>}
        {n.pressure.map((p) => (
          <span key={p} className="cv-pill is-degraded">
            {pressureWords[p] ?? p}
          </span>
        ))}
        {n.unschedulable && <span className="cv-pill is-idle">Cordoned</span>}
        {[n.instanceType, n.zone, n.pool].filter(Boolean).map((t) => (
          <span key={t} className="cv-tag">
            {t}
          </span>
        ))}
      </div>

      <Meter label="CPU" reserved={n.cpuRequestedMillicores} used={n.cpuUsageMillicores} total={n.cpuAllocatableMillicores} fmt={cpu} />
      <Meter label="Memory" reserved={n.memoryRequestedBytes} used={n.memoryUsageBytes} total={n.memoryAllocatableBytes} fmt={memory} />

      <div className="cv-node-pods">
        <div className="cv-node-pods-head">
          <span>Pods</span>
          <span className="cv-muted">
            {n.pods.length} of {n.podsAllocatable}
          </span>
        </div>
        <div className="cv-squares">
          {n.pods.map((p) => (
            <button
              key={`${p.namespace}/${p.name}`}
              className={`cv-square ${p.terminating ? "is-leaving" : p.ready ? "is-ready" : p.reason && p.reason !== "Completed" ? "is-stuck" : "is-starting"}`}
              title={`${p.workload} · ${p.namespace}/${p.name}${p.reason ? ` · ${p.reason}` : ""}`}
              onClick={() => onPod(p)}
              aria-label={`${p.workload}, ${p.name}`}
            />
          ))}
        </div>
      </div>
    </article>
  );
}

const pressureWords: Record<string, string> = {
  MemoryPressure: "Low on memory",
  DiskPressure: "Low on disk",
  PIDPressure: "Too many processes",
  NetworkUnavailable: "No network",
};

function Meter(props: { label: string; reserved: number; used?: number; total: number; fmt: (n?: number) => string }) {
  const { label, reserved, used, total, fmt } = props;
  const r = percent(reserved, total);
  const u = used !== undefined ? percent(used, total) : undefined;
  const hot = Math.max(r, u ?? 0) >= 90;
  return (
    <div className={`cv-meter${hot ? " is-hot" : ""}`}>
      <div className="cv-meter-label">
        <span>{label}</span>
        <span className="cv-muted">
          {fmt(reserved)} reserved{used !== undefined && <> · {fmt(used)} used</>} of {fmt(total)}
        </span>
      </div>
      <div className="cv-meter-bar" role="img" aria-label={`${label}: ${r}% reserved${u !== undefined ? `, ${u}% used` : ""}`}>
        <span className="cv-meter-reserved" style={{ width: `${r}%` }} />
        {u !== undefined && <span className="cv-meter-used" style={{ width: `${u}%` }} />}
      </div>
    </div>
  );
}
