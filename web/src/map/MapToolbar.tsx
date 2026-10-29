import { Panel, useReactFlow, useViewport } from "@xyflow/react";
import { Focus, Maximize, Route, X, ZoomIn, ZoomOut } from "lucide-react";

interface Props {
  selectedTitle?: string;
  focused: boolean;
  serviceCount: number;
  hiddenCount: number;
  showLabels: boolean;
  onToggleLabels: () => void;
  onToggleFocus: () => void;
  onClear: () => void;
}

export function MapToolbar({ selectedTitle, focused, serviceCount, hiddenCount, showLabels, onToggleLabels, onToggleFocus, onClear }: Props) {
  const { fitView, zoomIn, zoomOut } = useReactFlow();
  const { zoom } = useViewport();
  const duration = () => window.matchMedia("(prefers-reduced-motion: reduce)").matches ? 0 : 250;
  return <>
    <Panel position="top-left" className="cv-map-toolbar">
      <div className="cv-map-context">
        <strong>{focused ? "Connected services" : "Service map"}</strong>
        <span>{serviceCount} service{serviceCount === 1 ? "" : "s"}{hiddenCount > 0 ? ` · ${hiddenCount} hidden` : ""}</span>
      </div>
      <div className="cv-map-actions">
        <button className="cv-btn is-secondary is-sm" disabled={!selectedTitle}
          title={focused ? "Show all services" : selectedTitle ? `Show direct connections for ${selectedTitle}` : "Select a service first"} onClick={onToggleFocus}>
          <Focus size={14} /> {focused ? "All services" : "Focus connections"}
        </button>
        <button className="cv-btn is-ghost is-sm" aria-pressed={showLabels} onClick={onToggleLabels} title="Show ingress hosts and paths on the map">
          <Route size={14} /> Route labels
        </button>
      </div>
      {focused && <p className="cv-map-focus-name" title={selectedTitle}>{selectedTitle}</p>}
    </Panel>
    <Panel position="bottom-left" className="cv-map-navigation">
      <div className="cv-map-zoom" role="group" aria-label="Map navigation">
        <button onClick={() => void zoomOut({ duration: duration() })} aria-label="Zoom out" title="Zoom out" disabled={zoom <= 0.2}><ZoomOut size={16} /></button>
        <span aria-label="Zoom level">{Math.round(zoom * 100)}%</span>
        <button onClick={() => void zoomIn({ duration: duration() })} aria-label="Zoom in" title="Zoom in" disabled={zoom >= 1.6}><ZoomIn size={16} /></button>
        <button onClick={() => void fitView({ padding: focused ? 0.08 : 0.15, duration: duration() })} title="Fit map to view"><Maximize size={15} /> Fit map</button>
        {selectedTitle && <button onClick={onClear} title="Clear selection and show all services"><X size={15} /> Reset view</button>}
      </div>
      <p className="cv-map-legend"><span aria-hidden="true">→</span> Arrows point to the service being called.</p>
    </Panel>
  </>;
}
