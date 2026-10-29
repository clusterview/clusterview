import type { PodState, Service, Workload } from "../api";
import { describeSchedule } from "../format-cron";
import { CategoryIcon } from "./CategoryIcon";

export type Tone = "down" | "degraded" | "warning" | "healthy" | "idle" | "unknown";

/** The one word that drives a service's look. */
export function toneOf(s: Service): Tone {
  if (s.status === "down" || s.status === "degraded") return s.status;
  if (s.problems.some((p) => p.severity === "warning")) return "warning";
  if (s.status === "idle" || s.status === "unknown") return s.status;
  return "healthy";
}

const MAX_PIPS = 12;

/** One small square per pod: green ready, red stuck, amber starting. */
export function PodPips({ pods, desired }: { pods: PodState[]; desired: number }) {
  const shown = pods.slice(0, MAX_PIPS);
  const missing = Math.max(0, Math.min(desired, MAX_PIPS) - shown.length);
  return (
    <span className="cv-pips" aria-hidden>
      {shown.map((p) => (
        <span key={p.name} className={`cv-pip ${p.ready ? "is-ready" : p.stuck ? "is-stuck" : "is-starting"}`} />
      ))}
      {Array.from({ length: missing }, (_, i) => (
        <span key={`m${i}`} className="cv-pip is-missing" />
      ))}
      {pods.length > MAX_PIPS && <span className="cv-pips-more">+{pods.length - MAX_PIPS}</span>}
    </span>
  );
}

interface Props {
  service: Service;
  workloads: Workload[];
  selected?: boolean;
  showNamespace?: boolean;
  dimmed?: boolean;
  /** Set when a service this one calls is broken. */
  dependsOnBroken?: string;
}

export function ServiceCard({ service: s, workloads, selected, showNamespace, dimmed, dependsOnBroken }: Props) {
  const tone = toneOf(s);
  const pods = workloads.filter((w) => w.kind !== "Job" && w.kind !== "CronJob").flatMap((w) => w.pods);
  const problem = s.problems[0];
  const showName = s.title !== s.name;
  const schedule = workloads.find((w) => w.schedule);

  let line: React.ReactNode;
  if (problem && (tone === "down" || tone === "degraded" || tone === "warning")) {
    line = <span className={`cv-card-problem is-${problem.severity}`}>{problem.title}</span>;
  } else if (dependsOnBroken) {
    line = <span className="cv-card-problem is-warning">{dependsOnBroken} is down</span>;
  } else if (schedule?.schedule) {
    line = <span className="cv-card-desc">{describeSchedule(schedule.schedule)}</span>;
  } else {
    line = <span className="cv-card-desc">{s.description ?? ""}</span>;
  }

  return (
    <article
      className={`cv-card is-${tone}${selected ? " is-selected" : ""}${dimmed ? " is-dimmed" : ""}`}
      aria-label={`${s.title}: ${s.status}${problem ? `, ${problem.title}` : ""}`}
    >
      {tone === "down" && <span className="cv-card-alarm" aria-hidden />}
      <header className="cv-card-head">
        <CategoryIcon category={s.category} />
        <div className="cv-card-titles">
          <h3 className="cv-card-title" title={s.title}>{s.title}</h3>
          <p className="cv-card-sub" title={`${s.namespace}/${s.name} · ${s.managedBy?.kind ?? workloads[0]?.kind ?? ""}`}>
            {showNamespace && <><span>{s.namespace}</span><span className="cv-dot-sep" /></>}
            {showName && <span className="cv-mono">{s.name}</span>}
            {showName && <span className="cv-dot-sep" />}
            <span>{s.managedBy ? s.managedBy.kind : (workloads[0]?.kind ?? "")}</span>
          </p>
        </div>
        <span className={`cv-status is-${tone}`} aria-hidden />
      </header>
      <footer className="cv-card-foot" title={problem?.detail ?? dependsOnBroken ?? s.description}>
        {s.desired > 0 && !schedule && (
          <span className="cv-replicas">
            <PodPips pods={pods} desired={s.desired} />
            <span className="cv-replicas-count">
              {s.ready}/{s.desired}
            </span>
          </span>
        )}
        {line}
      </footer>
    </article>
  );
}
