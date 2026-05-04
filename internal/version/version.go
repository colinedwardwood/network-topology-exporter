// Package version exposes build metadata, populated at build time via -ldflags.
package version

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)
