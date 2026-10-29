import assert from "node:assert/strict";
import test from "node:test";
import { buildGraph, connectedGraph, shapeKey } from "../../web/src/map/graph.ts";
import type { Service, Snapshot } from "../../web/src/api.ts";

function service(id: string, namespace = "shop", category: Service["category"] = "app"): Service {
  return { id, namespace, category, name: id, title: id, status: "healthy", desired: 1, ready: 1, workloads: [], problems: [], addresses: [], entrypoints: [] };
}
function snapshot(services: Service[], pairs: [string, string][]): Snapshot {
  return { services, links: pairs.map(([from, to]) => ({ from, to, via: to, evidence: "env API_URL" })), workloads: [], version: 1, fetchedAt: "", summary: { services: services.length, workloads: 0, needAttention: 0, pods: 0, readyPods: 0, restarts: 0, namespaces: 1 } };
}

test("connection focus includes both directions and excludes unrelated and transitive routes", () => {
  const services = [service("selected"), service("caller"), service("database", "data", "database"), service("indirect"), service("unrelated")];
  services[1].entrypoints = [{ host: "example.test", ingress: "shop/web", tls: true }];
  const graph = buildGraph(snapshot(services, [["caller", "selected"], ["selected", "database"], ["database", "indirect"], ["caller", "database"]]), true);
  const focused = connectedGraph(graph, "selected");
  assert.deepEqual(focused.services.map((item) => item.id), ["selected", "caller", "database"]);
  assert.deepEqual(focused.edges.map((edge) => edge.id), ["caller->selected", "selected->database"]);
  assert.equal(focused.entry.length, 0, "an upstream service's ingress is not a direct connection of the selection");
  assert.equal(focused.hiddenCount, 2);
  assert.equal(graph.services.length, 5, "focusing must not mutate the full graph");
});

test("focus retains an ingress directly attached to the selected service", () => {
  const selected = service("selected");
  selected.entrypoints = [{ host: "example.test", path: "/api", ingress: "shop/web", tls: true }];
  const focused = connectedGraph(buildGraph(snapshot([selected], []), true), "selected");
  assert.deepEqual(focused.entry, [{ id: "selected", label: "example.test/api" }]);
});

test("search selection reveals a hidden cluster service without revealing other healthy system services", () => {
  const graph = buildGraph(snapshot([service("web"), service("metrics", "monitoring", "monitoring"), service("traces", "monitoring", "monitoring")], []), false, "metrics");
  assert.deepEqual(graph.services.map((item) => item.id), ["web", "metrics"]);
  assert.equal(graph.hiddenCount, 1);
});

test("health and label changes preserve the layout key", () => {
  const state = snapshot([service("web"), service("database")], [["web", "database"]]);
  const before = shapeKey(buildGraph(state, true));
  state.services[0].status = "down";
  state.services[0].title = "Longer updated name";
  assert.equal(shapeKey(buildGraph(state, true)), before);
});
