import type { ElkNode } from "elkjs/lib/elk.bundled.js";
import { INTERNET, type MapGraph } from "./graph";

export const CARD_W = 264;
export const CARD_H = 92;
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
export async function layout(g: MapGraph): Promise<Layout> {
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
