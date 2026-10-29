import { ArrowDownLeft, ArrowUpRight, Globe } from "lucide-react";
import type { Link, Service, Snapshot } from "../api";
import { CategoryIcon } from "../components/CategoryIcon";
import { toneOf } from "../components/ServiceCard";
import { StatusPill } from "../components/ui";

export function Connections({ service, snapshot, onSelect }: { service: Service; snapshot: Snapshot; onSelect: (id: string) => void }) {
  const byId = new Map(snapshot.services.map((item) => [item.id, item]));
  const outgoing = snapshot.links.filter((link) => link.from === service.id && byId.has(link.to));
  const incoming = snapshot.links.filter((link) => link.to === service.id && byId.has(link.from));
  const group = (links: Link[], direction: "in" | "out") => <div className="cv-connection-group">
    <h4>{direction === "out" ? <ArrowUpRight size={14} /> : <ArrowDownLeft size={14} />}
      {direction === "out" ? "Calls" : "Called by"} <span className="cv-count">{links.length}</span>
    </h4>
    <ul className="cv-connections">
      {links.map((link, index) => {
        const peer = byId.get(direction === "out" ? link.to : link.from)!;
        const tone = toneOf(peer);
        return <li key={`${peer.id}/${index}`}>
          <button className="cv-connection" onClick={() => onSelect(peer.id)}>
            <CategoryIcon category={peer.category} size={28} />
            <span className="cv-connection-main">
              <strong>{peer.title}</strong>
              <span>{peer.namespace} / {peer.name}</span>
              <span className="cv-connection-evidence">{link.via}{link.evidence && <> · Found in {link.evidence}</>}</span>
            </span>
            <StatusPill status={tone === "warning" ? "warning" : peer.status} />
          </button>
        </li>;
      })}
    </ul>
  </div>;
  return <section className="cv-section">
    <h3>Connections</h3>
    <p className="cv-note cv-connection-note">Discovered from Kubernetes configuration. Arrows show the direction of each connection.</p>
    {service.entrypoints.length > 0 && <div className="cv-connection-group">
      <h4><Globe size={14} /> From outside <span className="cv-count">{service.entrypoints.length}</span></h4>
      <ul className="cv-entrypoints">{service.entrypoints.map((entry, index) => <li key={index}>
        <span className="cv-mono">{entry.tls ? "https" : "http"}://{entry.host ?? "*"}{entry.path ?? ""}</span>
        <span className="cv-note">Ingress {entry.ingress}</span>
      </li>)}</ul>
    </div>}
    {outgoing.length > 0 && group(outgoing, "out")}
    {incoming.length > 0 && group(incoming, "in")}
    {!outgoing.length && !incoming.length && !service.entrypoints.length && <p className="cv-note">No connections discovered for this service.</p>}
  </section>;
}
