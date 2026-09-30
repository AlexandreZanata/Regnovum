export const options = {
  scenarios: {
    probe: { executor: 'constant-vus', vus: 1, duration: '1s', exec: 'probe' },
  },
  thresholds: {
    probe_failed_total: ['count==0'],
    "checks{kind:invariant}": ['rate==1'],
    'http_req_duration{workload:visitors}': ['p(95)<1000'],
  },
};
export function probe() {}
