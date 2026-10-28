import { useEffect, useMemo, useState } from "react";
import {
  Background,
  BackgroundVariant,
  BaseEdge,
  Controls,
  EdgeLabelRenderer,
  Handle,
  Position,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
  type Edge,
  type EdgeProps,
  type Node,
  type NodeProps,
} from "@xyflow/react";
import "@xyflow/react/dist/base.css";
import { Globe } from "lucide-react";
import type { Service, Snapshot, Workload } from "../api";
import { ServiceCard } from "../components/ServiceCard";
import { buildGraph, INTERNET, shapeKey } from "./graph";
import { groupId, layout, type Placed, type Route } from "./layout";
import "./map.css";

type ServiceData = {
  service: Service;
  workloads: Workload[];
  selected: boolean;
  dimmed: boolean;
  dependsOnBroken?: string;
};
type GroupData = { namespace: string; count: number; app?: string; dimmed: boolean };

function ServiceNode({ data }: NodeProps<Node<ServiceData>>) {
  return (
    <>
      <Handle type="target" position={Position.Left} className="cv-handle" />
      <ServiceCard
        service={data.service}
        workloads={data.workloads}
        selected={data.selected}
        dimmed={data.dimmed}
        dependsOnBroken={data.dependsOnBroken}
      />
      <Handle type="source" position={Position.Right} className="cv-handle" />
    </>
  );
}

function GroupNode({ data }: NodeProps<Node<GroupData>>) {
  return (
    <div className={`cv-group${data.dimmed ? " is-dimmed" : ""}`}>
      <span className="cv-group-label">
        <span className="cv-group-ns">{data.namespace}</span>
        <span className="cv-group-count">
          {data.count} service{data.count === 1 ? "" : "s"}
        </span>
      </span>
    </div>
  );
}

function InternetNode() {
  return (
    <>
      <div className="cv-internet">
        <Globe size={16} strokeWidth={2} /> Internet
      </div>
      <Handle type="source" position={Position.Right} className="cv-handle" />
    </>
  );
}

type RouteData = { points: Route["points"]; label?: string };

/** ELK's orthogonal route, drawn with rounded corners. */
function RouteEdge({ data, markerEnd, style }: EdgeProps<Edge<RouteData>>) {
  const pts = data?.points ?? [];
  if (pts.length < 2) return null;
  const r = 10;
  let d = `M ${pts[0].x} ${pts[0].y}`;
  for (let i = 1; i < pts.length - 1; i++) {
    const [a, b, c] = [pts[i - 1], pts[i], pts[i + 1]];
    const inLen = Math.hypot(b.x - a.x, b.y - a.y);
    const outLen = Math.hypot(c.x - b.x, c.y - b.y);
    const k = Math.min(r, inLen / 2, outLen / 2);
    const p1 = { x: b.x - ((b.x - a.x) / inLen) * k, y: b.y - ((b.y - a.y) / inLen) * k };
    const p2 = { x: b.x + ((c.x - b.x) / outLen) * k, y: b.y + ((c.y - b.y) / outLen) * k };
    d += ` L ${p1.x} ${p1.y} Q ${b.x} ${b.y} ${p2.x} ${p2.y}`;
  }
  const last = pts[pts.length - 1];
  d += ` L ${last.x} ${last.y}`;

  // A small arrowhead shows which way the call goes.
  const prev = pts[pts.length - 2];
  const len = Math.hypot(last.x - prev.x, last.y - prev.y) || 1;
  const [ux, uy] = [(last.x - prev.x) / len, (last.y - prev.y) / len];
  const tip = { x: last.x - ux, y: last.y - uy };
  const arrow = `M ${tip.x} ${tip.y} L ${tip.x - ux * 7 - uy * 4} ${tip.y - uy * 7 + ux * 4} L ${tip.x - ux * 7 + uy * 4} ${tip.y - uy * 7 - ux * 4} Z`;

  // The label sits on the last straight run, just before the service.
  const before = pts[pts.length - 2];
  const lx = (before.x + last.x) / 2;
  const ly = (before.y + last.y) / 2;
  return (
    <>
      <BaseEdge path={d} markerEnd={markerEnd} style={style} />
      <path d={arrow} className="cv-edge-arrow" />
      {data?.label && (
        <EdgeLabelRenderer>
          <span className="cv-edge-label" style={{ transform: `translate(-50%, -50%) translate(${lx}px, ${ly}px)` }}>
            {data.label}
          </span>
        </EdgeLabelRenderer>
      )}
    </>
  );
}

const nodeTypes = { service: ServiceNode, namespace: GroupNode, internet: InternetNode };
const edgeTypes = { route: RouteEdge };

interface Props {
  snapshot: Snapshot;
  showSystem: boolean;
  selectedId: string | null;
  onSelect: (id: string | null) => void;
}

export function ServiceMap(props: Props) {
  return (
    <ReactFlowProvider>
      <MapCanvas {...props} />
    </ReactFlowProvider>
  );
}

function MapCanvas({ snapshot, showSystem, selectedId, onSelect }: Props) {
  const graph = useMemo(() => buildGraph(snapshot, showSystem), [snapshot, showSystem]);
  const key = shapeKey(graph);
  const [layoutState, setLayoutState] = useState<{
    key: string;
    placed: Map<string, Placed>;
    routes: Map<string, Route>;
  } | null>(null);
  const placed = layoutState?.placed ?? null;
  const [hovered, setHovered] = useState<string | null>(null);

  // Lay out again only when the shape changes, never on a status update.
  useEffect(() => {
    let cancelled = false;
    layout(graph).then((l) => {
      if (!cancelled) setLayoutState({ key, placed: new Map(l.nodes.map((x) => [x.id, x])), routes: l.routes });
    });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);


  // Bring the selected service into view, left of the drawer that opens on
  // the right.
  const { setCenter, getZoom } = useReactFlow();
  // Set once the canvas has done its first fit, so panning isn't undone by it.
  const [ready, setReady] = useState(false);
  useEffect(() => {
    if (!selectedId || !placed || !ready) return;
    const p = placed.get(selectedId);
    if (!p) return;
    const parent = p.parent ? placed.get(p.parent) : undefined;
    const x = (parent?.x ?? 0) + p.x + p.w / 2;
    const y = (parent?.y ?? 0) + p.y + p.h / 2;
    const zoom = Math.max(getZoom(), 0.85);
    const drawer = Math.min(552, window.innerWidth * 0.45);
    void setCenter(x + drawer / 2 / zoom, y, { zoom, duration: 450 });
  }, [selectedId, placed, ready, setCenter, getZoom]);

  const workloads = useMemo(() => new Map(snapshot.workloads.map((w) => [w.id, w])), [snapshot]);
  const byId = useMemo(() => new Map(graph.services.map((s) => [s.id, s])), [graph]);

  // Hovering (or selecting) a service highlights it and its direct neighbours.
  const focus = hovered ?? selectedId;
  const neighbours = useMemo(() => {
    if (!focus) return null;
    const set = new Set([focus]);
    for (const e of graph.edges) {
      if (e.from === focus) set.add(e.to);
      if (e.to === focus) set.add(e.from);
    }
    if (graph.entry.some((e) => e.id === focus)) set.add(INTERNET);
    return set;
  }, [focus, graph]);

  const nodes = useMemo<Node[]>(() => {
    if (!placed) return [];
    const out: Node[] = [];
    const internet = placed.get(INTERNET);
    if (internet) {
      out.push({
        id: INTERNET,
        type: "internet",
        position: { x: internet.x, y: internet.y },
        data: {},
        draggable: false,
        selectable: false,
        className: neighbours && !neighbours.has(INTERNET) ? "is-dimmed" : "",
      });
    }
    for (const ns of graph.namespaces) {
      const g = placed.get(groupId(ns));
      if (!g) continue;
      const members = graph.services.filter((s) => s.namespace === ns);
      out.push({
        id: groupId(ns),
        type: "namespace",
        position: { x: g.x, y: g.y },
        style: { width: g.w, height: g.h },
        data: {
          namespace: ns,
          count: members.length,
          dimmed: !!neighbours && !members.some((m) => neighbours.has(m.id)),
        },
        draggable: false,
        selectable: false,
        zIndex: -1,
      });
    }
    for (const s of graph.services) {
      const p = placed.get(s.id);
      if (!p) continue;
      const broken = graph.edges
        .filter((e) => e.from === s.id)
        .map((e) => byId.get(e.to))
        .find((t) => t?.status === "down");
      out.push({
        id: s.id,
        type: "service",
        parentId: p.parent,
        position: { x: p.x, y: p.y },
        data: {
          service: s,
          workloads: s.workloads.map((id) => workloads.get(id)).filter((w): w is Workload => !!w),
          selected: s.id === selectedId,
          dimmed: !!neighbours && !neighbours.has(s.id),
          dependsOnBroken: s.status !== "down" ? broken?.title : undefined,
        } satisfies ServiceData,
        draggable: false,
      });
    }
    return out;
  }, [placed, graph, workloads, byId, selectedId, neighbours]);

  const edges = useMemo<Edge[]>(() => {
    const all = [
      ...graph.entry.map((e) => ({ id: `${INTERNET}->${e.id}`, from: INTERNET, to: e.id, label: e.label || undefined })),
      ...graph.edges,
    ];
    return all.map((e) => {
      const target = byId.get(e.to);
      const broken = target?.status === "down";
      const hot = !!focus && (e.from === focus || e.to === focus);
      const dim = !!focus && !hot;
      return {
        id: e.id,
        source: e.from,
        target: e.to,
        type: "route",
        data: { points: layoutState?.routes.get(e.id)?.points ?? [], label: e.label } satisfies RouteData,
        className: `cv-edge${broken ? " is-broken" : ""}${hot ? " is-hot" : ""}${dim ? " is-dimmed" : ""}`,
      };
    });
  }, [graph, byId, focus, layoutState]);

  if (!layoutState) return null;
  return (
    <div className="cv-map" onKeyDown={(e) => e.key === "Escape" && onSelect(null)}>
      <ReactFlow
        // A new shape gets a fresh canvas, fitted to the view.
        key={layoutState.key}
        fitView
        fitViewOptions={{ padding: 0.08 }}
        onInit={() => setTimeout(() => setReady(true), 50)}
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        nodesConnectable={false}
        nodesDraggable={false}
        elementsSelectable={false}
        onNodeClick={(_, n) => n.type === "service" && onSelect(n.id)}
        onPaneClick={() => onSelect(null)}
        onNodeMouseEnter={(_, n) => n.type === "service" && setHovered(n.id)}
        onNodeMouseLeave={() => setHovered(null)}
        minZoom={0.2}
        maxZoom={1.6}
        proOptions={{ hideAttribution: true }}
      >
        <Background variant={BackgroundVariant.Dots} gap={20} size={1.4} color="var(--cv-canvas-dot)" />
        <Controls showInteractive={false} position="bottom-left" className="cv-controls" />
      </ReactFlow>
    </div>
  );
}
