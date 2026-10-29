# Service map ownership

- `graph.ts` prepares the visible discovered graph and filters direct connections.
  It keeps namespace scope intact and reveals a service selected from search even
  when healthy system services are hidden.
- `layout.ts` owns ELK layout for the full map and the three-column connected
  layout (callers, selection, dependencies). Positions change only with graph
  shape or an explicit view change, not health updates.
- `ServiceMap.tsx` owns the map view's user intent and composes nodes and routes.
- `MapElements.tsx` renders cards, namespace groups and rounded connection paths.
- `MapToolbar.tsx` owns zoom/fit controls and focus/label toggles.
- `MapViewport.tsx` integrates selection and resizing with the mounted React Flow viewport.

Focused cards carry their namespace because namespace group boxes are omitted.
Route labels are opt-in to avoid covering the graph with ingress host names.
Connection evidence remains visible in the service drawer. Neither layout adds
relationships or measures traffic; all edges come from the discovered snapshot.
