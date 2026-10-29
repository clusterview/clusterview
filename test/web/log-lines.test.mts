import assert from "node:assert/strict";
import test from "node:test";
import { MAX_LOG_CHARS, MAX_LOG_LINES, mergeLogLines } from "../../web/src/drawer/log-lines.ts";

test("merges concurrent sources in timestamp order without dropping identical messages", () => {
  const later = { timestamp: "2026-10-29T12:00:02.000000000Z", text: "same", pod: "api-b", container: "app" };
  const earlier = { timestamp: "2026-10-29T12:00:01.000000000Z", text: "same", pod: "api-a", container: "sidecar" };
  const lines = mergeLogLines([later], [earlier, earlier]);
  assert.deepEqual(lines, [earlier, earlier, later]);
});

test("retains the newest 5000 lines", () => {
  const lines = Array.from({ length: MAX_LOG_LINES + 10 }, (_, i) => ({ text: String(i) }));
  const result = mergeLogLines([], lines);
  assert.equal(result.length, MAX_LOG_LINES);
  assert.equal(result[0].text, "10");
  assert.equal(result.at(-1)?.text, String(MAX_LOG_LINES + 9));
});

test("bounds large messages by retained text, not just line count", () => {
  const text = "x".repeat(MAX_LOG_CHARS / 2);
  const result = mergeLogLines([{ text, pod: "old" }], [{ text, pod: "new" }]);
  assert.equal(result.length, 1);
  assert.equal(result[0].pod, "new");
});

test("keeps a single long line and preserves source labels", () => {
  const line = { text: "x".repeat(MAX_LOG_CHARS + 1), namespace: "shop", pod: "api-1", container: "app" };
  assert.deepEqual(mergeLogLines([], [line]), [line]);
});

test("copied logs preserve complete source names and message line breaks", async () => {
  const { formatLogLines } = await import("../../web/src/drawer/log-lines.ts");
  const line = { timestamp: "2026-10-29T12:00:01Z", namespace: "shop", pod: "api-very-long-replica-name", container: "worker", text: "failed\n  trace" };
  assert.equal(formatLogLines([line]), "2026-10-29T12:00:01Z [shop/api-very-long-replica-name/worker] failed\n  trace");
  assert.equal(formatLogLines([{ text: "plain message" }]), "plain message");
  assert.equal(formatLogLines([]), "");
});
