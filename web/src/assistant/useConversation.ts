import { useEffect, useRef, useState } from "react";
import { askAssistant, forgetConversation, type AssistantReply } from "./api";

export type ChatItem = { role: "user"; text: string } | { role: "assistant"; reply: AssistantReply };

/** Own request lifetime here; the panel remains mounted when hidden. */
export function useConversation() {
  const [items, setItems] = useState<ChatItem[]>([]);
  const [sessionId, setSessionId] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const request = useRef<AbortController | null>(null);
  useEffect(() => () => request.current?.abort(), []);
  const send = async (message: string, selectedServiceId: string) => {
    if (request.current || !message.trim()) return;
    const controller = new AbortController();
    request.current = controller;
    setPending(true);
    setError("");
    setItems((old) => [...old, { role: "user", text: message }]);
    try {
      const reply = await askAssistant({ sessionId, message, selectedServiceId }, controller.signal);
      if (controller.signal.aborted) return;
      setSessionId(reply.sessionId);
      setItems((old) => [...old, { role: "assistant", reply }]);
    } catch (e) {
      if (!controller.signal.aborted) setError(e instanceof Error ? e.message : "The assistant is unavailable. Try again.");
    } finally {
      if (request.current === controller) {
        request.current = null;
        setPending(false);
      }
    }
  };
  const stop = () => {
    request.current?.abort();
    request.current = null;
    setPending(false);
    setError("Investigation stopped. You can ask another question.");
  };
  const reset = () => {
    request.current?.abort();
    request.current = null;
    void forgetConversation(sessionId);
    setItems([]);
    setSessionId("");
    setError("");
    setPending(false);
  };
  return { items, pending, error, send, stop, reset };
}
