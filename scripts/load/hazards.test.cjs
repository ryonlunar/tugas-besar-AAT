const { readFileSync } = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
const test = require('node:test');

// Exercise the k6 script's control flow without requiring k6 or network services.
const source = readFileSync(`${__dirname}/hazards.js`, 'utf8')
  .replace(/^import .*;\n/gm, '').replace(/^export /gm, '')
  + '\nglobalThis.readers = { BMKG: bmkgReaders, PVMBG: pvmbgReaders }; globalThis.settings = options;';

function fixture({ authStatus = 200, hazardStatus = 200, malformedSession = false, retryAfter } = {}) {
  const samples = {};
  let now = 0;
  let gets = 0;
  const sleeps = [];
  class Metric {
    constructor(name) { this.name = name; samples[name] = []; }
    add(value, tags) { samples[this.name].push({ value, tags }); }
  }
  const context = {
    __ENV: { FIELD_TEAM_CLIENT_SECRET: 'test-only' },
    Counter: Metric, Rate: Metric, Trend: Metric, sleep: seconds => { sleeps.push(seconds); }, Date: { now: () => now },
    http: {
      post: () => ({ status: typeof authStatus === 'function' ? authStatus(now) : authStatus,
        headers: { 'Retry-After': retryAfter }, timings: { duration: 30 }, json: () => malformedSession ? {} : { access_token: 'access', refresh_token: 'refresh', expires_in: 60 } }),
      get: () => { gets += 1; return { status: hazardStatus, timings: { duration: 10 } }; },
    },
  };
  vm.createContext(context);
  vm.runInContext(source, context);
  return { samples, settings: context.settings, read: (source = 'BMKG') => context.readers[source](),
    sleeps, setTime: value => { now = value; }, gets: () => gets,
    count: name => samples[name].reduce((total, sample) => total + sample.value, 0) };
}

test('login failure sends no hazard request and records a failed workload', () => {
  const f = fixture({ authStatus: 401 });
  assert.throws(() => f.read(), /auth/);
  assert.equal(f.gets(), 0);
  assert.equal(f.count('hazard_iterations'), 1);
  assert.equal(f.count('hazard_attempts'), 0);
  assert.equal(f.count('auth_failures'), 1);
  assert.equal(f.settings.thresholds.auth_failures[0], 'count==0');
});

test('failed refresh does not count an unsent hazard request', () => {
  const f = fixture({ authStatus: now => now ? 503 : 200 });
  f.read(); f.setTime(51000);
  assert.throws(() => f.read(), /auth/);
  assert.equal(f.gets(), 1);
  assert.equal(f.count('hazard_attempts'), 1);
  assert.equal(f.count('auth_failures'), 1);
});

test('exhausted controlled Auth retries invalidate the run', () => {
  const f = fixture({ authStatus: 429 });
  assert.throws(() => f.read(), /auth/);
  assert.equal(f.count('auth_controlled_429'), 5);
  assert.equal(f.count('auth_failures'), 1);
  assert.equal(f.gets(), 0);
});

test('429 is counted separately and cannot satisfy successful workload requirements', () => {
  const f = fixture({ hazardStatus: 429 });
  f.read();
  assert.equal(f.count('hazard_attempts'), 1);
  assert.equal(f.count('hazard_controlled_429'), 1);
  assert.equal(f.samples.hazard_unexpected_rate.length, 0);
  assert.equal(f.samples.bmkg_success_duration.length, 0);
  assert.equal(f.settings.thresholds['hazard_successes{source:BMKG}'][0], 'count>0');
  assert.equal(f.settings.thresholds['hazard_successes{source:PVMBG}'][0], 'count>0');
});

test('successful hazard latency excludes Auth and remains separated by source', () => {
  const f = fixture(); f.read(); f.read('PVMBG');
  assert.equal(f.count('auth_duration'), 30);
  assert.equal(f.count('hazard_success_duration'), 20);
  assert.equal(f.count('bmkg_success_duration'), 10);
  assert.equal(f.count('pvmbg_success_duration'), 10);
  assert.equal(f.settings.thresholds.bmkg_success_duration[0], 'p(95)<300');
});

test('unexpected hazard errors contribute to the noncontrolled error denominator', () => {
  const f = fixture({ hazardStatus: 503 }); f.read();
  assert.equal(f.count('hazard_unexpected'), 1);
  assert.equal(f.count('hazard_unexpected_rate'), 1);
});

test('malformed successful Auth response fails the workload explicitly', () => {
  const f = fixture({ malformedSession: true });
  assert.throws(() => f.read(), /invalid session/);
  assert.equal(f.count('auth_failures'), 1);
  assert.equal(f.gets(), 0);
});

 test('controlled Auth retries respect bounded Retry-After guidance', () => {
  const f = fixture({ authStatus: 429, retryAfter: '1' });
  assert.throws(() => f.read(), /auth/);
  assert.deepEqual(f.sleeps, [1, 1, 1, 1]);
  const bounded = fixture({ authStatus: 429, retryAfter: '3600' });
  assert.throws(() => bounded.read(), /auth/);
  assert.deepEqual(bounded.sleeps, [5, 5, 5, 5]);
 });
