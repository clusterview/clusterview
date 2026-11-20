import type { PodState, Service } from "../api";
import { ResourceHistory, ResourceMeter } from "../components/ResourceHealth";
import { age } from "../format";

const number = (n?: number) => n === undefined ? "No data" : n.toLocaleString(undefined, { maximumFractionDigits: 1 });

export function HealthDetails({ service, pods }: { service: Service; pods: PodState[] }) {
  const h = service.health;
  if (!h) return null;
  return <section className="cv-section">
    <h3>Resources</h3>
    <p className="cv-note">Most constrained container or volume; percentages use limits and measured capacity.</p>
    <div className="cv-health-details">
      {([['CPU', h.cpu], ['Memory', h.memory], ['Disk', h.disk]] as const).map(([label, r]) => <div key={label}>
        <ResourceMeter label={label} reading={r} />
        {r?.subject && <p className="cv-health-subject cv-mono">{r.subject}</p>}
        {r && <ResourceHistory label={label} reading={r} />}
      </div>)}
    </div>
    {pods.filter((p) => p.resources?.length).map((p) => <details className="cv-container-health" key={`${p.namespace}/${p.name}`}>
      <summary>{p.name} · container measurements</summary>
      {p.resources!.map((c) => <div key={c.name}><p className="cv-note cv-mono">{c.name}</p><ResourceMeter label="CPU" reading={c.cpu} /><ResourceMeter label="Memory" reading={c.memory} />{c.throttling && <ResourceMeter label="Throttled CPU periods" reading={c.throttling} />}</div>)}
    </details>)}
    {service.category === "queue" && !h.queues?.length && <p className="cv-note">Queue flow is not monitored. Configure a RabbitMQ queue in monitoring to inspect message processing.</p>}
    {!!h.queues?.length && <><h3>RabbitMQ queues</h3>{h.queues.map((q) => <div className={`cv-queue-health is-${q.state}`} key={`${q.job}/${q.vhost}/${q.queue}`}>
      <p className="cv-queue-title">{q.queue}</p><p className="cv-note">{q.vhost} · {q.state}</p><p>{q.reason}</p>
      <dl className="cv-queue-stats">
        <div><dt>Waiting</dt><dd>{number(q.ready)}</dd></div><div><dt>Unacknowledged</dt><dd>{number(q.unacked)}</dd></div>
        <div><dt>Incoming / second</dt><dd>{number(q.publishRate)}</dd></div><div><dt>{q.ackMode === "auto" ? "Auto-delivered" : "Acknowledged"} / second</dt><dd>{number(q.processRate)}</dd></div>
        <div><dt>Consumers</dt><dd>{number(q.consumers)}</dd></div><div><dt>Last observation</dt><dd>{q.observedAt ? `${age(q.observedAt)} ago` : "No data"}</dd></div>
      </dl><p className="cv-note">Growing/stalled backlog: warn at {number(q.warningMessages)}, critical at {number(q.criticalMessages)}. Prometheus job: {q.job}.</p>
    </div>)}</>}
  </section>;
}
