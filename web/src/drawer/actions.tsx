import { useState } from "react";
import { ChevronsUpDown, Minus, Play, Plus, RotateCw } from "lucide-react";
import { restartPod, restartWorkload, runCronJob, scaleWorkload, type ActionsConfig, type AuditEntry, type Me, type PodState, type Service, type Workload } from "../api";
import { Button, Dialog, useToast } from "../components/ui";

/** Whether this person may act on this namespace: actions on, operator role,
 *  namespace allowed. The server checks all of it again. */
export function canActIn(me: Me | null, cfg: ActionsConfig | null, namespace: string) {
  return (
    !!cfg?.enabled &&
    me?.role === "operator" &&
    (cfg.namespaces.length === 0 || cfg.namespaces.includes(namespace))
  );
}

function useRun() {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const run = async (fn: () => Promise<AuditEntry>, done: () => void) => {
    setBusy(true);
    try {
      const e = await fn();
      toast({ ok: true, title: e.action, detail: e.detail });
      done();
    } catch (err) {
      toast({ ok: false, title: "Not done", detail: err instanceof Error ? err.message : String(err) });
    } finally {
      setBusy(false);
    }
  };
  return { busy, run };
}

type Pending = { kind: "restart" | "scale" | "run"; w: Workload } | null;

export function ServiceActions({ service, workloads, maxReplicas }: { service: Service; workloads: Workload[]; maxReplicas: number }) {
  const [pending, setPending] = useState<Pending>(null);
  const { busy, run } = useRun();
  const restartable = workloads.find((w) => w.kind === "Deployment" || w.kind === "StatefulSet" || w.kind === "DaemonSet");
  // An operator or autoscaler owns the count of what it runs; scaling by hand would be undone.
  const scalable = workloads.find((w) => (w.kind === "Deployment" || w.kind === "StatefulSet") && !w.autoscaling && !w.owner);
  const cron = workloads.find((w) => w.kind === "CronJob");
  const close = () => setPending(null);

  return (
    <div className="cv-actions">
      {restartable && (
        <Button size="sm" icon={<RotateCw size={14} />} onClick={() => setPending({ kind: "restart", w: restartable })}>
          Restart
        </Button>
      )}
      {scalable && (
        <Button size="sm" icon={<ChevronsUpDown size={14} />} onClick={() => setPending({ kind: "scale", w: scalable })}>
          Scale
        </Button>
      )}
      {cron && (
        <Button size="sm" icon={<Play size={14} />} onClick={() => setPending({ kind: "run", w: cron })}>
          Run now
        </Button>
      )}

      {pending?.kind === "restart" && (
        <Dialog
          title={`Restart ${service.title}?`}
          onClose={close}
          footer={
            <>
              <Button onClick={close}>Cancel</Button>
              <Button variant="primary" disabled={busy} onClick={() => run(() => restartWorkload(pending.w.kind, pending.w.namespace, pending.w.name), close)}>
                Restart
              </Button>
            </>
          }
        >
          <p>
            Kubernetes replaces its {pending.w.desired === 1 ? "pod" : `${pending.w.desired} pods`} a few at a time, so it keeps serving while it restarts.
          </p>
          <p className="cv-muted">
            Same as <span className="cv-mono">kubectl rollout restart {pending.w.kind.toLowerCase()}/{pending.w.name}</span>. Recorded in the audit log.
          </p>
        </Dialog>
      )}
      {pending?.kind === "scale" && <ScaleDialog w={pending.w} title={service.title} max={maxReplicas} busy={busy} run={run} onClose={close} />}
      {pending?.kind === "run" && (
        <Dialog
          title={`Run ${service.title} now?`}
          onClose={close}
          footer={
            <>
              <Button onClick={close}>Cancel</Button>
              <Button variant="primary" disabled={busy} onClick={() => run(() => runCronJob(pending.w.namespace, pending.w.name), close)}>
                Run now
              </Button>
            </>
          }
        >
          <p>Starts one run right away, outside its schedule. Its next scheduled run still happens as usual.</p>
        </Dialog>
      )}
    </div>
  );
}

function ScaleDialog({
  w,
  title,
  max,
  busy,
  run,
  onClose,
}: {
  w: Workload;
  title: string;
  max: number;
  busy: boolean;
  run: ReturnType<typeof useRun>["run"];
  onClose: () => void;
}) {
  const [n, setN] = useState(w.desired);
  const clamp = (v: number) => Math.max(0, Math.min(max, Number.isFinite(v) ? Math.round(v) : 0));
  return (
    <Dialog
      title={`Scale ${title}`}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            variant={n === 0 ? "danger" : "primary"}
            disabled={busy || n === w.desired}
            onClick={() => run(() => scaleWorkload(w.kind, w.namespace, w.name, n), onClose)}
          >
            {n === 0 ? "Scale to zero" : `Set ${n} replica${n === 1 ? "" : "s"}`}
          </Button>
        </>
      }
    >
      <p>How many copies should run? Now: {w.desired}.</p>
      <div className="cv-stepper">
        <button onClick={() => setN((v) => clamp(v - 1))} aria-label="Fewer">
          <Minus size={16} />
        </button>
        <input type="number" min={0} max={max} value={n} onChange={(e) => setN(clamp(Number(e.target.value)))} aria-label="Replicas" />
        <button onClick={() => setN((v) => clamp(v + 1))} aria-label="More">
          <Plus size={16} />
        </button>
      </div>
      {n === 0 && <p className="cv-warn-text">At zero nothing runs, and anything that calls it will fail.</p>}
    </Dialog>
  );
}

export function PodRestartButton({ pod }: { pod: PodState }) {
  const [open, setOpen] = useState(false);
  const { busy, run } = useRun();
  return (
    <>
      <button className="cv-btn is-ghost is-sm" onClick={() => setOpen(true)} title="Restart this pod">
        <RotateCw size={13} />
      </button>
      {open && (
        <Dialog
          title="Restart this pod?"
          onClose={() => setOpen(false)}
          footer={
            <>
              <Button onClick={() => setOpen(false)}>Cancel</Button>
              <Button variant="primary" disabled={busy} onClick={() => run(() => restartPod(pod.namespace, pod.name), () => setOpen(false))}>
                Restart pod
              </Button>
            </>
          }
        >
          <p>
            <span className="cv-mono">{pod.name}</span> is stopped and a fresh copy takes its place.
          </p>
        </Dialog>
      )}
    </>
  );
}
