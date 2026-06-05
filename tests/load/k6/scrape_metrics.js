// k6 load test for the exporter's HTTP scrape surface.
//
// The exporter's runtime load is dominated by Prometheus scraping /metrics —
// rendering the topology collector under whatever cardinality the discovered
// graph carries. This script hammers /metrics with concurrent virtual users
// and asserts latency + error-rate thresholds, the same way a busy Prometheus
// (or several federated scrapers) would.
//
// Run locally against any running exporter:
//   k6 run tests/load/k6/scrape_metrics.js
//   TARGET=http://host:9100 VUS=50 DURATION=2m k6 run tests/load/k6/scrape_metrics.js
//
// For a realistic high-cardinality measurement, point TARGET at an instance
// that has discovered a large topology (e.g. the deploy/long-running-test lab,
// or an instance started from a large snapshot) rather than a fresh process.
//
// k6 is a Grafana product; results stream to Grafana Cloud k6 with `k6 cloud`.
import http from "k6/http";
import { check } from "k6";

const TARGET = __ENV.TARGET || "http://localhost:9100";

export const options = {
  scenarios: {
    scrape: {
      executor: "constant-vus",
      vus: Number(__ENV.VUS || 20),
      duration: __ENV.DURATION || "30s",
    },
  },
  thresholds: {
    // Fewer than 1% of scrapes may fail.
    http_req_failed: ["rate<0.01"],
    // Scrape latency budget — align with your Prometheus scrape_timeout.
    http_req_duration: ["p(95)<500", "p(99)<1500"],
  },
};

export default function () {
  const res = http.get(`${TARGET}/metrics`);
  check(res, {
    "status is 200": (r) => r.status === 200,
    "exposes topology metrics": (r) =>
      typeof r.body === "string" && r.body.includes("network_topology_"),
  });
}
