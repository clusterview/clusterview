import { useMemo, useState } from "react";
import type { Service, Snapshot, Workload } from "../api";
import { CategoryIcon, categoryLabel } from "../components/CategoryIcon";
import { PodPips, toneOf } from "../components/ServiceCard";
import { describeSchedule } from "../format-cron";
import { isSystemNamespace, needsAttention } from "../map/graph";
import "./views.css";

interface Props {
  snapshot: Snapshot;
  showSystem: boolean;
  onShowSystem: (on: boolean) => void;
  selectedId: string | null;
  onSelect: (id: string) => void;
}

/** Every service as a row: what needs attention first, then by namespace. */
export function ListView({ snapshot, showSystem, onShowSystem, selectedId, onSelect }: Props) {
  const [filter, setFilter] = useState("");
  const workloads = useMemo(() => new Map(snapshot.workloads.map((w) => [w.id, w])), [snapshot]);

  const { trouble, groups } = useMemo(() => {
    const q = filter.trim().toLowerCase();
    const system = new Set(snapshot.services.map((s) => s.namespace).filter((ns) => isSystemNamespace(ns, snapshot.services)));
    const match = (s: Service) =>
      !q || [s.title, s.name, s.namespace, s.category, s.description ?? ""].some((x) => x.toLowerCase().includes(q));
    const shown = snapshot.services.filter((s) => match(s) && (showSystem || !system.has(s.namespace) || needsAttention(s)));
    const trouble = shown.filter(needsAttention);
    const rest = shown.filter((s) => !needsAttention(s));
    const groups = new Map<string, Service[]>();
    for (const s of rest) groups.set(s.namespace, [...(groups.get(s.namespace) ?? []), s]);
    return { trouble, groups: [...groups.entries()].sort(([a], [b]) => a.localeCompare(b)) };
  }, [snapshot, filter, showSystem]);

  const row = (s: Service) => (
    <ServiceRow
      key={s.id}
      service={s}
      workloads={s.workloads.map((id) => workloads.get(id)).filter((w): w is Workload => !!w)}
      selected={s.id === selectedId}
      onSelect={() => onSelect(s.id)}
    />
  );

  return (
    <div className="cv-view">
      <div className="cv-view-inner">
        <div className="cv-view-bar">
          <input
            className="cv-input"
            placeholder="Filter services"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            aria-label="Filter services"
          />
          <label className="cv-check">
            <input type="checkbox" checked={showSystem} onChange={(e) => onShowSystem(e.target.checked)} />
            Show cluster services
          </label>
          <span className="cv-muted cv-view-bar-end">
            {snapshot.summary.services} services · {snapshot.summary.readyPods}/{snapshot.summary.pods} pods ready ·{" "}
            {snapshot.summary.namespaces} namespaces
          </span>
        </div>

        {trouble.length > 0 && (
          <section className="cv-list-group is-trouble">
            <h2>Needs attention</h2>
            <ul className="cv-rows">{trouble.map(row)}</ul>
          </section>
        )}
        {groups.map(([ns, services]) => (
          <section key={ns} className="cv-list-group">
            <h2>
              {ns} <span className="cv-count">{services.length}</span>
            </h2>
            <ul className="cv-rows">{services.map(row)}</ul>
          </section>
        ))}
        {!trouble.length && !groups.length && <p className="cv-empty-note">No services match “{filter}”.</p>}
      </div>
    </div>
  );
}

function ServiceRow({
  service: s,
  workloads,
  selected,
  onSelect,
}: {
  service: Service;
  workloads: Workload[];
  selected: boolean;
  onSelect: () => void;
}) {
  const tone = toneOf(s);
  const problem = s.problems[0];
  const schedule = workloads.find((w) => w.schedule)?.schedule;
  const pods = workloads.filter((w) => w.kind !== "Job" && w.kind !== "CronJob").flatMap((w) => w.pods);
  return (
    <li>
      <button className={`cv-row is-${tone}${selected ? " is-selected" : ""}`} onClick={onSelect}>
        <span className={`cv-status is-${tone}`} />
        <CategoryIcon category={s.category} size={30} />
        <span className="cv-row-name">
          <strong>{s.title}</strong>
          <span className="cv-muted cv-mono">
            {s.title !== s.name ? `${s.name} · ` : ""}
            {s.namespace}
          </span>
        </span>
        <span className="cv-row-kind cv-muted">{s.managedBy?.kind ?? workloads[0]?.kind ?? categoryLabel[s.category]}</span>
        <span className="cv-row-replicas">
          {schedule ? (
            <span className="cv-muted">{describeSchedule(schedule)}</span>
          ) : (
            s.desired > 0 && (
              <>
                <PodPips pods={pods} desired={s.desired} />
                <span className="cv-replicas-count">
                  {s.ready}/{s.desired}
                </span>
              </>
            )
          )}
        </span>
        <span className="cv-row-line">
          {problem ? (
            <span className={`cv-card-problem is-${problem.severity}`}>
              {problem.title}
              {problem.detail && <span className="cv-row-detail"> · {problem.detail}</span>}
            </span>
          ) : (
            <span className="cv-card-desc">{s.description}</span>
          )}
        </span>
      </button>
    </li>
  );
}
