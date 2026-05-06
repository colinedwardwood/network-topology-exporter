// Package version exposes build metadata, populated at build time via -ldflags.
package version

// Version metadata populated at build time via -ldflags.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)
