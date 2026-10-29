import { useEffect } from "react";
import { useReactFlow, useStore } from "@xyflow/react";
import type { Placed } from "./layout";

/** Apply explicit view changes once the canvas viewport is mounted. */
export function MapViewport({ selectedId, placed, focused }: { selectedId: string | null; placed: Map<string, Placed> | null; focused: boolean }) {
  const width = useStore((state) => state.width);
  const height = useStore((state) => state.height);
  const { setCenter, getZoom, fitView, viewportInitialized } = useReactFlow();
  useEffect(() => {
    if (!placed || !viewportInitialized || !width || !height) return;
    const duration = window.matchMedia("(prefers-reduced-motion: reduce)").matches ? 0 : 300;
    if (!selectedId || focused) {
      void fitView({ padding: focused ? 0.08 : 0.15, duration });
      return;
    }
    const node = placed.get(selectedId);
    if (!node) return;
    const parent = node.parent ? placed.get(node.parent) : undefined;
    const x = (parent?.x ?? 0) + node.x + node.w / 2;
    const y = (parent?.y ?? 0) + node.y + node.h / 2;
    void setCenter(x, y, { zoom: Math.max(getZoom(), .85), duration });
  }, [selectedId, placed, focused, getZoom, setCenter, fitView, viewportInitialized, width, height]);
  return null;
}
