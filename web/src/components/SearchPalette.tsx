import { useEffect, useMemo, useRef, useState } from "react";
import { CornerDownLeft, Search } from "lucide-react";
import type { Service, Snapshot } from "../api";
import { CategoryIcon, categoryLabel } from "./CategoryIcon";
import { toneOf } from "./ServiceCard";
import "./search.css";

interface Hit {
  service: Service;
  why?: string; // e.g. "pod payments-api-7c9f…"
  score: number;
}

/** Ranks services against a query: title and name first, then namespace,
 *  category and description, then pod names. */
function search(snap: Snapshot, query: string): Hit[] {
  const q = query.trim().toLowerCase();
  const podsOf = new Map(snap.workloads.map((w) => [w.id, w.pods.map((p) => p.name)]));
  const hits: Hit[] = [];
  for (const s of snap.services) {
    if (!q) {
      // Nothing typed: what needs attention, then the rest.
      hits.push({ service: s, score: s.status === "down" ? 3 : s.status === "degraded" ? 2 : 0 });
      continue;
    }
    const title = s.title.toLowerCase();
    const name = s.name.toLowerCase();
    let score = 0;
    let why: string | undefined;
    if (title.startsWith(q) || name.startsWith(q)) score = 10;
    else if (title.includes(q) || name.includes(q)) score = 7;
    else if (s.namespace.toLowerCase().includes(q)) score = 4;
    else if (categoryLabel[s.category].toLowerCase().includes(q) || (s.description ?? "").toLowerCase().includes(q)) score = 3;
    else {
      const pod = s.workloads.flatMap((w) => podsOf.get(w) ?? []).find((p) => p.toLowerCase().includes(q));
      if (pod) {
        score = 2;
        why = `pod ${pod}`;
      }
    }
    if (score) hits.push({ service: s, score: score + (s.status === "down" ? 0.5 : 0), why });
  }
  return hits.sort((a, b) => b.score - a.score || a.service.title.localeCompare(b.service.title)).slice(0, 12);
}

export function SearchPalette({
  snapshot,
  onPick,
  onClose,
}: {
  snapshot: Snapshot;
  onPick: (id: string) => void;
  onClose: () => void;
}) {
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const hits = useMemo(() => search(snapshot, query), [snapshot, query]);

  useEffect(() => input.current?.focus(), []);
  useEffect(() => setActive(0), [query]);

  const pick = (h?: Hit) => {
    if (!h) return;
    onPick(h.service.id);
    onClose();
  };

  return (
    <div className="cv-dialog-backdrop is-top" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="cv-search" role="dialog" aria-label="Find a service">
        <div className="cv-search-field">
          <Search size={18} />
          <input
            ref={input}
            value={query}
            placeholder="Find a service, namespace or pod"
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown") {
                e.preventDefault();
                setActive((a) => Math.min(a + 1, hits.length - 1));
              } else if (e.key === "ArrowUp") {
                e.preventDefault();
                setActive((a) => Math.max(a - 1, 0));
              } else if (e.key === "Enter") pick(hits[active]);
              else if (e.key === "Escape") onClose();
            }}
            aria-label="Search"
            role="combobox"
            aria-expanded
            aria-controls="cv-search-results"
            aria-activedescendant={hits[active] ? `cv-hit-${active}` : undefined}
          />
          <kbd>esc</kbd>
        </div>
        <ul className="cv-search-results" id="cv-search-results" role="listbox">
          {hits.map((h, i) => {
            const s = h.service;
            const tone = toneOf(s);
            return (
              <li
                key={s.id}
                id={`cv-hit-${i}`}
                role="option"
                aria-selected={i === active}
                className={i === active ? "is-active" : ""}
                onMouseEnter={() => setActive(i)}
                onMouseDown={(e) => {
                  e.preventDefault();
                  pick(h);
                }}
              >
                <CategoryIcon category={s.category} size={28} />
                <span className="cv-hit-main">
                  <strong>{s.title}</strong>
                  <span className="cv-muted">
                    {s.title !== s.name && <span className="cv-mono">{s.name} · </span>}
                    {s.namespace}
                    {h.why && <> · {h.why}</>}
                  </span>
                </span>
                {s.problems[0] && (tone === "down" || tone === "degraded") && (
                  <span className={`cv-card-problem is-${s.problems[0].severity}`}>{s.problems[0].title}</span>
                )}
                <span className={`cv-status is-${tone}`} />
                {i === active && <CornerDownLeft size={14} className="cv-muted" />}
              </li>
            );
          })}
          {!hits.length && <li className="cv-search-empty">Nothing matches “{query}”.</li>}
        </ul>
      </div>
    </div>
  );
}
