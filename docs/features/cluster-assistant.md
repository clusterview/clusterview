# Cluster assistant

The **Assistant** button opens a troubleshooting chat on the right of the map,
list or nodes view. Selecting a service focuses subsequent questions on it.
Use the arrow beside the focus to open the existing service drawer, or clear
it to investigate the cluster as a whole. On small screens the chat occupies
the workspace. Closing it preserves the conversation until the page reloads;
**New chat** clears the transcript and requests deletion of its server session.

## Setup

Use the existing `ai.provider`, `ai.model`, `ai.existingSecret` and optional
`ai.baseURL` chart settings. There is one provider, key and model: the same
model screens input and investigates. No second classifier is configured.
AI is off when no provider is set. A cloud provider without an API key also
leaves AI off: the dashboard starts normally, logs a setup warning, and the
sidebar shows **Connect your AI provider** without a chat input. A missing AI
Secret or `apiKey` field behaves the same way; login credentials remain required.
Keyless custom OpenAI-compatible endpoints continue to work. Other invalid AI
settings, such as an unknown provider or a missing model when a key is supplied,
still report a configuration error. The unconfigured sidebar links to setup.

- `anthropic`: existing Anthropic integration; the existing default model
  applies if `ai.model` is omitted.
- `openai`: specify a model supporting structured JSON responses. A custom
  base URL supports compatible servers, including local models.
- `gemini`: specify a Gemini model supporting structured JSON responses.
  The default endpoint is Google's OpenAI-compatible
  `https://generativelanguage.googleapis.com/v1beta/openai/` endpoint.

Keys stay on the server, normally in the existing Secret's `apiKey` field.
For the CLI, `CLUSTERVIEW_AI_API_KEY` overrides `ANTHROPIC_API_KEY`,
`OPENAI_API_KEY` or `GEMINI_API_KEY` for the selected provider. All AI features
share this configuration. Choose a model that can classify scope and follow
the bounded JSON tool protocol within the deadlines; compatibility alone
does not establish troubleshooting or guardrail quality.

No Kubernetes permission changes are needed. Namespace and node visibility
follow the dashboard's watch scope. Node details require `scope.nodes`; live
usage requires metrics-server. Chat works for authenticated viewers as well
as operators. With password auth all users share one identity and rate limit;
with auth disabled callers share the anonymous identity. Session IDs still
isolate separate conversations, but use OIDC or a trusted auth proxy for
individual identities. With several replicas, use session affinity if a
conversation must survive routing between requests; sessions are process-local.

## Investigation flow

1. The server accepts a message, optional session ID and selected service ID.
   It owns conversation history; clients cannot submit model or tool messages.
2. A scope check receives redacted input and recent conversation, with no new
   cluster evidence. Off-topic, override, secret-exfiltration and execution
   requests get a bounded refusal or clarification. Legitimate investigation
   of authentication failures or quoted attacks remains in scope. Invalid or
   unavailable classification stops the request.
3. An allowed turn gets a fresh cached health snapshot: summary counts, up to
   80 services (unhealthy first), and up to 40 node summaries. A valid selected
   service adds bounded workloads, pod health, events and direct discovered
   connections. No logs are fetched initially.
4. The model may request a service search, an exact service or node, whitelisted
   details for one watched pod, or a short log tail for one watched pod/container.
   Every operation is checked server-side. No arbitrary API paths, commands,
   URL fetching, Secret reads, mutation or repository tools are exposed.
5. An answer includes confidence, next steps, unknowns and exact quotes from
   inspected evidence. The server verifies each quote occurs in the cited,
   redacted source and rejects citations to failed tool calls. This checks
   provenance, not the correctness of the model's conclusion.
6. **Copy for coding agent** creates a plain-text handoff with findings,
   evidence, timestamps, uncertainty and suggested verification. The assistant
   has no repository access and must not invent file paths or claim to fix code.

Configuration-derived connections are not traffic measurements. Pod readiness
is not an end-to-end website check. Current node usage cannot establish long-term
capacity or cost optimization. Answers should distinguish observations from
hypotheses and say which evidence is missing.

## Bounds and data handling

- One request per conversation; three concurrent investigations per process.
- Ten attempted requests per minute per identity, including new conversations.
- Up to ten completed turns per session, 64 sessions per process, 30-minute idle
  expiry. Sessions exist only in memory; restarts and page reloads lose continuity.
- Up to five tools per turn, including at most three log tails. Each tail requests
  80 lines and at most 12 KiB, current or previous container, with an eight-second
  timeout. There is no all-pod log operation in chat.
- Evidence is redacted and clipped to about 12 KiB per source. The serialized
  investigation prompt is capped at 96 KiB. Only the last six conversation
  messages accompany a turn; previous evidence is not replayed automatically.
- Ninety-second overall deadline, including a 20-second scope check. Output
  token budgets are sent to the provider; provider retries or JSON fallback can
  make additional HTTP calls within that deadline. **Stop** cancels the request;
  a provider may already have processed billable work.

Cluster names, topology, images, events and selected logs are sent to the
configured provider when investigating. The scope check also receives recent
redacted answers. Pod details omit environment values, commands, arguments,
annotations and HTTP headers. Evidence and responses use the existing secret
redactor, which is best effort and cannot recognize every sensitive value.
Do not paste credentials into chat. Provider retention and billing policies
apply; use an approved local endpoint where required.

Model input screening and instructions reduce risk, but do not guarantee
injection or jailbreak detection. Server-enforced scope, allowlisted read-only
tools, bounds, owner checks and plain-text rendering form the hard limits.
Answers never render model HTML, remote images or automatic links. Model errors
return a generic message instead of logging prompts, provider payloads or keys.

## API and implementation

- `GET /api/v1/ai` adds `chat` availability.
- `POST /api/v1/assistant`: `{ "message": "...", "sessionId": "...", "selectedServiceId": "..." }`.
  Only `message` is necessary. Requests require `X-Clusterview-Action` and existing
  dashboard authentication. Unknown fields and multiple JSON objects are rejected.
- `DELETE /api/v1/assistant/{session}` deletes only that identity's idle session.
- Responses disable caching. Expired/unavailable sessions return 404, rate or
  concurrency limits 429, invalid input 400, provider/protocol failures 502.

Backend ownership: `internal/assistant` contains scope classification, bounded
context, tool execution and sessions. `internal/api/assistant.go` supplies HTTP
validation and identity. `internal/ai` remains the provider boundary.
Frontend ownership is described in `web/src/assistant/README.md`.

## Verification

Unit tests use scripted model replies to exercise enforcement, not to claim
semantic jailbreak resistance. Tests cover pre-context refusals, scope, real
HTTP pod-log request shape, redaction, exact quotes, budgets, cancellation,
owner isolation, authentication and malformed input. Run:

```bash
go test -race ./...
npm test --prefix web
npm run build --prefix web
```

There is an **opt-in, billable** scope conformance evaluation with synthetic
inputs (health, remediation advice, quoted attacks, authentication debugging,
off-topic chat, role overrides, secret exfiltration and write requests):

```bash
# Configure CLUSTERVIEW_AI_PROVIDER, CLUSTERVIEW_AI_MODEL,
# CLUSTERVIEW_AI_API_KEY and optional CLUSTERVIEW_AI_BASE_URL first.
CLUSTERVIEW_ASSISTANT_EVAL=1 go test ./internal/assistant -run TestLiveScopeEval -v -count=1
```

This small evaluation is a regression check, not a comprehensive security
assessment. It should be run against the actual configured model before rollout.
Browser fixtures at `?mock=demo&assistant=open` use **simulated evidence**, not a
provider. Add `&ai=off` for setup or `&assistantError=1` for a provider failure.

Protocol references: [OpenAI structured outputs](https://developers.openai.com/api/docs/guides/structured-outputs)
and [Gemini's compatible endpoint](https://ai.google.dev/gemini-api/docs/openai).
