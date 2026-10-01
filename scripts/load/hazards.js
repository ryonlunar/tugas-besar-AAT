import http from 'k6/http';
import { Counter, Rate } from 'k6/metrics';
import { sleep } from 'k6';

const authURL = __ENV.AUTH_URL || 'http://127.0.0.1:8084';
const apiURL = __ENV.API_URL || 'http://127.0.0.1:8080';
const clientSecret = __ENV.FIELD_TEAM_CLIENT_SECRET;
if (!clientSecret) throw new Error('FIELD_TEAM_CLIENT_SECRET must be set in the k6 environment');

export const options = {
  scenarios: {
    bmkg_readers: { executor: 'constant-vus', exec: 'bmkgReaders', vus: 30, duration: '90s', gracefulStop: '5s' },
    pvmbg_readers: { executor: 'constant-vus', exec: 'pvmbgReaders', vus: 30, duration: '90s', gracefulStop: '5s' },
  },
  thresholds: {
    'http_req_duration{endpoint:hazards,source:BMKG}': ['p(95)<300'],
    hazard_unexpected_rate: ['rate<0.01'],
  },
};

const hazardAttempts = new Counter('hazard_attempts');
const hazardSuccesses = new Counter('hazard_successes');
const hazard429 = new Counter('hazard_controlled_429');
const hazardUnexpected = new Counter('hazard_unexpected');
const hazardUnexpectedRate = new Rate('hazard_unexpected_rate');
const authAttempts = new Counter('auth_attempts');
const auth429 = new Counter('auth_controlled_429');
const authUnexpected = new Counter('auth_unexpected');
let session;

function requestSession(path, payload) {
  for (let attempt = 0; attempt < 5; attempt += 1) {
    authAttempts.add(1);
    const response = http.post(`${authURL}${path}`, JSON.stringify(payload), { headers: { 'Content-Type': 'application/json' }, tags: { endpoint: 'auth' }, timeout: '5s' });
    if (response.status === 200) {
      const body = response.json();
      return { accessToken: body.access_token, refreshToken: body.refresh_token, issuedAt: Date.now() };
    }
    if (response.status === 429 && attempt < 4) {
      auth429.add(1);
      // Only bootstrap/refresh retries sleep; hazard workers have no think time.
      sleep(0.1 * (attempt + 1));
      continue;
    }
    if (response.status === 429) auth429.add(1); else authUnexpected.add(1);
    throw new Error(`auth ${path} failed with status ${response.status}`);
  }
  throw new Error(`auth ${path} exhausted retries`);
}

function token() {
  if (!session) session = requestSession('/v1/auth/login', { client_id: 'field-team', client_secret: clientSecret });
  if (Date.now() - session.issuedAt >= 50000) session = requestSession('/v1/auth/refresh', { refresh_token: session.refreshToken });
  return session.accessToken;
}

function read(source) {
  hazardAttempts.add(1);
  const response = http.get(`${apiURL}/v1/hazards?source=${source}&view=full&limit=50`, { headers: { Authorization: `Bearer ${token()}` }, tags: { endpoint: 'hazards', source }, timeout: '5s' });
  if (response.status >= 200 && response.status < 300) { hazardSuccesses.add(1); hazardUnexpectedRate.add(0); }
  else if (response.status === 429) { hazard429.add(1); /* excluded from unexpected-error denominator */ }
  else { hazardUnexpected.add(1); hazardUnexpectedRate.add(1); }
}

export function bmkgReaders() { read('BMKG'); }
export function pvmbgReaders() { read('PVMBG'); }
