# Service drawer ownership

- `ServiceDrawer.tsx` renders service details, pod health and existing actions.
  The app keys the drawer by service ID so switching services seeds a fresh view.
- `Connections.tsx` renders incoming/outgoing connections and their evidence.
- `LogsTab.tsx` owns the selected pod/container, filters and display controls.
- `useLogStream.ts` owns history requests, the SSE lifetime, connection status,
  batched lines and stream cleanup. It does not depend on presentation filters.
- `LogOutput.tsx` renders lines and owns only its scrolling DOM integration.
  Scrolling up pauses auto-scroll; Jump to latest resumes it without reopening
  the stream.
- `log-lines.ts` bounds/sorts retained lines and formats the filtered copy output.
- `actions.tsx` and `FindError.tsx` retain their existing authorization and flows.

Log sources use full names in tooltips and copied output. Compact labels keep the
pod's identifying suffix visible. New viewer controls are read-only and use the
existing shared button, icon and toast patterns; no new UI dependency is needed.
