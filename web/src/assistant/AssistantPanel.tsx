import { useEffect, useRef, useState } from "react";
import { ArrowRight, MessageSquareText, Plus, Send, ShieldCheck, Square, X } from "lucide-react";
import type { AIInfo, Service } from "../api";
import { mockName } from "../mock";
import { AssistantAnswer } from "./AssistantAnswer";
import { useConversation } from "./useConversation";
import "./assistant.css";

export function AssistantPanel({
  open,
  ai,
  selected,
  onClose,
  onInspect,
  onClearSelection,
}: {
  open: boolean;
  ai: AIInfo | null;
  selected: Service | null;
  onClose: () => void;
  onInspect: () => void;
  onClearSelection: () => void;
}) {
  const conversation = useConversation();
  const [draft, setDraft] = useState("");
  const transcript = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLTextAreaElement>(null);
  const stick = useRef(true);
  const [atBottom, setAtBottom] = useState(true);
  const enabled = !!ai?.chat;
  const tooLong = new TextEncoder().encode(draft).length > 4000;
  const full = conversation.items.filter((item) => item.role === "assistant").length >= 10;
  useEffect(() => {
    if (open) input.current?.focus();
  }, [open]);
  useEffect(() => {
    if (stick.current && transcript.current) transcript.current.scrollTop = transcript.current.scrollHeight;
  }, [conversation.items, conversation.pending]);
  const send = (message = draft) => {
    if (!enabled || conversation.pending || !message.trim() || new TextEncoder().encode(message).length > 4000 || full) return;
    setDraft("");
    stick.current = true;
    setAtBottom(true);
    void conversation.send(message.trim(), selected?.id ?? "");
  };
  return (
    <aside
      id="cluster-assistant"
      className="cv-assistant"
      aria-label="Cluster assistant"
      hidden={!open}
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.stopPropagation();
          onClose();
        }
      }}
    >
      <header className="cv-assistant-head">
        <span className="cv-assistant-mark">
          <MessageSquareText size={19} />
        </span>
        <div>
          <h2>Cluster assistant</h2>
          <p>{mockName ? "Demo conversation" : "Investigate · understand · hand off"}</p>
        </div>
        <button
          className="cv-icon-btn"
          aria-label="New chat"
          title="New chat"
          onClick={() => {
            conversation.reset();
            setDraft("");
            input.current?.focus();
          }}
        >
          <Plus size={18} />
        </button>
        <button className="cv-icon-btn" aria-label="Close assistant" onClick={onClose}>
          <X size={18} />
        </button>
      </header>
      <div className="cv-assistant-boundary">
        <ShieldCheck size={13} /> Read-only · No changes made to your cluster
      </div>
      {selected && (
        <div className="cv-assistant-focus">
          <div>
            <span>Current focus</span>
            <strong title={`${selected.namespace}/${selected.name}`}>{selected.title}</strong>
          </div>
          <button className="cv-icon-btn" onClick={onInspect} aria-label="Open focused service details" title="Open service details">
            <ArrowRight size={16} />
          </button>
          <button className="cv-icon-btn" onClick={onClearSelection} aria-label="Clear assistant focus">
            <X size={14} />
          </button>
        </div>
      )}
      {!enabled ? (
        <div className="cv-assistant-setup">
          <h3>{ai ? "Connect your AI provider" : "Checking AI setup…"}</h3>
          <p>Use one provider, API key and model for scope checks and troubleshooting.</p>
          <p>
            Configure Anthropic, OpenAI, Gemini or an OpenAI-compatible endpoint on the Clusterview server. Keep the API key in your
            Kubernetes Secret or server environment.
          </p>
          <a href="https://github.com/clusterview/clusterview#find-error-ai" target="_blank" rel="noreferrer">
            Provider setup instructions <ArrowRight size={14} />
          </a>
          <p className="cv-note">
            Cluster summaries and targeted evidence are sent to your configured provider. Credentials are never entered in this chat.
          </p>
        </div>
      ) : (
        <>
          <div
            className="cv-assistant-transcript"
            ref={transcript}
            role="log"
            aria-label="Assistant conversation"
            aria-live="polite"
            aria-relevant="additions"
            tabIndex={0}
            onScroll={(e) => {
              stick.current = e.currentTarget.scrollHeight - e.currentTarget.scrollTop - e.currentTarget.clientHeight < 60;
              setAtBottom(stick.current);
            }}
          >
            {!conversation.items.length && (
              <div className="cv-assistant-welcome">
                <h3>What needs a closer look?</h3>
                <p>
                  I start with cluster health and connections, then inspect specific resources when needed. Logs are fetched only for
                  targeted investigations.
                </p>
                <div className="cv-assistant-starters">
                  {[
                    selected
                      ? `Investigate ${selected.title}. What evidence explains its health?`
                      : "What needs attention in this cluster?",
                    "How could we improve reliability?",
                  ].map((q) => (
                    <button key={q} onClick={() => send(q)}>
                      {q}
                      <ArrowRight size={15} />
                    </button>
                  ))}
                </div>
                <p className="cv-note">Findings can be incomplete. Check the cited evidence before acting on a suggested fix.</p>
              </div>
            )}
            {conversation.items.map((item, i) => (
              <div key={i} className={`cv-assistant-message is-${item.role}`}>
                <span className="cv-assistant-speaker">{item.role === "user" ? "You" : "Cluster assistant"}</span>
                {item.role === "user" ? <p className="cv-assistant-prose">{item.text}</p> : <AssistantAnswer reply={item.reply} />}
              </div>
            ))}
            {conversation.pending && (
              <p className="cv-assistant-pending" role="status">
                <span /> Checking scope and investigating relevant cluster evidence…
              </p>
            )}
            {conversation.error && (
              <p className="cv-assistant-error" role="alert">
                {conversation.error}
              </p>
            )}
            {full && <p className="cv-note">This conversation has reached its limit. Start a new chat for a fresh investigation.</p>}
          </div>
          {!atBottom && (
            <button
              className="cv-assistant-latest cv-btn is-secondary is-sm"
              onClick={() => {
                stick.current = true;
                setAtBottom(true);
                if (transcript.current) transcript.current.scrollTop = transcript.current.scrollHeight;
              }}
            >
              Latest message ↓
            </button>
          )}
          <form
            className="cv-assistant-composer"
            onSubmit={(e) => {
              e.preventDefault();
              send();
            }}
          >
            <label className="cv-sr-only" htmlFor="cluster-question">
              Ask about this cluster
            </label>
            <textarea
              ref={input}
              id="cluster-question"
              placeholder={selected ? `Ask about ${selected.title}…` : "Ask about this cluster…"}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              maxLength={4000}
              rows={2}
              disabled={full}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                  e.preventDefault();
                  send();
                }
              }}
            />
            <div className="cv-assistant-compose-foot">
              <span>{tooLong ? "Message is too long. Please shorten it." : "Enter to send · Shift+Enter for a new line"}</span>
              {conversation.pending ? (
                <button type="button" className="cv-btn is-secondary is-sm" onClick={conversation.stop}>
                  <Square size={13} /> Stop
                </button>
              ) : (
                <button type="submit" className="cv-btn is-primary is-sm" disabled={!draft.trim() || tooLong || full}>
                  <Send size={14} /> Send
                </button>
              )}
            </div>
          </form>
        </>
      )}
    </aside>
  );
}
