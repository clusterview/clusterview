import { mockResponse } from "../mock";

export interface Evidence {
  id: string;
  label: string;
  data: string;
  error?: boolean;
}
export interface AssistantReply {
  sessionId: string;
  answer: string;
  confidence: "high" | "medium" | "low";
  nextSteps: string[];
  unknowns: string[];
  citations: { source: string; quote: string }[];
  evidence: Evidence[];
  handoff: string;
  model: string;
  askedAt: string;
  snapshotAt: string;
  decision: "allow" | "off_topic" | "blocked" | "clarify";
}
export interface ChatRequest {
  sessionId: string;
  message: string;
  selectedServiceId: string;
}

export async function askAssistant(body: ChatRequest, signal: AbortSignal): Promise<AssistantReply> {
  const mocked = await mockResponse("api/v1/assistant", body);
  if (signal.aborted) throw new DOMException("Stopped", "AbortError");
  if (mocked !== undefined) return mocked as AssistantReply;
  const res = await fetch("api/v1/assistant", {
    method: "POST",
    signal,
    headers: { "X-Clusterview-Action": "1", "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const json = (await res.json()) as AssistantReply & { error?: string };
  if (!res.ok) throw new Error(json.error ?? "The assistant is unavailable. Try again.");
  return json;
}
export async function forgetConversation(sessionId: string) {
  if (!sessionId) return;
  if ((await mockResponse("api/v1/assistant/forget")) !== undefined) return;
  await fetch(`api/v1/assistant/${encodeURIComponent(sessionId)}`, { method: "DELETE", headers: { "X-Clusterview-Action": "1" } }).catch(
    () => undefined,
  );
}
