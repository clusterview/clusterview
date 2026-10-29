import { BaseEdge, EdgeLabelRenderer, Handle, Position, type Edge, type EdgeProps, type Node, type NodeProps } from "@xyflow/react";
import { Globe } from "lucide-react";
import type { Service, Workload } from "../api";
import { ServiceCard } from "../components/ServiceCard";
import type { Route } from "./layout";

export type ServiceData = {
  service: Service;
  workloads: Workload[];
  selected: boolean;
  showNamespace: boolean;
  dimmed: boolean;
  onSelect: (id: string) => void;
  dependsOnBroken?: string;
};
export type GroupData = { namespace: string; count: number; app?: string; dimmed: boolean };

function ServiceNode({ data }: NodeProps<Node<ServiceData>>) {
  return (
    <div role="button" tabIndex={0} className="cv-service-target"
      aria-label={`${data.service.title}, ${data.service.namespace}, ${data.service.status}`}
      aria-pressed={data.selected}
      onClick={() => data.onSelect(data.service.id)}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          data.onSelect(data.service.id);
        }
      }}>
      <Handle type="target" position={Position.Left} className="cv-handle" />
      <ServiceCard
        service={data.service}
        workloads={data.workloads}
        selected={data.selected}
        showNamespace={data.showNamespace}
        dimmed={data.dimmed}
        dependsOnBroken={data.dependsOnBroken}
      />
      <Handle type="source" position={Position.Right} className="cv-handle" />
    </div>
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

export type RouteData = { points: Route["points"]; label?: string };

/** Draw the layout’s orthogonal routes with rounded corners. */
function RouteEdge({ data, markerEnd, style }: EdgeProps<Edge<RouteData>>) {
  const pts = (data?.points ?? []).filter((point, index, points) => index === 0 || point.x !== points[index - 1].x || point.y !== points[index - 1].y);
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
          <span title={data.label} className="cv-edge-label" style={{ transform: `translate(-50%, -50%) translate(${lx}px, ${ly}px)` }}>
            {data.label}
          </span>
        </EdgeLabelRenderer>
      )}
    </>
  );
}

export const nodeTypes = { service: ServiceNode, namespace: GroupNode, internet: InternetNode };
export const edgeTypes = { route: RouteEdge };

