import { Copy } from "lucide-react";
import { useToast } from "../components/ui";
import type { AssistantReply } from "./api";

export function AssistantAnswer({ reply }: { reply: AssistantReply }) {
  const toast = useToast();
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(reply.handoff);
      toast({ ok: true, title: "Coding-agent handoff copied" });
    } catch {
      toast({ ok: false, title: "Couldn’t copy handoff", detail: "Allow clipboard access or select the text in the handoff preview." });
    }
  };
  return (
    <article className="cv-assistant-answer">
      <p className="cv-assistant-prose">{reply.answer}</p>
      {reply.decision === "allow" && (
        <>
          <span className={`cv-assistant-confidence is-${reply.confidence}`}>{reply.confidence} confidence</span>
          {!!reply.nextSteps.length && (
            <section>
              <h4>Next steps</h4>
              <ol>
                {reply.nextSteps.map((step, i) => (
                  <li key={i}>{step}</li>
                ))}
              </ol>
            </section>
          )}
          {!!reply.unknowns.length && (
            <section>
              <h4>Still unverified</h4>
              <ul>
                {reply.unknowns.map((step, i) => (
                  <li key={i}>{step}</li>
                ))}
              </ul>
            </section>
          )}
          {!!reply.citations.length && (
            <section>
              <h4>Supporting evidence</h4>
              {reply.citations.map((citation, i) => (
                <blockquote key={i}>
                  <p>{citation.quote}</p>
                  <cite>{reply.evidence.find((e) => e.id === citation.source)?.label ?? citation.source}</cite>
                </blockquote>
              ))}
            </section>
          )}
          <details className="cv-assistant-evidence">
            <summary>Evidence reviewed ({reply.evidence.length})</summary>
            <p className="cv-note">Snapshot: {new Date(reply.snapshotAt).toLocaleString()}. This is a point-in-time investigation.</p>
            {reply.evidence.map((e) => (
              <details key={e.id}>
                <summary>
                  {e.label}
                  {e.error ? " · unavailable" : ""}
                </summary>
                <pre>{e.data}</pre>
              </details>
            ))}
          </details>
          {reply.handoff && (
            <div className="cv-assistant-handoff">
              <button className="cv-btn is-secondary is-sm" onClick={() => void copy()}>
                <Copy size={14} /> Copy for coding agent
              </button>
              <details>
                <summary>Preview handoff</summary>
                <pre tabIndex={0}>{reply.handoff}</pre>
              </details>
            </div>
          )}
        </>
      )}
      <p className="cv-assistant-meta">
        {reply.model} · {new Date(reply.askedAt).toLocaleTimeString()}
      </p>
    </article>
  );
}
