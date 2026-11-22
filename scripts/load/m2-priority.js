import http from 'k6/http';
import { sleep } from 'k6';
import { Counter } from 'k6/metrics';

const url = __ENV.P5_API_URL;
const mediaToken = __ENV.P5_MEDIA_TOKEN;
const fieldToken = __ENV.P5_FIELD_TOKEN;
if (!url || !mediaToken || !fieldToken) {
  throw new Error('Set P5_API_URL, P5_MEDIA_TOKEN and P5_FIELD_TOKEN.');
}

const accepted = new Counter('priority_accepted');
const controlled = new Counter('priority_controlled_429');
const unexpected = new Counter('priority_unexpected');

export const options = {
  discardResponseBodies: true,
  scenarios: {
    media: { executor: 'constant-vus', vus: Number(__ENV.P5_MEDIA_VUS || 320), duration: '25s', exec: 'media' },
    field: { executor: 'constant-vus', vus: 10, duration: '25s', exec: 'field' },
  },
  thresholds: {
    'priority_controlled_429{role:media}': ['count>0'],
    'priority_accepted{role:media}': ['count>=0'],
    'priority_accepted{role:field-team}': ['count>0'],
    'priority_controlled_429{role:field-team}': ['count==0'],
    priority_unexpected: ['count==0'],
  },
};

function read(token, role) {
  const tags = { role };
  const response = http.get(`${url}/v1/hazards?source=BMKG&view=summary&limit=50`, {
    headers: { Authorization: `Bearer ${token}` },
    tags,
    timeout: '5s',
  });
  const ok = response.status === 200;
  const rejected = response.status === 429 && Boolean(response.headers['Retry-After']);
  accepted.add(Number(ok), tags);
  controlled.add(Number(rejected), tags);
  unexpected.add(Number(!ok && !rejected), tags);
}

export function media() { read(mediaToken, 'media'); }
export function field() { read(fieldToken, 'field-team'); sleep(0.2); }
