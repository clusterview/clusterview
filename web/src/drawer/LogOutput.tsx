import { useEffect, useRef, useState } from "react";
import { ArrowDown } from "lucide-react";
import type { LogLine } from "../api";

export function LogOutput({ lines, allPods, wrap, loading, errorPattern, empty, onClearFilters }: {
  lines: LogLine[]; allPods: boolean; wrap: boolean; loading: boolean; errorPattern: RegExp; empty: string; onClearFilters?: () => void;
}) {
  const box = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const [atBottom, setAtBottom] = useState(true);
  useEffect(() => {
    if (box.current && stick.current) box.current.scrollTop = box.current.scrollHeight;
  }, [lines]);
  const jump = () => {
    stick.current = true;
    setAtBottom(true);
    if (box.current) box.current.scrollTop = box.current.scrollHeight;
  };
  return <div className="cv-logs-output">
    <div className={`cv-logs-box${allPods ? " has-sources" : ""}${wrap ? " is-wrapped" : ""}`} ref={box} role="region" aria-label="Log output" tabIndex={0}
      onScroll={(event) => {
        const element = event.currentTarget;
        stick.current = element.scrollHeight - element.scrollTop - element.clientHeight < 40;
        setAtBottom(stick.current);
      }}>
      {loading && <p className="cv-log-empty" role="status">Loading logs…</p>}
      {!loading && lines.length === 0 && <div className="cv-log-empty"><p>{empty}</p>{onClearFilters && <button className="cv-btn is-secondary is-sm" onClick={onClearFilters}>Clear filters</button>}</div>}
      {lines.map((line, index) => <div key={index} className={`cv-log-line${errorPattern.test(line.text) ? " is-error" : ""}`}>
        <span className="cv-log-time" title={line.timestamp}>{line.timestamp?.slice(11, 19) ?? "—"}</span>
        {allPods && <span className="cv-log-source" title={`${line.namespace}/${line.pod} · ${line.container}`}>
          <span>{line.pod && line.pod.length > 22 ? `${line.pod.slice(0, 10)}…${line.pod.slice(-9)}` : line.pod}</span>
          <span className="cv-log-container">{line.container}</span>
        </span>}
        <span className="cv-log-text">{line.text}</span>
      </div>)}
    </div>
    {!atBottom && lines.length > 0 && <button className="cv-btn is-secondary is-sm cv-log-jump" onClick={jump}><ArrowDown size={14} /> Jump to latest</button>}
  </div>;
}
