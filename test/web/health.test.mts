import assert from "node:assert/strict";
import test from "node:test";
import type { ResourceReading, Service, ServiceHealth } from "../../web/src/api.ts";
import { healthReason, meterWidth, needsAttention, toneOf } from "../../web/src/health.ts";
import { buildGraph } from "../../web/src/map/graph.ts";

const resource: ResourceReading = { state: "healthy", usage: 20, limit: 100, percent: 20, source: "metrics-server", unit: "bytes", reason: "Within thresholds" };
const health: ServiceHealth = { state: "healthy", cpu: resource, memory: resource, checks: {} };
function service(): Service { return { id: "Pod/monitoring/metrics", namespace: "monitoring", name: "metrics", title: "Metrics", category: "monitoring", status: "healthy", ready: 1, desired: 1, workloads: [], problems: [], addresses: [], entrypoints: [], health }; }

test("ready services show critical resources and enter the attention list", () => {
  const s = service();
  s.health = { ...health, state: "critical" };
  s.problems = [{ kind: "resource-memory", severity: "critical", title: "Memory almost full" }];
  assert.equal(toneOf(s), "critical");
  assert.equal(s.status, "healthy", "availability stays separate");
  assert.equal(needsAttention(s), true);
  const graph = buildGraph({ services: [s], links: [], workloads: [], version: 1, fetchedAt: "", summary: {} } as any, false);
  assert.equal(graph.services.length, 1, "resource failures reveal hidden cluster services");
});

test("unknown resources don't appear healthy or suppress availability failures", () => {
  const s = service();
  s.health = { ...health, state: "unknown", memory: { ...resource, state: "unknown", percent: undefined, reason: "Memory metrics stale" } };
  assert.equal(toneOf(s), "unknown");
  assert.equal(healthReason(s), "Memory metrics stale");
  s.status = "down";
  assert.equal(toneOf(s), "down");
  s.status = "idle";
  assert.equal(toneOf(s), "idle");
});

test("warnings colour cards without falsely calling pods down", () => {
  const s = service();
  s.problems = [{ kind: "resource-disk", severity: "warning", title: "Disk filling up" }];
  assert.equal(toneOf(s), "warning");
  assert.equal(healthReason(s), "Disk filling up");
  assert.equal(needsAttention(s), true);
});

test("meters clamp geometry while retaining over-limit values in the reading", () => {
  assert.equal(meterWidth({ ...resource, percent: 120 }), 100);
  assert.equal(meterWidth({ ...resource, percent: -1 }), 0);
  assert.equal(meterWidth({ ...resource, percent: Number.NaN }), 0);
  assert.equal(meterWidth(undefined), 0);
});
