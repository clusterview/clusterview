import { useEffect, useState } from "react";
import { Check, ChevronRight, Copy, RotateCw, Sparkles } from "lucide-react";
import { findError, type AIInfo, type Diagnosis, type Service } from "../api";
import { Button } from "../components/ui";
import { age } from "../format";

// Answers survive switching tabs and services; the server keeps them a few
// minutes too.
const answers = new Map<string, Diagnosis>();

type State = { kind: "idle" } | { kind: "asking"; started: number } | { kind: "done"; d: Diagnosis } | { kind: "failed"; error: string };

/** "Find error": asks the model why the service is broken. */
export function FindError({ service, ai }: { service: Service; ai: AIInfo }) {
  const [state, setState] = useState<State>(() => {
    const d = answers.get(service.id);
    return d ? { kind: "done", d } : { kind: "idle" };
  });

  useEffect(() => {
    const d = answers.get(service.id);
    setState(d ? { kind: "done", d } : { kind: "idle" });
  }, [service.id]);

  const ask = async (fresh: boolean) => {
    const id = service.id;
    setState({ kind: "asking", started: Date.now() });
    try {
      const d = await findError(id, fresh);
      answers.set(id, d);
      if (id === service.id) setState({ kind: "done", d });
    } catch (e) {
      setState({ kind: "failed", error: e instanceof Error ? e.message : String(e) });
    }
  };

  if (state.kind === "idle") {
    return (
      <div className="cv-ai is-idle">
        <span className="cv-ai-mark">
          <Sparkles size={16} />
        </span>
        <div className="cv-ai-intro">
          <strong>Not sure why?</strong>
          <span>
            Reads its logs, warnings and setup, hides anything that looks secret, and asks <span className="cv-nowrap">{ai.model ?? "the model"}</span>.
          </span>
        </div>
        <Button variant="primary" size="sm" icon={<Sparkles size={14} />} onClick={() => ask(false)}>
          Find error
        </Button>
      </div>
    );
  }

  if (state.kind === "asking") return <Asking started={state.started} />;

  if (state.kind === "failed") {
    return (
      <div className="cv-ai is-failed">
        <strong>Couldn't find the error</strong>
        <p>{state.error}</p>
        <Button size="sm" icon={<RotateCw size={14} />} onClick={() => ask(true)}>
          Try again
        </Button>
      </div>
    );
  }

  const d = state.d;
  return (
    <div className="cv-ai is-done">
      <div className="cv-ai-head">
        <span className="cv-ai-mark">
          <Sparkles size={16} />
        </span>
        <span className="cv-ai-label">AI diagnosis</span>
        <span className={`cv-ai-confidence is-${d.confidence}`}>{confidenceWords[d.confidence]}</span>
      </div>
      <p className="cv-ai-summary">{d.summary}</p>
      <p className="cv-ai-cause">{d.cause}</p>

      {d.fix.length > 0 && (
        <>
          <h4>How to fix it</h4>
          <ol className="cv-ai-fix">
            {d.fix.map((step, i) => (
              <FixStep key={i} step={step} />
            ))}
          </ol>
        </>
      )}

      {d.evidence.length > 0 && (
        <>
          <h4>What shows it</h4>
          <ul className="cv-ai-evidence">
            {d.evidence.map((line, i) => (
              <li key={i} className="cv-mono">
                {line}
              </li>
            ))}
          </ul>
        </>
      )}

      <details className="cv-ai-sent">
        <summary>
          <ChevronRight size={14} /> What was sent
          {d.redactions > 0 && (
            <span className="cv-muted">
              {" "}
              · {d.redactions} value{d.redactions === 1 ? "" : "s"} hidden as possibly secret
            </span>
          )}
        </summary>
        <pre className="cv-mono">{d.sent}</pre>
      </details>

      <div className="cv-ai-foot">
        <span>
          {d.model} · {age(d.askedAt)} ago · AI can be wrong, so check before acting
        </span>
        <Button variant="ghost" size="sm" icon={<RotateCw size={13} />} onClick={() => ask(true)}>
          Ask again
        </Button>
      </div>
    </div>
  );
}

const confidenceWords = { high: "Confident", medium: "Likely", low: "A guess" };

const steps = ["Reading logs and warnings", "Hiding anything secret", "Asking the model", "Working out the cause"];

function Asking({ started }: { started: number }) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 400);
    return () => clearInterval(t);
  }, []);
  // The first two steps are quick; the model takes the time.
  const step = Math.min(steps.length - 1, Math.floor((now - started) / 900));
  return (
    <div className="cv-ai is-asking" role="status" aria-live="polite">
      <div className="cv-ai-head">
        <span className="cv-ai-mark is-busy">
          <Sparkles size={16} />
        </span>
        <span className="cv-ai-label">{steps[step]}…</span>
      </div>
      <div className="cv-ai-skeleton">
        <span style={{ width: "88%" }} />
        <span style={{ width: "72%" }} />
        <span style={{ width: "80%" }} />
      </div>
    </div>
  );
}

/** A step, with any kubectl command in it set apart with a copy button. */
function FixStep({ step }: { step: string }) {
  const at = step.search(/\bkubectl\s/);
  if (at < 0) return <li>{step}</li>;
  const text = step.slice(0, at).replace(/[:\s]+$/, "");
  const command = step.slice(at).replace(/[.\s]+$/, "");
  return (
    <li>
      {text && <span>{text}</span>}
      <Command text={command} />
    </li>
  );
}

function Command({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  const copy = () => {
    void navigator.clipboard?.writeText(text).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  };
  return (
    <span className="cv-ai-command">
      <code className="cv-mono">{text}</code>
      <button className="cv-icon-btn" onClick={copy} aria-label="Copy command" title="Copy">
        {copied ? <Check size={14} /> : <Copy size={14} />}
      </button>
    </span>
  );
}
