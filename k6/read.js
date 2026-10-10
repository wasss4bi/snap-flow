import http from 'k6/http';
import {check, sleep} from 'k6';

const base = __ENV.BASE_URL || 'http://server:8080';
const token = __ENV.K6_TOKEN || '';
export const options = {
  vus: Number(__ENV.K6_VUS || 5),
  duration: __ENV.K6_DURATION || '30s',
  thresholds: {http_req_failed: ['rate<0.01'], http_req_duration: ['p(95)<1000']},
};
export default function () {
  const res = token
    ? http.get(`${base}/api/images?page=1&limit=20`, {headers: {Authorization: `Bearer ${token}`}, tags: {name: '/api/images'}})
    : http.get(`${base}/`, {tags: {name: '/'}});
  check(res, {'status is 200': (r) => r.status === 200});
  sleep(1);
}
