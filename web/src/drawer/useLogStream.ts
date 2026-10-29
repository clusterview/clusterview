import { useEffect, useState } from "react";
import { followLogsURL, followServiceLogsURL, getLogs, getServiceLogs, type LogLine, type PodState, type ServiceLogs } from "../api";
import { mockName } from "../mock";
import { MAX_LOG_CHARS, mergeLogLines } from "./log-lines";

export type LogStatus = "loading" | "live" | "history" | "reconnecting" | "ended" | "error" | "demo";

/** Own the HTTP/SSE lifetime here so display controls never own subscriptions. */
export function useLogStream({ serviceId, pod, allPods, container, previous, follow, revision }: {
  serviceId: string; pod?: PodState; allPods: boolean; container: string; previous: boolean; follow: boolean; revision: number;
}) {
  const [lines, setLines] = useState<LogLine[]>([]);
  const [containers, setContainers] = useState<string[]>([]);
  const [shown, setShown] = useState("");
  const [notices, setNotices] = useState<string[]>([]);
  const [summary, setSummary] = useState<string[]>([]);
  const [connection, setConnection] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [status, setStatus] = useState<LogStatus>("loading");
  const [sourceCount, setSourceCount] = useState(0);
  useEffect(() => {
    if (!allPods && !pod) {
      setLoading(false);
      setStatus("ended");
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
    setStatus("loading");
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
      setSourceCount(logs.sources.length);
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
        setStatus("live");
      };
      source.addEventListener("sources", (e) => metadata(JSON.parse((e as MessageEvent<string>).data) as ServiceLogs));
      source.addEventListener("line", (e) => append(JSON.parse((e as MessageEvent<string>).data) as LogLine));
      source.addEventListener("notice", (e) => notice((JSON.parse((e as MessageEvent<string>).data) as { message: string }).message));
      source.addEventListener("end", (e) => {
        notice((JSON.parse((e as MessageEvent<string>).data) as { error?: string }).error ?? "Log stream ended.");
        setLoading(false);
        setStatus("ended");
        source?.close();
      });
      source.onerror = () => {
        setLoading(false);
        setConnection("Live log connection interrupted; reconnecting…");
        setStatus("reconnecting");
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
        setStatus(mockName ? "demo" : "history");
        if (!allPods && follow && !previous && !mockName) {
          source = new EventSource(followLogsURL(pod!.namespace, pod!.name, "container" in logs ? logs.container : container));
          const last = logs.lines.at(-1)?.timestamp ?? "";
          source.onopen = () => { setStatus("live"); setConnection(null); };
          source.onerror = () => { setStatus("reconnecting"); setConnection("Live log connection interrupted; reconnecting…"); };
          source.addEventListener("line", (e) => {
            const line = JSON.parse((e as MessageEvent<string>).data) as LogLine;
            if (line.timestamp && line.timestamp <= last) return;
            append(line);
          });
          source.addEventListener("end", () => { setStatus("ended"); source?.close(); });
        }
      }).catch((e: unknown) => {
        if (cancelled) return;
        setStatus("error");
        setNotices([e instanceof Error ? e.message : String(e)]);
        setLoading(false);
      });
    }
    return () => {
      cancelled = true;
      source?.close();
      clearTimeout(timer);
    };
  }, [serviceId, allPods, pod?.namespace, pod?.name, container, previous, follow, revision]); // eslint-disable-line react-hooks/exhaustive-deps

  return { lines, containers, shown, notices: [...summary, ...notices, ...(connection ? [connection] : [])], loading, status, sourceCount: allPods ? sourceCount : 1 };
}
