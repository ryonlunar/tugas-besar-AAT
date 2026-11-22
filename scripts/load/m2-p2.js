import http from 'k6/http';
import exec from 'k6/execution';
import { sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

const vus = Number(__ENV.P2_VUS || 10000);
const ramp = Number(__ENV.P2_RAMP_SECONDS || 45);
const duration = Number(__ENV.P2_SECONDS || 150);
const think = Number(__ENV.P2_THINK_SECONDS || 8);
const rate = Number(__ENV.P2_RATE || 0);
const api = __ENV.P2_API_URL;
const relay = __ENV.P2_TOKEN_URL;
if (!api || !relay) throw new Error('Set P2_API_URL and P2_TOKEN_URL.');
for (const value of [vus, ramp, duration]) {
  if (!Number.isInteger(value) || value < 1) throw new Error('VUs, ramp and duration must be positive integers.');
}
if (!Number.isFinite(think) || think < 0 || !Number.isInteger(rate) || rate < 0) {
  throw new Error('Think time and request rate must be nonnegative.');
}

const outcomes = ['complete', 'partial', 'controlled', 'auth', 'http503', 'network', 'other'];
const counts = new Counter('p2_outcomes');
const unexpected = new Rate('p2_unexpected');
const latency = new Trend('p2_complete_latency', true);
const thresholds = {
  'p2_unexpected{phase:steady}': ['rate<0.01'],
  'p2_complete_latency{phase:steady}': ['p(95)>=0'],
};
for (const phase of ['ramp', 'steady']) {
  for (const kind of outcomes) thresholds[`p2_outcomes{kind:${kind},phase:${phase}}`] = ['count>=0'];
}
const initialVUs = Math.max(10, Math.ceil(rate / 4));
export const options = {
  discardResponseBodies: true,
  systemTags: ['status', 'method', 'name', 'scenario', 'error_code'],
  summaryTrendStats: ['p(50)', 'p(95)', 'p(99)'],
  thresholds,
  scenarios: {
    hazard_reads: rate ? {
      executor: 'constant-arrival-rate', rate, timeUnit: '1s', duration: `${duration}s`,
      preAllocatedVUs: initialVUs, maxVUs: 2 * initialVUs, gracefulStop: '6s',
    } : {
      executor: 'ramping-vus', startVUs: 0, gracefulRampDown: '5s', gracefulStop: '6s',
      stages: [
        {duration: `${ramp}s`, target: vus},
        {duration: `${duration}s`, target: vus},
        {duration: '10s', target: 0},
      ],
    },
  },
};

let token;

function accessToken() {
  const earlyRefresh = 5 + (exec.vu.idInTest % 16);
  if (!token || token.expires_at_ms - earlyRefresh * 1000 <= Date.now()) {
    const response = http.get(relay, {
      timeout: '2s', responseType: 'text', tags: {name: 'local_token_relay'},
    });
    if (response.status !== 200) return null;
    token = response.json();
  }
  return token.access_token;
}

function outcome(response) {
  if (response.status === 0) return 'network';
  if (response.status === 401 || response.status === 403) return 'auth';
  if (response.status === 503) return 'http503';
  if (response.status !== 200 && response.status !== 429) return 'other';
  try {
    const body = response.json();
    if (response.status === 429) {
      return body.error?.code === 'RATE_LIMITED' && response.headers['Retry-After'] ? 'controlled' : 'other';
    }
    if (body.meta?.partial === true && Array.isArray(body.data)) return 'partial';
    if (body.meta?.partial === false && Array.isArray(body.data) && body.data.length) return 'complete';
  } catch {
    return 'other';
  }
  return 'other';
}

export default function () {
  const iteration = exec.vu.iterationInScenario;
  const id = exec.vu.idInTest;
  if (!rate && iteration === 0 && think) sleep((id % 1000) * think / 1000);
  const elapsed = (Date.now() - exec.scenario.startTime) / 1000;
  const phase = rate || (elapsed >= ramp && elapsed < ramp + duration) ? 'steady' : 'ramp';
  const credential = accessToken();
  let kind = 'auth';
  if (credential) {
    const source = (iteration + id) % 2 === 0 ? 'BMKG' : 'PVMBG';
    const response = http.get(`${api}/v1/hazards?source=${source}&view=full&limit=50`, {
      timeout: '5s', responseType: 'text', tags: {name: 'hazard_page'},
      headers: {
        Authorization: `Bearer ${credential}`,
        'X-Correlation-ID': `p2-${__ENV.P2_RUN_ID || 'local'}-${id}-${iteration}`,
      },
    });
    kind = outcome(response);
    if (kind === 'complete') latency.add(response.timings.duration, {phase});
  }
  counts.add(1, {kind, phase});
  if (kind !== 'controlled') unexpected.add(kind !== 'complete' && kind !== 'partial', {phase});
  if (!rate && think) sleep(think);
}

export function handleSummary(data) {
  const value = (name, key) => data.metrics[name]?.values[key] ?? 0;
  const phases = Object.fromEntries(['ramp', 'steady'].map(phase => [
    phase, Object.fromEntries(outcomes.map(kind => [kind, value(`p2_outcomes{kind:${kind},phase:${phase}}`, 'count')])),
  ]));
  return {
    'result.json': JSON.stringify({
      configured: {vus: rate ? null : vus, rate, ramp_seconds: rate ? 0 : ramp, steady_seconds: duration, think_seconds: rate ? 0 : think},
      outcomes: phases,
      complete_reads_per_second: phases.steady.complete / duration,
      unexpected_error_rate: value('p2_unexpected{phase:steady}', 'rate'),
      p95_complete_ms: value('p2_complete_latency{phase:steady}', 'p(95)'),
      dropped_iterations: value('dropped_iterations', 'count'),
      active_vu_peak: value('vus', 'max'),
      duration_seconds: data.state.testRunDurationMs / 1000,
    }, null, 2) + '\n',
    stdout: 'P2 result saved to result.json\n',
  };
}
