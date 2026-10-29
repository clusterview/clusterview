import { useEffect, useMemo, useRef, useState } from "react";
import { followLogsURL, followServiceLogsURL, getLogs, getServiceLogs, type LogLine, type PodState, type ServiceLogs } from "../api";
import { mockName } from "../mock";
import { MAX_LOG_CHARS, mergeLogLines } from "./log-lines";

const ERROR = /\b(error|err|exception|fatal|panic|traceback|failed|failure|refused|denied|timeout)\b/i;

export function LogsTab({ serviceId, pods, initialPod }: { serviceId: string; pods: PodState[]; initialPod: string | null }) {
  const [podName, setPodName] = useState(initialPod ?? "");
  const [container, setContainer] = useState("");
  const [containers, setContainers] = useState<string[]>([]);
  const [shown, setShown] = useState("");
  const [previous, setPrevious] = useState(false);
  const [follow, setFollow] = useState(true);
  const [filter, setFilter] = useState("");
  const [errorsOnly, setErrorsOnly] = useState(false);
  const [lines, setLines] = useState<LogLine[]>([]);
  const [notices, setNotices] = useState<string[]>([]);
  const [summary, setSummary] = useState<string[]>([]);
  const [connection, setConnection] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const box = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const pod = pods.find((p) => p.name === podName);
  const allPods = podName === "";

  useEffect(() => setPodName(initialPod ?? ""), [initialPod]);

  // A crashing container's useful log is usually the one before its restart.
  useEffect(() => {
    setContainer("");
    setPrevious(!!pod && pod.restarts > 0 && !pod.ready);
  }, [podName]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!allPods && !pod) {
      setLoading(false);
      setNotices(["This pod is no longer available. Select All pods to follow its replacements."]);
      return;
    }
    let cancelled = false;
    let source: EventSource | null = null;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let pending: LogLine[] = [];
    let pendingChars = 0;
    setLoading(true);
    setLines([]);
    setNotices([]);
    setSummary([]);
    setConnection(null);
    const notice = (message: string) => setNotices((old) => [...new Set([...old, message])].slice(-20));
    const append = (line: LogLine) => {
      pending.push(line);
      pendingChars += line.text.length;
      if (pending.length > 5000 || pendingChars > MAX_LOG_CHARS) {
        pending = mergeLogLines([], pending);
        pendingChars = pending.reduce((total, item) => total + item.text.length, 0);
      }
      timer ??= setTimeout(() => {
        const batch = pending;
        pending = [];
        pendingChars = 0;
        timer = undefined;
        if (!cancelled) setLines((old) => mergeLogLines(old, batch));
      }, 100);
    };
    const metadata = (logs: ServiceLogs) => {
      setContainers(logs.containers);
      setSummary(logs.notices);
      setLoading(false);
    };
    if (allPods && follow && !previous && !mockName) {
      // Kubernetes sends history and live lines on the same streams, avoiding
      // a read/follow gap or duplicates when several replicas write together.
      source = new EventSource(followServiceLogsURL(serviceId, container));
      source.onopen = () => {
        pending = [];
        pendingChars = 0;
        setLines([]);
        setNotices([]);
        setConnection(null);
      };
      source.addEventListener("sources", (e) => metadata(JSON.parse((e as MessageEvent<string>).data) as ServiceLogs));
      source.addEventListener("line", (e) => append(JSON.parse((e as MessageEvent<string>).data) as LogLine));
      source.addEventListener("notice", (e) => notice((JSON.parse((e as MessageEvent<string>).data) as { message: string }).message));
      source.addEventListener("end", (e) => {
        notice((JSON.parse((e as MessageEvent<string>).data) as { error?: string }).error ?? "Log stream ended.");
        setLoading(false);
        source?.close();
      });
      source.onerror = () => {
        setLoading(false);
        setConnection("Live log connection interrupted; reconnecting…");
      };
    } else {
      const history = allPods
        ? getServiceLogs(serviceId, container, previous)
        : getLogs(pod!.namespace, pod!.name, container, previous);
      history.then((logs) => {
        if (cancelled) return;
        setLines(mergeLogLines([], logs.lines));
        setContainers(logs.containers);
        if ("sources" in logs) {
          metadata(logs);
        } else {
          setShown(logs.container);
          setNotices(logs.notice ? [logs.notice] : []);
        }
        setLoading(false);
        if (!allPods && follow && !previous && !mockName) {
          source = new EventSource(followLogsURL(pod!.namespace, pod!.name, "container" in logs ? logs.container : container));
          const last = logs.lines.at(-1)?.timestamp ?? "";
          source.addEventListener("line", (e) => {
            const line = JSON.parse((e as MessageEvent<string>).data) as LogLine;
            if (line.timestamp && line.timestamp <= last) return;
            append(line);
          });
          source.addEventListener("end", () => source?.close());
        }
      }).catch((e: unknown) => {
        if (cancelled) return;
        setNotices([e instanceof Error ? e.message : String(e)]);
        setLoading(false);
      });
    }
    return () => {
      cancelled = true;
      source?.close();
      clearTimeout(timer);
    };
  }, [serviceId, allPods, pod?.namespace, pod?.name, container, previous, follow]); // eslint-disable-line react-hooks/exhaustive-deps

  const visible = useMemo(() => {
    const f = filter.toLowerCase();
    return lines.filter((l) => (!errorsOnly || ERROR.test(l.text)) && (!f || `${l.pod ?? ""} ${l.container ?? ""} ${l.text}`.toLowerCase().includes(f)));
  }, [lines, filter, errorsOnly]);
  const errorCount = useMemo(() => lines.filter((l) => ERROR.test(l.text)).length, [lines]);

  useEffect(() => {
    const el = box.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [visible]);

  return (
    <div className="cv-logs">
      <div className="cv-logs-bar">
        <select value={podName} onChange={(e) => setPodName(e.target.value)} aria-label="Pod">
          <option value="">All pods ({pods.length})</option>
          {pods.map((p) => (
            <option key={p.name} value={p.name}>{p.name}{!p.ready ? " (not ready)" : ""}</option>
          ))}
        </select>
        {(allPods || containers.length > 1) && (
          <select value={allPods ? container : shown} onChange={(e) => setContainer(e.target.value)} aria-label="Container">
            {allPods && <option value="">All containers</option>}
            {containers.map((c) => <option key={c}>{c}</option>)}
          </select>
        )}
        <input className="cv-logs-filter" placeholder="Filter" value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="Filter lines" />
      </div>
      <div className="cv-logs-bar cv-logs-toggles">
        <label><input type="checkbox" checked={previous} onChange={(e) => setPrevious(e.target.checked)} /> Before last restart</label>
        <label><input type="checkbox" checked={follow && !previous} disabled={previous} onChange={(e) => setFollow(e.target.checked)} /> Follow</label>
        <label>
          <input type="checkbox" checked={errorsOnly} onChange={(e) => setErrorsOnly(e.target.checked)} /> Errors only
          {errorCount > 0 && <span className="cv-tab-badge">{errorCount}</span>}
        </label>
      </div>
      {allPods && <p className="cv-note cv-logs-help">Combined pod and container logs, ordered by timestamp. Keeps up to 5,000 recent lines.</p>}
      {[...summary, ...notices, ...(connection ? [connection] : [])].map((text) => <p key={text} className="cv-logs-notice">{text}</p>)}
      <div className="cv-logs-box" ref={box} onScroll={(e) => {
        const el = e.currentTarget;
        stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
      }}>
        {loading && <p className="cv-muted">Loading…</p>}
        {!loading && visible.length === 0 && !notices.length && <p className="cv-muted">{pods.length ? "Nothing to show." : "No pods yet."}</p>}
        {visible.map((l, i) => (
          <div key={i} className={`cv-log-line${ERROR.test(l.text) ? " is-error" : ""}`}>
            {l.timestamp && <span className="cv-log-time">{l.timestamp.slice(11, 19)}</span>}
            {allPods && l.pod && <span className="cv-log-source" title={`${l.namespace}/${l.pod} · ${l.container}`}>{l.pod}/{l.container}</span>}
            <span className="cv-log-text">{l.text}</span>
          </div>
        ))}
      </div>
    </div>
  );
}
