# Assistant UI ownership

This feature is app-local: its context selection, server-owned sessions, evidence
protocol and handoff are specific to the cluster dashboard. Existing buttons,
tokens, icons and toast infrastructure are reused; no shared primitive or new
dependency is introduced.

- `AssistantPanel.tsx`: presentation, draft, focus, scroll-follow and keyboard input.
- `useConversation.ts`: conversation state and request/abort/reset lifetime. The
  panel remains mounted while hidden; data never goes in browser storage.
- `api.ts`: typed transport using relative URLs for base-path deployments.
- `AssistantAnswer.tsx`: plain-text answers, evidence, uncertainty and handoff copy.
  Never turn model text into HTML, automatic links or remote image requests.
- `assistant.css`: theme tokens, sidebar layout and mobile workspace overlay.
- `App.tsx`: open/close and selected-service ownership. Chat and service details
  share the right side; inspect switches back to the existing drawer.
- `../mock.ts`: development-only simulated responses; not a guardrail evaluation.

The backend refreshes evidence for each turn and owns all history/tool validation.
See `docs/features/cluster-assistant.md` for limits and privacy behavior.

Review with `?mock=demo&assistant=open`: send, evidence details, handoff copy,
follow-up, new chat, close/reopen, stop, service focus/inspect, both themes and
390px width. `&ai=off` shows setup, `&assistantError=1` shows request failure.
