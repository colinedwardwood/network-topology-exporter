// Command topology-exporter discovers network topology over SNMP, LLDP,
// CDP, BGP, OSPF, and FDB, and emits the result as Prometheus metrics and
// structured log lines.
//
// README.md documents the emitted-signal contract; CONTRIBUTING.md documents
// the clean-room development rules.
//
// This package is the thin entry point: argv parsing, the discovery engine,
// and HTTP handlers all live in internal/app (and internal/app/httpx).
package main

import (
	"context"
	"os"

	"github.com/grafana/network-topology-exporter/internal/app"
)

func main() {
	os.Exit(app.Run(context.Background(), os.Args[1:]))
}
