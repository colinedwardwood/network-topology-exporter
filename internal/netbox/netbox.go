// Package netbox is explicitly out of scope for v1.
//
// NetBox writeback breaks the output contract: this binary emits observability
// signals (Prometheus metrics, structured log lines), it does not modify
// external systems. Writing discovered devices into NetBox via REST means the
// exporter must handle: partial writes, NetBox downtime, auth failures,
// idempotency, and the risk of overwriting operator-curated documentation with
// incorrect discovery data.
//
// If NetBox integration is needed, the correct pattern is a separate
// reconciliation process that reads from the Prometheus/Mimir topology metrics
// and writes to NetBox — keeping the exporter's output contract clean.
package netbox
