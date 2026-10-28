import { useEffect, useRef, useState } from "react";
import { mockName } from "./mock";

// Calls load() now and again whenever the cluster changes, as announced on
// /api/v1/events. Returns the latest result and whether the stream is live.
export function useLive<T>(load: () => Promise<T>, enabled = true) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [live, setLive] = useState(false);
  const loadRef = useRef(load);
  loadRef.current = load;

  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    let inFlight = false;
    let again = false;

    const refresh = async () => {
      if (inFlight) {
        again = true;
        return;
      }
      inFlight = true;
      try {
        const next = await loadRef.current();
        if (!cancelled) {
          setData(next);
          setError(null);
        }
      } catch (e) {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e));
      } finally {
        inFlight = false;
        if (again && !cancelled) {
          again = false;
          void refresh();
        }
      }
    };

    void refresh();
    if (mockName) {
      setLive(true);
      return () => {
        cancelled = true;
      };
    }
    const events = new EventSource("/api/v1/events");
    events.addEventListener("version", () => void refresh());
    events.onopen = () => setLive(true);
    events.onerror = () => setLive(false);
    return () => {
      cancelled = true;
      events.close();
    };
  }, [enabled]);

  return { data, error, live };
}
