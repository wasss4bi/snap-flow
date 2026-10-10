import http from 'k6/http';
import {check, sleep} from 'k6';

const base = __ENV.BASE_URL || 'http://server:8080';
const token = __ENV.K6_TOKEN || '';
if (!token || __ENV.K6_ALLOW_WRITES !== '1') throw new Error('Upload test requires K6_TOKEN and K6_ALLOW_WRITES=1; use an isolated test account');
const image = open('/scripts/pixel.png', 'b');
const headers = {Authorization: `Bearer ${token}`};
export const options = {
  vus: Number(__ENV.K6_VUS || 1),
  duration: __ENV.K6_DURATION || '30s',
  thresholds: {http_req_failed: ['rate<0.01'], checks: ['rate>0.99']},
};
export default function () {
  const uploaded = http.post(`${base}/api/upload-images`, {images: http.file(image, 'k6.png', 'image/png')}, {headers, tags: {name: '/api/upload-images'}});
  if (!check(uploaded, {'upload accepted': r => r.status === 202})) return;
  const name = uploaded.json('files.0');
  if (!name) { check(null, {'upload has filename': () => false}); return; }
  let ready = false;
  for (let i = 0; i < 60; i++) {
    const status = http.get(`${base}/api/uploads/${name}`, {headers, tags: {name: '/api/uploads/:filename'}});
    if (status.status === 200 && status.json('status') === 'ready') { ready = true; break; }
    if (status.status === 200 && status.json('status') === 'failed') break;
    sleep(0.5);
  }
  if (!check(ready, {'upload ready': Boolean})) return;
  const list = http.get(`${base}/api/images?page=1&limit=100`, {headers, tags: {name: '/api/images'}});
  const rows = list.status === 200 ? list.json('images') : [];
  const item = Array.isArray(rows) ? rows.find(row => row.filename === name) : null;
  if (!check(item, {'uploaded image found for cleanup': Boolean})) return;
  const deleted = http.del(`${base}/api/delete-image/${item.id}`, null, {headers, tags: {name: '/api/delete-image/:id'}});
  check(deleted, {'delete scheduled': r => r.status === 200});
  sleep(1);
}
