import { useMemo, useState } from "react";
import { Copy, RotateCw, Search, WrapText, X } from "lucide-react";
import type { PodState } from "../api";
import { useToast } from "../components/ui";
import { formatLogLines } from "./log-lines";
import { LogOutput } from "./LogOutput";
import { useLogStream, type LogStatus } from "./useLogStream";

const ERROR = /\b(error|err|exception|fatal|panic|traceback|failed|failure|refused|denied|timeout)\b/i;
const statusLabels: Record<LogStatus, string> = { loading: "Loading", live: "Following", history: "History", reconnecting: "Reconnecting", ended: "Stream ended", error: "Unavailable", demo: "Demo logs" };

export function LogsTab({ serviceId, pods, initialPod }: { serviceId: string; pods: PodState[]; initialPod: string | null }) {
  const [podName, setPodName] = useState(initialPod ?? "");
  const [container, setContainer] = useState("");
  const initial = pods.find((pod) => pod.name === initialPod);
  const [previous, setPrevious] = useState(!!initial && initial.restarts > 0 && !initial.ready);
  const [follow, setFollow] = useState(true);
  const [filter, setFilter] = useState("");
  const [errorsOnly, setErrorsOnly] = useState(false);
  const [wrap, setWrap] = useState(true);
  const [revision, setRevision] = useState(0);
  const toast = useToast();
  const pod = pods.find((item) => item.name === podName);
  const allPods = podName === "";
  const { lines, containers, shown, notices, loading, status, sourceCount } = useLogStream({ serviceId, pod, allPods, container, previous, follow, revision });
  const visible = useMemo(() => {
    const query = filter.trim().toLowerCase();
    return lines.filter((line) => (!errorsOnly || ERROR.test(line.text)) && (!query || `${line.pod ?? ""} ${line.container ?? ""} ${line.text}`.toLowerCase().includes(query)));
  }, [lines, filter, errorsOnly]);
  const errorCount = lines.filter((line) => ERROR.test(line.text)).length;
  const selectPod = (name: string) => {
    const next = pods.find((item) => item.name === name);
    setPodName(name);
    setContainer("");
    setPrevious(!!next && next.restarts > 0 && !next.ready);
  };
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(formatLogLines(visible));
      toast({ ok: true, title: "Visible logs copied" });
    } catch {
      toast({ ok: false, title: "Couldn’t copy logs", detail: "Allow clipboard access or select and copy the log text." });
    }
  };
  return <div className="cv-logs">
    <div className="cv-logs-selectors">
      <label>Pods
        <select value={podName} onChange={(event) => selectPod(event.target.value)} aria-label="Pod">
          <option value="">All pods ({pods.length})</option>
          {podName && !pod && <option value={podName}>{podName} (removed)</option>}
          {pods.map((item) => <option key={item.name} value={item.name}>{item.name}{!item.ready ? " (not ready)" : ""}</option>)}
        </select>
      </label>
      {(allPods || containers.length > 1) && <label>Container
        <select value={allPods ? container : container || shown} onChange={(event) => setContainer(event.target.value)} aria-label="Container">
          {allPods && <option value="">All containers</option>}
          {containers.map((name) => <option key={name}>{name}</option>)}
        </select>
      </label>}
    </div>
    <div className="cv-logs-search">
      <Search size={14} aria-hidden="true" />
      <input placeholder="Filter message, pod or container" value={filter} onChange={(event) => setFilter(event.target.value)} aria-label="Filter lines" />
      {filter && <button className="cv-icon-btn" onClick={() => setFilter("")} aria-label="Clear log filter"><X size={14} /></button>}
    </div>
    <div className="cv-logs-toggles">
      <label><input type="checkbox" checked={previous} onChange={(event) => setPrevious(event.target.checked)} /> Before last restart</label>
      <label><input type="checkbox" checked={follow && !previous} disabled={previous} onChange={(event) => setFollow(event.target.checked)} /> Follow</label>
      <label><input type="checkbox" checked={errorsOnly} onChange={(event) => setErrorsOnly(event.target.checked)} /> Errors only
        {errorCount > 0 && <span className="cv-tab-badge">{errorCount}</span>}
      </label>
    </div>
    <div className="cv-logs-tools">
      <span className={`cv-log-status is-${status}`} role="status"><span aria-hidden="true" />{status === "live" && allPods && sourceCount === 0 ? "Waiting for pods" : statusLabels[status]}</span>
      <span className="cv-logs-count">{visible.length.toLocaleString()} / {lines.length.toLocaleString()} lines{allPods ? ` · ${sourceCount} containers` : ""}</span>
      <div className="cv-logs-actions">
        <button className="cv-icon-btn" aria-label="Wrap log lines" aria-pressed={wrap} title="Wrap log lines" onClick={() => setWrap(!wrap)}><WrapText size={16} /></button>
        <button className="cv-icon-btn" aria-label="Reload logs" title="Reload logs and reconnect" disabled={loading} onClick={() => setRevision(revision + 1)}><RotateCw size={15} /></button>
        <button className="cv-icon-btn" aria-label="Copy visible logs" title="Copy visible logs" disabled={!visible.length} onClick={() => void copy()}><Copy size={15} /></button>
      </div>
    </div>
    {notices.length > 0 && <div className="cv-logs-notices">{notices.map((notice) => <p key={notice} className="cv-logs-notice">{notice}</p>)}</div>}
    <LogOutput key={`${podName}/${container}/${previous}`} lines={visible} allPods={allPods} wrap={wrap} loading={loading} errorPattern={ERROR}
      empty={filter || errorsOnly ? "No lines match these filters." : pods.length ? "No log lines available." : "No pods yet."}
      onClearFilters={filter || errorsOnly ? () => { setFilter(""); setErrorsOnly(false); } : undefined} />
    <p className="cv-note cv-logs-help">{allPods ? "Combined logs, ordered by timestamp." : "Pod logs, ordered by timestamp."} Keeps up to 5,000 recent lines.</p>
  </div>;
}
