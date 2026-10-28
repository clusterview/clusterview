import type { Category, Service, Snapshot } from "../api";

/** Namespaces whose services are all plumbing are tucked away by default. */
const systemCategories = new Set<Category>(["platform", "network", "dns", "monitoring", "security", "storage"]);

export function isSystemNamespace(ns: string, services: Service[]) {
  if (ns.startsWith("kube-")) return true;
  const inNs = services.filter((s) => s.namespace === ns);
  return inNs.length > 0 && inNs.every((s) => systemCategories.has(s.category));
}

export function needsAttention(s: Service) {
  return s.status === "down" || s.status === "degraded";
}

export interface MapGraph {
  services: Service[];
  namespaces: string[];
  edges: { id: string; from: string; to: string; label?: string }[];
  /** Services with Ingress entrypoints, reached from the "Internet" node. */
  entry: { id: string; label: string }[];
  hiddenCount: number;
}

export const INTERNET = "internet";

/** What the map shows: every service except tucked-away system ones, which
 *  still appear when something's wrong with them. */
export function buildGraph(snap: Snapshot, showSystem: boolean): MapGraph {
  const system = new Set(
    [...new Set(snap.services.map((s) => s.namespace))].filter((ns) => isSystemNamespace(ns, snap.services)),
  );
  const services = snap.services.filter((s) => showSystem || !system.has(s.namespace) || needsAttention(s));
  const visible = new Set(services.map((s) => s.id));
  const namespaces = [...new Set(services.map((s) => s.namespace))];

  const edges: MapGraph["edges"] = snap.links
    .filter((l) => visible.has(l.from) && visible.has(l.to))
    .map((l) => ({ id: `${l.from}->${l.to}`, from: l.from, to: l.to }));

  // Outside traffic goes through the ingress controller when there's one to
  // pin it on: Internet → NGINX Ingress → storefront.
  const controllers = services.filter((s) => s.category === "ingress");
  const controllerFor = (cls?: string) =>
    controllers.find((c) => cls && `${c.name} ${c.title}`.toLowerCase().includes(cls.toLowerCase())) ??
    (controllers.length === 1 ? controllers[0] : undefined);

  const entry: MapGraph["entry"] = [];
  const routed = new Set<string>();
  for (const s of services) {
    for (const e of s.entrypoints) {
      const label = `${e.host ?? "*"}${e.path && e.path !== "/" ? e.path : ""}`;
      const via = controllerFor(e.class);
      if (via && via.id !== s.id) {
        routed.add(via.id);
        const id = `${via.id}->${s.id}`;
        const existing = edges.find((x) => x.id === id);
        if (existing) existing.label = [existing.label, label].filter(Boolean).join(", ");
        else edges.push({ id, from: via.id, to: s.id, label });
      } else {
        const existing = entry.find((x) => x.id === s.id);
        if (existing) existing.label += `, ${label}`;
        else entry.push({ id: s.id, label });
      }
    }
  }
  for (const id of routed) if (!entry.some((x) => x.id === id)) entry.push({ id, label: "" });

  return { services, namespaces, edges, entry, hiddenCount: snap.services.length - services.length };
}

/** A key that changes only when the shape of the map does, so status updates
 *  never move boxes around. */
export function shapeKey(g: MapGraph) {
  return JSON.stringify([g.services.map((s) => s.id).sort(), g.edges.map((e) => e.id).sort(), g.entry.map((e) => e.id).sort()]);
}
