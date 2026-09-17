package main

// Batch 37D - opt-in profiling listener (the second half of the load-testing
// roadmap row: "profiled matches under load").
//
// Threat model first, convenience second. pprof handlers expose heap,
// goroutine and CPU profiles of the process, so they must never ride the
// public listener: startPProf binds its own socket, REFUSES any non-loopback
// address, and serves from its own mux - the api mux (api.Routes) is never
// touched, so enabling profiling cannot widen the public surface by accident.
// The listener only exists when WORDARENA_PPROF_ADDR is set; the default
// deployment has no pprof socket at all. The Q8 stage-1 tunnel proxies only
// the game port, so even a misconfigured external address could not be reached
// through the tunnel - the loopback refusal below is the enforced layer, the
// tunnel is incidental cover, not the control.

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
)

// startPProf binds addr and serves the standard profiling handlers on it.
// addr must be loopback (127.0.0.1 or ::1); anything else is an error, not a
// warning. The returned listener is owned by the caller (in practice the
// process keeps it open until exit; tests must Close it).
func startPProf(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("pprof addr %q: %w", addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("pprof addr %q: profiling is loopback-only by policy (profiling data is unauthenticated by design)", addr)
	}
	mux := http.NewServeMux() // never the api mux - see header comment
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	// Handlers for the named profiles (heap, goroutine, allocs, ...) are
	// reached through pprof.Index above; registering the sub-paths explicitly
	// would only duplicate the same unauthenticated surface.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	go func() {
		if err := http.Serve(ln, mux); err != nil {
			log.Printf("pprof listener on %s stopped: %v", addr, err)
		}
	}()
	log.Printf("pprof listening on http://%s/debug/pprof/ (loopback only; unauthenticated by design - do not proxy)", addr)
	return ln, nil
}
