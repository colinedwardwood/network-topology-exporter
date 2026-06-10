package federation

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
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

func TestIsReadyRequiresLeadership(t *testing.T) {
	h := NewHub(config.FederationConfig{}, metrics.New(false), nil, "")
	h.firstLive.Store(true)
	h.SetLeader(true)
	if !h.IsReady() {
		t.Fatal("leader with firstLive must be ready")
	}
	h.SetLeader(false)
	if h.IsReady() {
		t.Fatal("non-leader must not be ready (push Service gate)")
	}
}

func TestHandlePush503WhenNotLeader(t *testing.T) {
	h := NewHub(config.FederationConfig{SpokeTimeout: time.Hour}, metrics.New(false), nil, "")
	h.SetLeader(false)
	body, _ := json.Marshal(SpokePayload{SpokeID: "dc-x", CycleAt: time.Now(), Devices: []discovery.Device{{ID: "d1"}}})
	rec := httptest.NewRecorder()
	h.handlePush(rec, httptest.NewRequest(http.MethodPost, "/spoke/push", bytes.NewReader(body)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("non-leader push: got %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("Connection"); got != "close" {
		t.Fatalf("expected Connection: close, got %q", got)
	}
	// Leader accepts (does not 503).
	h.SetLeader(true)
	rec = httptest.NewRecorder()
	h.handlePush(rec, httptest.NewRequest(http.MethodPost, "/spoke/push", bytes.NewReader(body)))
	if rec.Code == http.StatusServiceUnavailable {
		t.Fatal("leader push wrongly 503'd")
	}
}
