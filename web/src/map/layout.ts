import type { ElkNode } from "elkjs/lib/elk.bundled.js";
import { INTERNET, type MapGraph } from "./graph";

export const CARD_W = 288;
export const CARD_H = 112;
export const INTERNET_W = 132;
export const INTERNET_H = 44;
const GROUP_PAD = "[top=44,left=20,bottom=20,right=20]";

// ELK is most of the app's code; it loads in its own chunk, after the shell.
let elk: Promise<{ layout: (g: ElkNode) => Promise<ElkNode> }> | null = null;
const getElk = () => (elk ??= import("elkjs/lib/elk.bundled.js").then((m) => new m.default()));

export interface Placed {
  id: string;
  x: number;
  y: number;
  w: number;
  h: number;
  parent?: string;
}

export interface Route {
  id: string;
  points: { x: number; y: number }[];
}

export interface Layout {
  nodes: Placed[];
  /** Orthogonal edge routes, in map coordinates. */
  routes: Map<string, Route>;
}

export const groupId = (ns: string) => `ns:${ns}`;

/** A focused map has three predictable columns: callers, selection, dependencies.
 * Namespace names live on these cards, so unrelated group boxes need no space. */
function connectedLayout(graph: MapGraph, selectedId: string): Layout {
  const width = 224;
  const gap = 48;
  const step = CARD_H + 24;
  const incoming = graph.services.filter((service) => service.id !== selectedId && graph.edges.some((edge) => edge.from === service.id && edge.to === selectedId));
  const outgoing = graph.services.filter((service) => service.id !== selectedId && !incoming.includes(service));
  const entry = graph.entry.some((route) => route.id === selectedId);
  const leftCount = incoming.length + Number(entry);
  const height = Math.max(leftCount, outgoing.length, 1) * step - 24;
  const nodes: Placed[] = [];
  const column = (ids: string[], x: number) => ids.forEach((id, index) => {
    const internet = id === INTERNET;
    nodes.push({ id, x: x + (internet ? (width - INTERNET_W) / 2 : 0), y: (height - (ids.length * step - 24)) / 2 + index * step,
      w: internet ? INTERNET_W : width, h: internet ? INTERNET_H : CARD_H });
  });
  column([...incoming.map((service) => service.id), ...(entry ? [INTERNET] : [])], 24);
  column([selectedId], 24 + width + gap);
  column(outgoing.map((service) => service.id), 24 + 2 * (width + gap));
  const byId = new Map(nodes.map((node) => [node.id, node]));
  const routes = new Map<string, Route>();
  const edges = [...graph.edges, ...graph.entry.map((route) => ({ id: `${INTERNET}->${route.id}`, from: INTERNET, to: route.id }))];
  edges.forEach((edge, index) => {
    const from = byId.get(edge.from);
    const to = byId.get(edge.to);
    if (!from || !to) return;
    const start = { x: from.x + from.w, y: from.y + from.h / 2 };
    const end = { x: to.x, y: to.y + to.h / 2 };
    let points;
    if (end.x > start.x) {
      const middle = (start.x + end.x) / 2;
      points = [start, { x: middle, y: start.y }, { x: middle, y: end.y }, end];
    } else {
      // Bidirectional links and self-calls return outside the card columns.
      const bottom = height + 24 + index * 10;
      points = [start, { x: start.x + 16, y: start.y }, { x: start.x + 16, y: bottom }, { x: end.x - 16, y: bottom }, { x: end.x - 16, y: end.y }, end];
    }
    routes.set(edge.id, { id: edge.id, points });
  });
  return { nodes, routes };
}

/** Services with no lines would stack in one tall column. Chaining them a
 *  few to a row with invisible layout-only edges lays them out as a grid. */
function gridEdges(g: MapGraph) {
  const linked = new Set<string>();
  for (const e of g.edges) linked.add(e.from).add(e.to);
  for (const e of g.entry) linked.add(e.id);
  const out: { id: string; sources: string[]; targets: string[] }[] = [];
  for (const ns of g.namespaces) {
    const alone = g.services.filter((s) => s.namespace === ns && !linked.has(s.id));
    const perRow = Math.min(4, Math.ceil(Math.sqrt(alone.length)));
    alone.forEach((s, i) => {
      if (i % perRow !== 0) out.push({ id: `layout:${i}:${s.id}`, sources: [alone[i - 1].id], targets: [s.id] });
    });
  }
  return out;
}

/** Lays the map out left to right: traffic flows from the Internet through
 *  apps to their databases and queues. Namespaces are boxes around their
 *  services. Positions of children are relative to their group. */
export async function layout(g: MapGraph, selectedId?: string | null): Promise<Layout> {
  if (selectedId) return connectedLayout(g, selectedId);
  const root: ElkNode = {
    id: "root",
    layoutOptions: {
      "elk.algorithm": "layered",
      "elk.direction": "RIGHT",
      "elk.hierarchyHandling": "INCLUDE_CHILDREN",
      "elk.layered.spacing.nodeNodeBetweenLayers": "96",
      "elk.spacing.nodeNode": "24",
      "elk.spacing.componentComponent": "48",
      "elk.layered.nodePlacement.strategy": "NETWORK_SIMPLEX",
      "elk.layered.crossingMinimization.strategy": "LAYER_SWEEP",
      "elk.edgeRouting": "ORTHOGONAL",
      "elk.layered.spacing.edgeNodeBetweenLayers": "28",
      "elk.layered.spacing.edgeEdgeBetweenLayers": "12",
      // Every coordinate relative to the map, edges included.
      "elk.json.edgeCoords": "ROOT",
    },
    children: g.namespaces.map((ns) => ({
      id: groupId(ns),
      layoutOptions: { "elk.padding": GROUP_PAD, "elk.spacing.nodeNode": "20" },
      children: g.services.filter((s) => s.namespace === ns).map((s) => ({ id: s.id, width: CARD_W, height: CARD_H })),
    })),
    edges: [...g.edges.map((e) => ({ id: e.id, sources: [e.from], targets: [e.to] })), ...gridEdges(g)],
  };
  if (g.entry.length) {
    root.children!.unshift({ id: INTERNET, width: INTERNET_W, height: INTERNET_H });
    for (const e of g.entry) root.edges!.push({ id: `${INTERNET}->${e.id}`, sources: [INTERNET], targets: [e.id] });
  }

  const out = await (await getElk()).layout(root);
  const nodes: Placed[] = [];
  const routes = new Map<string, Route>();
  const collect = (n: ElkNode) => {
    for (const e of n.edges ?? []) {
      const sec = e.sections?.[0];
      if (sec) routes.set(e.id, { id: e.id, points: [sec.startPoint, ...(sec.bendPoints ?? []), sec.endPoint] });
    }
  };
  collect(out);
  for (const n of out.children ?? []) {
    nodes.push({ id: n.id, x: n.x ?? 0, y: n.y ?? 0, w: n.width ?? 0, h: n.height ?? 0 });
    collect(n);
    for (const c of n.children ?? []) {
      nodes.push({ id: c.id, x: c.x ?? 0, y: c.y ?? 0, w: c.width ?? 0, h: c.height ?? 0, parent: n.id });
    }
  }
  return { nodes, routes };
}
