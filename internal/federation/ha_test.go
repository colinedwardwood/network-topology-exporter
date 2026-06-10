package federation

import (
	"context"
	"testing"

	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
)

func TestHubLeadershipState(t *testing.T) {
	h := NewHub(config.FederationConfig{}, metrics.New(false), nil, "")
	if !h.IsLeader() {
		t.Fatal("non-HA hub must default to leader")
	}
	h.SetLeader(false)
	if h.IsLeader() {
		t.Fatal("SetLeader(false) not reflected")
	}
	h.SetLeader(true)
	if !h.IsLeader() {
		t.Fatal("SetLeader(true) not reflected")
	}
}

// TestFakeElectorDrivesCallbacks exercises the test-only fakeElector so its
// symbols are referenced (and verifies the lead/step → callback wiring that
// T6+ will rely on). Run blocks until ctx is cancelled and returns ctx.Err();
// lead()/step() invoke the recorded callbacks.
func TestFakeElectorDrivesCallbacks(t *testing.T) {
	f := newFakeElector()

	var started, stopped bool
	cb := LeaderCallbacks{
		OnStartedLeading: func(context.Context) { started = true },
		OnStoppedLeading: func() { stopped = true },
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Run records the callbacks then blocks on ctx. Start it, drive the
	// callbacks once it has recorded them (signalled via ready), then cancel.
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- f.run(ctx, cb, ready)
	}()
	<-ready

	f.lead()
	if !started {
		t.Fatal("lead() did not invoke OnStartedLeading")
	}
	f.step()
	if !stopped {
		t.Fatal("step() did not invoke OnStoppedLeading")
	}

	cancel()
	if err := <-done; err == nil {
		t.Fatal("Run should return ctx.Err() after cancel")
	}
}
