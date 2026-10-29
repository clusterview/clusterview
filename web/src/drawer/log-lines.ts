import type { LogLine } from "../api";

export const MAX_LOG_LINES = 5000;
export const MAX_LOG_CHARS = 1 << 20;

/** Bound retained text as well as line count: one very long line is still a log. */
export function recentLines(lines: LogLine[]): LogLine[] {
  let chars = 0;
  let start = lines.length;
  while (start > 0 && lines.length - start < MAX_LOG_LINES) {
    const line = lines[start - 1];
    const size = line.text.length + (line.pod?.length ?? 0) + (line.container?.length ?? 0);
    if (chars + size > MAX_LOG_CHARS && start < lines.length) break;
    chars += size;
    start--;
  }
  return lines.slice(start);
}

/** Kubernetes timestamps are normalised to UTC, so lexical order is time order.
 * Stable sorting preserves repeated messages, including equal timestamps. */
export function mergeLogLines(previous: LogLine[], next: LogLine[]): LogLine[] {
  return recentLines([...previous, ...next].sort((a, b) => (a.timestamp ?? "").localeCompare(b.timestamp ?? "")));
}
