import { useEffect, useMemo, useState } from "react";
import { AlertTriangle, CheckCircle2, LayoutGrid, List, Monitor, Moon, Search, Server, Sun } from "lucide-react";
import {
  getActionsConfig,
  getAI,
  getCluster,
  getMe,
  getSnapshot,
  getVersion,
  type ActionsConfig,
  type AIInfo,
  type ClusterInfo,
  type Me,
  type VersionInfo,
} from "./api";
import { useLive } from "./useLive";
import { useTheme } from "./useTheme";
import { ServiceMap } from "./map/ServiceMap";
import { needsAttention } from "./map/graph";
import { ListView } from "./views/ListView";
import { NodesView } from "./views/NodesView";
import { ServiceDrawer } from "./drawer/ServiceDrawer";
import { SearchPalette } from "./components/SearchPalette";
import "./shell.css";

type View = "map" | "list" | "nodes";

const isMac = /Mac|iPhone|iPad/.test(navigator.platform);

export function App() {
  const params = new URLSearchParams(window.location.search);
  const [cluster, setCluster] = useState<ClusterInfo | null>(null);
  const [version, setVersion] = useState<VersionInfo | null>(null);
  const [me, setMe] = useState<Me | null>(null);
  const [actions, setActions] = useState<ActionsConfig | null>(null);
  const [ai, setAI] = useState<AIInfo | null>(null);
  const [view, setView] = useState<View>(() => (["list", "nodes"].includes(params.get("view") ?? "") ? (params.get("view") as View) : "map"));
  // ?select=<service id> opens a service; handy for links and screenshots.
  const [selected, setSelected] = useState<string | null>(() => params.get("select"));
  const [showSystem, setShowSystem] = useState(false);
  const [searching, setSearching] = useState(false);
  const { data: snapshot, live } = useLive(getSnapshot);
  const { theme, next: nextTheme } = useTheme();
  const ThemeIcon = theme === "light" ? Sun : theme === "dark" ? Moon : Monitor;

  useEffect(() => {
    getCluster().then(setCluster, (e: unknown) => setCluster({ connected: false, error: String(e) }));
    getVersion().then(setVersion, () => undefined);
    getMe().then(setMe, () => undefined);
    getActionsConfig().then(setActions, () => undefined);
    getAI().then(setAI, () => undefined);
  }, []);

  // ⌘K / Ctrl+K, or "/" when not typing, opens search.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const typing = e.target instanceof HTMLElement && /^(INPUT|TEXTAREA|SELECT)$/.test(e.target.tagName);
      if ((e.key === "k" && (e.metaKey || e.ctrlKey)) || (e.key === "/" && !typing)) {
        e.preventDefault();
        setSearching(true);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const trouble = useMemo(() => snapshot?.services.filter(needsAttention) ?? [], [snapshot]);
  const selectedService = snapshot?.services.find((s) => s.id === selected) ?? null;

  // Picking a service from search or the nodes view shows it where it lives.
  const goTo = (id: string) => {
    setSelected(id);
    if (view === "nodes") setView("map");
  };

  return (
    <div className="cv-app">
      <header className="cv-top">
        <div className="cv-brand">
          <span className="cv-logo" aria-hidden />
          <strong>clusterview</strong>
          {cluster?.connected && (
            <span className="cv-cluster">
              {cluster.context ?? "in-cluster"} <span className="cv-cluster-version">Kubernetes {cluster.serverVersion}</span>
            </span>
          )}
        </div>

        <div className="cv-top-center">
          {snapshot && (
            <button
              className={`cv-health ${trouble.length ? "is-bad" : "is-ok"}`}
              onClick={() => trouble[0] && goTo(trouble[0].id)}
              disabled={!trouble.length}
            >
              {trouble.length ? <AlertTriangle size={15} /> : <CheckCircle2 size={15} />}
              {trouble.length
                ? `${trouble.length} service${trouble.length === 1 ? " needs" : "s need"} attention`
                : `All ${snapshot.summary.services} services healthy`}
            </button>
          )}
          {snapshot && (
            <button aria-label="Find a service" className="cv-search-btn" onClick={() => setSearching(true)}>
              <Search size={14} />
              <span>Find a service</span>
              <kbd>{isMac ? "⌘K" : "Ctrl K"}</kbd>
            </button>
          )}
        </div>

        <div className="cv-top-right">
          <nav className="cv-seg" aria-label="View">
            {(
              [
                ["map", LayoutGrid, "Map"],
                ["list", List, "List"],
                ["nodes", Server, "Nodes"],
              ] as const
            ).map(([v, Icon, label]) => (
              <button key={v} aria-pressed={view === v} onClick={() => setView(v)}>
                <Icon size={14} /> {label}
              </button>
            ))}
          </nav>
          <span className={`cv-live${live ? " is-on" : ""}`} title={live ? "Updating live" : "Reconnecting"}>
            <span className="cv-live-dot" /> {live ? "Live" : "Offline"}
          </span>
          <button className="cv-icon-btn" onClick={nextTheme} title={`Theme: ${theme}`} aria-label={`Theme: ${theme}`}>
            <ThemeIcon size={16} />
          </button>
          {me && me.mode !== "none" && (
            <span className="cv-user" title={`${me.name}, ${me.role === "operator" ? "can act" : "view only"}`}>
              {initials(me.name)}
            </span>
          )}
          {(me?.mode === "oidc" || me?.mode === "password") && (
            <a className="cv-signout" href="auth/logout">
              Sign out
            </a>
          )}
          <span className="cv-version">{version?.version}</span>
        </div>
      </header>

      <main className="cv-main">
        {cluster && !cluster.connected ? (
          <div className="cv-empty">
            <p className="cv-empty-title">Can't reach the cluster</p>
            <p className="cv-mono cv-empty-detail">{cluster.error}</p>
          </div>
        ) : !snapshot ? (
          <div className="cv-empty">
            <p className="cv-empty-title">Reading the cluster…</p>
          </div>
        ) : (
          <>
            {view === "map" && <ServiceMap snapshot={snapshot} showSystem={showSystem} selectedId={selected} onSelect={setSelected} />}
            {view === "list" && (
              <ListView snapshot={snapshot} showSystem={showSystem} onShowSystem={setShowSystem} selectedId={selected} onSelect={setSelected} />
            )}
            {view === "nodes" && <NodesView snapshot={snapshot} onSelect={goTo} />}
            {view === "map" && (
              <label className="cv-system-toggle">
                <input type="checkbox" checked={showSystem} onChange={(e) => setShowSystem(e.target.checked)} />
                Show cluster services
              </label>
            )}
            {selectedService && view !== "nodes" && (
              <ServiceDrawer
                key={selectedService.id}
                service={selectedService}
                snapshot={snapshot}
                me={me}
                actions={actions}
                ai={ai}
                onClose={() => setSelected(null)}
                onSelect={setSelected}
              />
            )}
          </>
        )}
      </main>

      {searching && snapshot && <SearchPalette snapshot={snapshot} onPick={goTo} onClose={() => setSearching(false)} />}
    </div>
  );
}

function initials(name: string) {
  const base = name.split("@")[0];
  const parts = base.split(/[.\-_ ]+/).filter(Boolean);
  return ((parts[0]?.[0] ?? "") + (parts[1]?.[0] ?? "")).toUpperCase() || "?";
}
