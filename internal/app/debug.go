package app

import (
	"net/http"
	"net/http/pprof"
)

// newDebugMux builds the dedicated ServeMux that serves net/http/pprof at
// /debug/pprof/* (issue #69). The handlers are registered EXPLICITLY here
// rather than relying on net/http/pprof's init() side-effect (which installs
// them on http.DefaultServeMux). This keeps the debug surface strictly on the
// opt-in debug listener and guarantees it never leaks onto the main metrics
// mux / :9100 server.
//
// pprof.Index serves /debug/pprof/ AND every named runtime profile reachable
// at /debug/pprof/<name> (heap, goroutine, allocs, mutex, block,
// threadcreate), so registering it at the /debug/pprof/ prefix covers them all.
func newDebugMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}
