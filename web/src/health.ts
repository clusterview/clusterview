import type { ResourceReading, Service, Status } from "./api";

export type Tone = Status | "warning" | "critical";

/** Availability remains separate: a ready pod can still have critical health. */
export function toneOf(s: Service): Tone {
  if (s.status === "down" || s.status === "degraded") return s.status;
  if (s.problems.some((p) => p.severity === "critical")) return "critical";
  if (s.problems.some((p) => p.severity === "warning")) return "warning";
  if (s.status === "idle" || s.status === "unknown") return s.status;
  return s.health?.state ?? "healthy";
}

export function needsAttention(s: Service): boolean {
  const tone = toneOf(s);
  return tone === "down" || tone === "degraded" || tone === "warning" || tone === "critical";
}

export function healthReason(s: Service): string | undefined {
  if (s.problems[0]) return s.problems[0].title;
  if (!s.health) return undefined;
  const missingQueue = s.health.queues?.find((q) => q.state === "unknown");
  if (missingQueue) return `${missingQueue.queue}: ${missingQueue.reason}`;
  const missing = [s.health.cpu, s.health.memory, s.health.disk].find((r) => r?.state === "unknown");
  return missing?.reason;
}

export function meterWidth(r?: ResourceReading): number {
  return r?.percent !== undefined && Number.isFinite(r.percent) ? Math.max(0, Math.min(100, r.percent)) : 0;
}
