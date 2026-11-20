import type { ResourceReading, ServiceHealth } from "../api";
import { age, cpu, memory } from "../format";
import { meterWidth } from "../health";
import "./resource-health.css";

export function resourceValue(r?: ResourceReading): string {
  if (!r || r.usage === undefined) return "No data";
  const format = r.unit === "millicores" ? cpu : r.unit === "bytes" ? memory : (n?: number) => `${n?.toFixed(1)}%`;
  return `${format(r.usage)}${r.limit !== undefined && r.unit !== "percent" ? ` / ${format(r.limit)}` : ""}`;
}

export function ResourceMeter({ label, reading: r, compact = false }: { label: string; reading?: ResourceReading; compact?: boolean }) {
  const value = r?.percent === undefined ? "—" : `${Math.round(r.percent)}%`;
  const description = r ? `${label}: ${resourceValue(r)}. ${r.reason}${r.subject ? `. ${r.subject}` : ""}` : `${label}: no persistent volume`;
  return <span className={`cv-resource-meter is-${r?.state ?? "unknown"}${compact ? " is-compact" : ""}`} title={description}>
    <span className="cv-resource-label"><span>{label}</span><span className="cv-resource-value">{compact ? value : resourceValue(r)}</span></span>
    <span className="cv-resource-track" role="meter" aria-label={label} aria-valuemin={0} aria-valuemax={100}
      aria-valuenow={meterWidth(r)} aria-valuetext={description}>
      {r?.percent !== undefined && <span style={{ width: `${meterWidth(r)}%` }} />}
    </span>
    {!compact && <span className="cv-resource-meta"><span>{r?.reason ?? "No persistent volume"}</span>{r?.observedAt && <span>{r.source} · {age(r.observedAt)} ago</span>}</span>}
  </span>;
}

/** Fixed card geometry keeps resource updates from rearranging the map. */
export function ResourceBars({ health }: { health: ServiceHealth }) {
  return <span className="cv-resource-bars">
    <ResourceMeter compact label="CPU" reading={health.cpu} />
    <ResourceMeter compact label="Memory" reading={health.memory} />
    <ResourceMeter compact label="Disk" reading={health.disk} />
  </span>;
}

export function ResourceHistory({ label, reading }: { label: string; reading: ResourceReading }) {
  const points = reading.history ?? [];
  if (points.length < 2) return null;
  const minTime = Date.parse(points[0].time);
  const span = Math.max(1, Date.parse(points[points.length - 1].time) - minTime);
  const ceiling = Math.max(100, ...points.map((p) => p.value));
  const coordinates = points.map((p) => `${((Date.parse(p.time) - minTime) / span) * 240},${44 - (p.value / ceiling) * 40}`).join(" ");
  return <div className={`cv-resource-history is-${reading.state}`}>
    <svg viewBox="0 0 240 48" role="img" aria-label={`${label} history: ${points[0].value.toFixed(1)}% to ${points[points.length - 1].value.toFixed(1)}%, from ${new Date(minTime).toLocaleTimeString()} to ${new Date(points[points.length - 1].time).toLocaleTimeString()}`}>
      <polyline points={coordinates} fill="none" stroke="currentColor" strokeWidth="2" vectorEffect="non-scaling-stroke" />
    </svg>
    <span className="cv-note">{age(points[0].time)} ago → latest · % of limit/capacity</span>
  </div>;
}
