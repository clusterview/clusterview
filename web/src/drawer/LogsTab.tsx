import { useEffect, useMemo, useRef, useState } from "react";
import { followLogsURL, getLogs, type LogLine, type PodState } from "../api";
import { mockName } from "../mock";

const ERROR = /\b(error|err|exception|fatal|panic|traceback|failed|failure|refused|denied|timeout)\b/i;

export function LogsTab({ pods, initialPod }: { pods: PodState[]; initialPod: string | null }) {
  const [podName, setPodName] = useState(initialPod ?? pods[0]?.name ?? "");
  const [container, setContainer] = useState("");
  const [containers, setContainers] = useState<string[]>([]);
  const [shown, setShown] = useState("");
  const [previous, setPrevious] = useState(false);
  const [follow, setFollow] = useState(true);
  const [filter, setFilter] = useState("");
  const [errorsOnly, setErrorsOnly] = useState(false);
  const [lines, setLines] = useState<LogLine[]>([]);
  const [notice, setNotice] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const box = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const pod = pods.find((p) => p.name === podName);

  useEffect(() => {
    if (initialPod) setPodName(initialPod);
  }, [initialPod]);

  // A crashing container's useful log is usually the one before its restart.
  useEffect(() => {
    setContainer("");
    setPrevious(!!pod && pod.restarts > 0 && !pod.ready);
  }, [podName]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!pod) return;
    let cancelled = false;
    let source: EventSource | null = null;
    setLoading(true);
    setNotice(null);
    getLogs(pod.namespace, pod.name, container, previous)
      .then((l) => {
        if (cancelled) return;
        setLines(l.lines);
        setContainers(l.containers);
        setShown(l.container);
        setNotice(l.notice ?? null);
        setLoading(false);
        if (follow && !previous && !mockName) {
          source = new EventSource(followLogsURL(pod.namespace, pod.name, l.container));
          const last = l.lines.at(-1)?.timestamp ?? "";
          source.addEventListener("line", (e) => {
            const line = JSON.parse((e as MessageEvent<string>).data) as LogLine;
            if (line.timestamp && line.timestamp <= last) return; // already shown
            setLines((prev) => [...prev.slice(-4999), line]);
          });
          source.addEventListener("end", () => source?.close());
        }
      })
      .catch((e: unknown) => {
        if (cancelled) return;
        setLines([]);
        setNotice(e instanceof Error ? e.message : String(e));
        setLoading(false);
      });
    return () => {
      cancelled = true;
      source?.close();
    };
  }, [pod?.namespace, pod?.name, container, previous, follow]); // eslint-disable-line react-hooks/exhaustive-deps

  const visible = useMemo(() => {
    const f = filter.toLowerCase();
    return lines.filter((l) => (!errorsOnly || ERROR.test(l.text)) && (!f || l.text.toLowerCase().includes(f)));
  }, [lines, filter, errorsOnly]);
  const errorCount = useMemo(() => lines.filter((l) => ERROR.test(l.text)).length, [lines]);

  // Stay at the bottom while following, unless the reader scrolled up.
  useEffect(() => {
    const el = box.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [visible]);

  if (!pods.length) return <p className="cv-empty-note">No pods, so no logs.</p>;

  return (
    <div className="cv-logs">
      <div className="cv-logs-bar">
        <select value={podName} onChange={(e) => setPodName(e.target.value)} aria-label="Pod">
          {pods.map((p) => (
            <option key={p.name} value={p.name}>
              {p.name}
              {!p.ready ? " (not ready)" : ""}
            </option>
          ))}
        </select>
        {containers.length > 1 && (
          <select value={shown} onChange={(e) => setContainer(e.target.value)} aria-label="Container">
            {containers.map((c) => (
              <option key={c}>{c}</option>
            ))}
          </select>
        )}
        <input
          className="cv-logs-filter"
          placeholder="Filter"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          aria-label="Filter lines"
        />
      </div>
      <div className="cv-logs-bar cv-logs-toggles">
        <label>
          <input type="checkbox" checked={previous} onChange={(e) => setPrevious(e.target.checked)} /> Before last restart
        </label>
        <label>
          <input type="checkbox" checked={follow && !previous} disabled={previous} onChange={(e) => setFollow(e.target.checked)} /> Follow
        </label>
        <label>
          <input type="checkbox" checked={errorsOnly} onChange={(e) => setErrorsOnly(e.target.checked)} /> Errors only
          {errorCount > 0 && <span className="cv-tab-badge">{errorCount}</span>}
        </label>
      </div>
      {notice && <p className="cv-logs-notice">{notice}</p>}
      <div
        className="cv-logs-box"
        ref={box}
        onScroll={(e) => {
          const el = e.currentTarget;
          stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
        }}
      >
        {loading && <p className="cv-muted">Loading…</p>}
        {!loading && visible.length === 0 && !notice && <p className="cv-muted">Nothing to show.</p>}
        {visible.map((l, i) => (
          <div key={i} className={`cv-log-line${ERROR.test(l.text) ? " is-error" : ""}`}>
            {l.timestamp && <span className="cv-log-time">{l.timestamp.slice(11, 19)}</span>}
            <span className="cv-log-text">{l.text}</span>
          </div>
        ))}
      </div>
    </div>
  );
}
