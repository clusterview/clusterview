import { useEffect, useMemo, useState } from "react";
import { Background, BackgroundVariant, ReactFlow, ReactFlowProvider, type Edge, type Node } from "@xyflow/react";
import "@xyflow/react/dist/base.css";
import type { Snapshot, Workload } from "../api";
import { buildGraph, connectedGraph, INTERNET, shapeKey } from "./graph";
import { groupId, layout, type Placed, type Route } from "./layout";
import { edgeTypes, nodeTypes, type RouteData, type ServiceData } from "./MapElements";
import { MapToolbar } from "./MapToolbar";
import { MapViewport } from "./MapViewport";
import "./map.css";

interface Props {
  snapshot: Snapshot;
  showSystem: boolean;
  detailsOpen?: boolean;
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

function MapCanvas({ snapshot, showSystem, selectedId, onSelect, detailsOpen = true }: Props) {
  const [focusConnections, setFocusConnections] = useState(false);
  const [showLabels, setShowLabels] = useState(false);
  const selected = snapshot.services.find((service) => service.id === selectedId);
  const focused = focusConnections && !!selected;
  const graph = useMemo(() => {
    const all = buildGraph(snapshot, focused || showSystem, selectedId);
    return focused && selectedId ? connectedGraph(all, selectedId) : all;
  }, [snapshot, showSystem, selectedId, focused]);
  const key = `${focused ? `connections:${selectedId}` : "overview"}:${shapeKey(graph)}`;
  const [layoutState, setLayoutState] = useState<{
    key: string;
    placed: Map<string, Placed>;
    routes: Map<string, Route>;
  } | null>(null);
  const placed = layoutState?.placed ?? null;
  const [hovered, setHovered] = useState<string | null>(null);
  const [layoutError, setLayoutError] = useState(false);
  const [attempt, setAttempt] = useState(0);

  // Lay out again only when the shape changes, never on a status update.
  useEffect(() => {
    let cancelled = false;
    setLayoutError(false);
    layout(graph, focused ? selectedId : null).then((l) => {
      if (!cancelled) setLayoutState({ key, placed: new Map(l.nodes.map((x) => [x.id, x])), routes: l.routes });
    }).catch(() => { if (!cancelled) setLayoutError(true); });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, attempt]);


  const workloads = useMemo(() => new Map(snapshot.workloads.map((w) => [w.id, w])), [snapshot]);
  const byId = useMemo(() => new Map(graph.services.map((s) => [s.id, s])), [graph]);

  // Hovering (or selecting) a service highlights it and its direct neighbours.
  const focus = (hovered && byId.has(hovered) ? hovered : null) ?? selectedId;
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
        width: internet.w,
        height: internet.h,
        data: {},
        draggable: false,
        selectable: false,
        focusable: false,
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
        width: g.w,
        height: g.h,
        style: { width: g.w, height: g.h },
        data: {
          namespace: ns,
          count: members.length,
          dimmed: !!neighbours && !members.some((m) => neighbours.has(m.id)),
        },
        draggable: false,
        selectable: false,
        focusable: false,
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
        width: p.w,
        height: p.h,
        data: {
          service: s,
          workloads: s.workloads.map((id) => workloads.get(id)).filter((w): w is Workload => !!w),
          selected: s.id === selectedId,
          dimmed: !!neighbours && !neighbours.has(s.id),
          onSelect,
          showNamespace: focused,
          dependsOnBroken: s.status !== "down" ? broken?.title : undefined,
        } satisfies ServiceData,
        draggable: false,
        focusable: false,
      });
    }
    return out;
  }, [placed, graph, workloads, byId, selectedId, neighbours, onSelect, focused]);

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
        data: { points: layoutState?.routes.get(e.id)?.points ?? [], label: showLabels ? e.label : undefined } satisfies RouteData,
        className: `cv-edge${broken ? " is-broken" : ""}${hot ? " is-hot" : ""}${dim ? " is-dimmed" : ""}`,
      };
    });
  }, [graph, byId, focus, layoutState, showLabels]);

  return (
    <div className={`cv-map${selected && detailsOpen ? " has-selection" : ""}${focused ? " is-focused" : ""}`} onKeyDown={(event) => {
      if (event.key === "Escape") { setFocusConnections(false); onSelect(null); }
    }}>
      <ReactFlow
        // A new shape gets a fresh canvas, fitted to the view.
        key={layoutState?.key ?? "loading"}
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        nodesConnectable={false}
        nodesDraggable={false}
        elementsSelectable={false}
        onPaneClick={() => { setFocusConnections(false); onSelect(null); }}
        onNodeMouseEnter={(_, n) => n.type === "service" && setHovered(n.id)}
        onNodeMouseLeave={() => setHovered(null)}
        minZoom={0.2}
        maxZoom={1.6}
        proOptions={{ hideAttribution: true }}
      >
        <Background variant={BackgroundVariant.Dots} gap={20} size={1.4} color="var(--cv-canvas-dot)" />
        <MapViewport selectedId={selectedId} placed={placed} focused={focused} />
        <MapToolbar
          selectedTitle={selected?.title}
          focused={focused}
          serviceCount={graph.services.length}
          hiddenCount={graph.hiddenCount}
          showLabels={showLabels}
          onToggleLabels={() => setShowLabels(!showLabels)}
          onToggleFocus={() => setFocusConnections(!focused)}
          onClear={() => { setFocusConnections(false); onSelect(null); }}
        />
      </ReactFlow>
      {(!layoutState || layoutError || graph.services.length === 0) && <div className="cv-map-message" role={layoutError ? "alert" : "status"}>
        {layoutError ? <>Couldn’t arrange the map. <button className="cv-btn is-secondary is-sm" onClick={() => setAttempt(attempt + 1)}>Try again</button></> : graph.services.length === 0 ? "No services found in the watched namespaces." : "Arranging services…"}
      </div>}
    </div>
  );
}
