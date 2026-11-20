// Resource-health examples layered over the shop fixture, with current times.
import demo from './demo.json';
import type { HealthState, ResourceReading, ServiceHealth, Snapshot } from '../api';

const snapshot = structuredClone(demo) as unknown as Snapshot;
const now = Date.now();
const observedAt = new Date(now).toISOString();
const GiB = 1024 ** 3;

function reading(percent: number, unit: ResourceReading['unit'], subject: string, state: HealthState = 'healthy', reason = 'Within configured thresholds'): ResourceReading {
  const limit = unit === 'millicores' ? 1000 : unit === 'percent' ? 100 : GiB;
  return {
    state, usage: percent / 100 * limit, limit, percent, unit, subject, reason,
    source: unit === 'percent' ? 'prometheus' : 'metrics-server', observedAt,
    history: Array.from({ length: 11 }, (_, i) => ({
      time: new Date(now - (10 - i) * 60_000).toISOString(), value: Math.max(0, percent - (10 - i) * 2),
    })),
  };
}

for (const service of snapshot.services) {
  const workloads = snapshot.workloads.filter((w) => service.workloads.includes(w.id));
  const pods = workloads.filter((w) => w.kind !== 'Job' && w.kind !== 'CronJob').flatMap((w) => w.pods);
  if (!pods.length) continue;
  service.status = 'healthy';
  service.ready = service.desired;
  service.problems = [];
  const subject = `${pods[0].name}/app`;
  const h: ServiceHealth = {
    state: 'healthy', cpu: reading(24, 'millicores', subject),
    memory: reading(46, 'bytes', subject), checks: {},
  };
  service.health = h;
  if (service.name === 'payments-api') {
    h.state = 'warning';
    h.memory = reading(90, 'bytes', subject, 'warning', 'Approaching its limit');
    service.problems = [{ kind: 'resource-memory', severity: 'warning', title: 'Memory close to its limit', detail: '90% of the container memory limit.' }];
  } else if (service.name === 'order-worker') {
    h.state = 'critical';
    h.cpu = reading(98, 'millicores', subject, 'critical', 'CPU is being throttled');
    service.problems = [{ kind: 'resource-cpu', severity: 'critical', title: 'CPU is being throttled', detail: '60% of CPU periods are throttled.' }];
  } else if (service.name === 'orders-db') {
    h.state = 'warning';
    h.disk = { ...reading(88, 'bytes', 'data/orders-data', 'warning', 'Approaching its capacity'), source: 'prometheus' };
    service.problems = [{ kind: 'resource-disk', severity: 'warning', title: 'Data volume filling up', detail: '88% of measured filesystem capacity.' }];
  } else if (service.name === 'cache') {
    h.state = 'unknown';
    h.cpu = { state: 'unknown', unit: 'millicores', source: 'metrics-server', reason: 'CPU metrics missing or stale' };
    h.memory = { state: 'unknown', unit: 'bytes', source: 'metrics-server', reason: 'Memory metrics missing or stale' };
  } else if (service.category === 'queue') {
    h.state = 'critical';
    h.queues = [{
      serviceId: service.id, job: 'rabbitmq', vhost: '/', queue: 'orders',
      warningMessages: 1000, criticalMessages: 10000, ackMode: 'manual', allowNoConsumers: false,
      state: 'critical', reason: 'Messages waiting with no consumers',
      ready: 12400, unacked: 0, consumers: 0, publishRate: 300, processRate: 0, observedAt,
    }];
    service.problems = [{ kind: 'queue:orders', severity: 'critical', title: 'Orders: no consumers', detail: '12,400 messages waiting; incoming 300/s, acknowledged 0/s.' }];
  }
  for (const w of workloads) {
    w.status = 'healthy';
    w.ready = w.desired;
    w.problems = [];
    for (const pod of w.pods) {
      pod.ready = true;
      pod.stuck = false;
      pod.reason = '';
      pod.restarts = 0;
      pod.readyContainers = '1/1';
      pod.resources = [{
        name: 'app', cpu: { ...h.cpu, subject: `${pod.name}/app` }, memory: { ...h.memory, subject: `${pod.name}/app` },
        ...(service.name === 'order-worker' ? { throttling: reading(60, 'percent', `${pod.name}/app`, 'critical', 'CPU periods throttled') } : {}),
      }];
    }
  }
}
snapshot.summary.needAttention = snapshot.services.filter((s) => s.problems.length).length;
snapshot.summary.readyPods = snapshot.workloads.flatMap((w) => w.pods).filter((p) => p.ready).length;
snapshot.summary.restarts = snapshot.workloads.flatMap((w) => w.pods).reduce((total, p) => total + p.restarts, 0);
export default snapshot;
