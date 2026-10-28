import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { CheckCircle2, X, XCircle } from "lucide-react";
import type { Status } from "../api";
import "./ui.css";

/* Buttons */

type ButtonProps = React.ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "primary" | "secondary" | "danger" | "ghost";
  size?: "sm" | "md";
  icon?: ReactNode;
};

export function Button({ variant = "secondary", size = "md", icon, children, className = "", ...rest }: ButtonProps) {
  return (
    <button className={`cv-btn is-${variant} is-${size} ${className}`} {...rest}>
      {icon}
      {children}
    </button>
  );
}

/* Status pill: "Down", "Healthy"… */

const statusWords: Record<Status | "warning", string> = {
  healthy: "Healthy",
  idle: "Idle",
  degraded: "Partly running",
  down: "Down",
  unknown: "Unknown",
  warning: "Needs a look",
};

export function StatusPill({ status }: { status: Status | "warning" }) {
  return (
    <span className={`cv-pill is-${status}`}>
      <span className="cv-pill-dot" />
      {statusWords[status]}
    </span>
  );
}

/* Tabs */

export function Tabs<T extends string>({
  tabs,
  value,
  onChange,
}: {
  tabs: { id: T; label: string; badge?: number }[];
  value: T;
  onChange: (t: T) => void;
}) {
  return (
    <div className="cv-tabs" role="tablist">
      {tabs.map((t) => (
        <button key={t.id} role="tab" aria-selected={value === t.id} onClick={() => onChange(t.id)}>
          {t.label}
          {!!t.badge && <span className="cv-tab-badge">{t.badge}</span>}
        </button>
      ))}
    </div>
  );
}

/* Dialog */

export function Dialog({
  title,
  children,
  onClose,
  footer,
}: {
  title: string;
  children: ReactNode;
  onClose: () => void;
  footer: ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    ref.current?.querySelector<HTMLElement>("input, button.is-primary, button.is-danger")?.focus();
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div className="cv-dialog-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="cv-dialog" role="dialog" aria-modal="true" aria-label={title} ref={ref}>
        <header className="cv-dialog-head">
          <h2>{title}</h2>
          <button className="cv-icon-btn" onClick={onClose} aria-label="Close">
            <X size={16} />
          </button>
        </header>
        <div className="cv-dialog-body">{children}</div>
        <footer className="cv-dialog-foot">{footer}</footer>
      </div>
    </div>
  );
}

/* Toasts */

interface Toast {
  id: number;
  ok: boolean;
  title: string;
  detail?: string;
}

const ToastContext = createContext<(t: Omit<Toast, "id">) => void>(() => undefined);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const push = useCallback((t: Omit<Toast, "id">) => {
    const id = Date.now() + Math.random();
    setToasts((all) => [...all, { ...t, id }]);
    setTimeout(() => setToasts((all) => all.filter((x) => x.id !== id)), t.ok ? 4000 : 7000);
  }, []);
  return (
    <ToastContext.Provider value={push}>
      {children}
      <div className="cv-toasts" aria-live="polite">
        {toasts.map((t) => (
          <div key={t.id} className={`cv-toast ${t.ok ? "is-ok" : "is-bad"}`}>
            {t.ok ? <CheckCircle2 size={18} /> : <XCircle size={18} />}
            <div>
              <strong>{t.title}</strong>
              {t.detail && <p>{t.detail}</p>}
            </div>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}

export const useToast = () => useContext(ToastContext);
