import http from 'k6/http';
import { Counter, Rate, Trend } from 'k6/metrics';
import { sleep } from 'k6';

const authURL = __ENV.AUTH_URL || 'http://127.0.0.1:8084';
const apiURL = __ENV.API_URL || 'http://127.0.0.1:8080';
const clientSecret = __ENV.FIELD_TEAM_CLIENT_SECRET;
if (!clientSecret) throw new Error('FIELD_TEAM_CLIENT_SECRET must be set in the k6 environment');

export const options = {
	// Written into the k6 summary JSON for the required p50/p95/p99 reporting.
	// `p(50)` is used rather than the default `med` label to make the report explicit.
	summaryTrendStats: ['min', 'max', 'avg', 'p(50)', 'p(95)', 'p(99)'],
  scenarios: {
    bmkg_readers: { executor: 'constant-vus', exec: 'bmkgReaders', vus: 30, duration: '90s', gracefulStop: '5s' },
    pvmbg_readers: { executor: 'constant-vus', exec: 'pvmbgReaders', vus: 30, duration: '90s', gracefulStop: '5s' },
  },
  thresholds: {
    bmkg_success_duration: ['p(95)<300'],
    hazard_unexpected_rate: ['rate<0.01'],
    'hazard_successes{source:BMKG}': ['count>0'],
    'hazard_successes{source:PVMBG}': ['count>0'],
    auth_failures: ['count==0'],
  },
};

const hazardIterations = new Counter('hazard_iterations');
const hazardAttempts = new Counter('hazard_attempts');
const hazardSuccesses = new Counter('hazard_successes');
const hazard429 = new Counter('hazard_controlled_429');
const hazardUnexpected = new Counter('hazard_unexpected');
const hazardUnexpectedRate = new Rate('hazard_unexpected_rate');
const authAttempts = new Counter('auth_attempts');
const auth429 = new Counter('auth_controlled_429');
const authUnexpected = new Counter('auth_unexpected');
const authFailures = new Counter('auth_failures');
const authDuration = new Trend('auth_duration', true);
const hazardDuration = new Trend('hazard_duration', true);
const hazardSuccessDuration = new Trend('hazard_success_duration', true);
const sourceDuration = { BMKG: new Trend('bmkg_duration', true), PVMBG: new Trend('pvmbg_duration', true) };
const sourceSuccessDuration = { BMKG: new Trend('bmkg_success_duration', true), PVMBG: new Trend('pvmbg_success_duration', true) };
let session;

function failAuth(message) {
  authFailures.add(1);
  throw new Error(message);
}

function requestSession(path, payload) {
  for (let attempt = 0; attempt < 5; attempt += 1) {
    authAttempts.add(1);
    const response = http.post(`${authURL}${path}`, JSON.stringify(payload), { headers: { 'Content-Type': 'application/json' }, tags: { endpoint: 'auth' }, timeout: '5s' });
    authDuration.add(response.timings.duration);
    if (response.status === 200) {
      let body;
      try { body = response.json(); } catch (_) { authUnexpected.add(1); return failAuth('auth returned invalid JSON'); }
      if (typeof body.access_token !== 'string' || !body.access_token || typeof body.refresh_token !== 'string' || !body.refresh_token || !(body.expires_in > 0)) {
        authUnexpected.add(1);
        return failAuth('auth returned an invalid session');
      }
      return { accessToken: body.access_token, refreshToken: body.refresh_token, refreshAt: Date.now() + Math.max(0, body.expires_in * 1000 - 10000) };
    }
    if (response.status === 429 && attempt < 4) {
      auth429.add(1);
      // Only bootstrap/refresh retries sleep; hazard workers have no think time.
      const retryAfter = Number(response.headers && response.headers['Retry-After']);
      sleep(Math.max(0.1 * (attempt + 1), Number.isFinite(retryAfter) && retryAfter > 0 ? Math.min(retryAfter, 5) : 0));
      continue;
    }
    if (response.status === 429) auth429.add(1); else authUnexpected.add(1);
    return failAuth(`auth ${path} failed with status ${response.status}`);
  }
  return failAuth(`auth ${path} exhausted retries`);
}

function token() {
  if (!session) session = requestSession('/v1/auth/login', { client_id: 'field-team', client_secret: clientSecret });
  if (Date.now() >= session.refreshAt) session = requestSession('/v1/auth/refresh', { refresh_token: session.refreshToken });
  return session.accessToken;
}

function read(source) {
  hazardIterations.add(1);
  const accessToken = token();
  const tags = { source };
  hazardAttempts.add(1, tags);
  const response = http.get(`${apiURL}/v1/hazards?source=${source}&view=full&limit=50`, { headers: { Authorization: `Bearer ${accessToken}` }, tags: { endpoint: 'hazards', source }, timeout: '5s' });
  hazardDuration.add(response.timings.duration);
  sourceDuration[source].add(response.timings.duration);
  if (response.status >= 200 && response.status < 300) {
    hazardSuccesses.add(1, tags);
    hazardUnexpectedRate.add(0);
    hazardSuccessDuration.add(response.timings.duration);
    sourceSuccessDuration[source].add(response.timings.duration);
  }
  else if (response.status === 429) { hazard429.add(1); /* excluded from unexpected-error denominator */ }
  else { hazardUnexpected.add(1); hazardUnexpectedRate.add(1); }
}

export function bmkgReaders() { read('BMKG'); }
export function pvmbgReaders() { read('PVMBG'); }
