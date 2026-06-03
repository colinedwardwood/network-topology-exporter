package httpx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeRediscoverer is a test double for the httpx.Rediscoverer interface.
type fakeRediscoverer struct {
	authConfigured bool
	gotTargets     []string
	results        []any
}

func (f *fakeRediscoverer) AuthConfigured() bool { return f.authConfigured }

func (f *fakeRediscoverer) RediscoverResults(_ context.Context, targets []string) []any {
	f.gotTargets = targets
	return f.results
}

func postJSON(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/rediscover", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestRediscoverHandlerNoAuthReturns403 verifies the privileged-endpoint
// contract: with no auth configured the handler must refuse (403) and never
// touch the Rediscoverer. Unlike /metrics, the default no-auth ground state
// does not expose this endpoint.
func TestRediscoverHandlerNoAuthReturns403(t *testing.T) {
	fake := &fakeRediscoverer{authConfigured: false}
	h := NewRediscoverHandler(fake)

	rec := postJSON(t, h, `{"targets":["10.0.0.1"]}`)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if fake.gotTargets != nil {
		t.Errorf("Rediscover was called despite no auth configured: %v", fake.gotTargets)
	}
}

// TestRediscoverHandlerHappyPath verifies that with auth configured a valid
// POST reaches the Rediscoverer and the per-target results are echoed back.
func TestRediscoverHandlerHappyPath(t *testing.T) {
	fake := &fakeRediscoverer{
		authConfigured: true,
		results: []any{
			map[string]any{"target": "10.0.0.1", "outcome": "success", "edges": 2},
		},
	}
	h := NewRediscoverHandler(fake)

	rec := postJSON(t, h, `{"targets":["10.0.0.1"]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if len(fake.gotTargets) != 1 || fake.gotTargets[0] != "10.0.0.1" {
		t.Errorf("targets passed to Rediscover = %v, want [10.0.0.1]", fake.gotTargets)
	}
	var resp struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0]["outcome"] != "success" {
		t.Errorf("results = %v, want one success", resp.Results)
	}
}

// TestRediscoverHandlerRejectsGET verifies non-POST methods are refused.
func TestRediscoverHandlerRejectsGET(t *testing.T) {
	h := NewRediscoverHandler(&fakeRediscoverer{authConfigured: true})
	req := httptest.NewRequest(http.MethodGet, "/admin/rediscover", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// TestRediscoverHandlerEmptyAndBadBody verifies request-body validation: an
// empty targets array and malformed JSON both yield 400 without invoking the
// Rediscoverer.
func TestRediscoverHandlerEmptyAndBadBody(t *testing.T) {
	for name, body := range map[string]string{
		"empty targets": `{"targets":[]}`,
		"not json":      `{not json`,
		"unknown field": `{"targets":["10.0.0.1"],"bogus":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeRediscoverer{authConfigured: true}
			h := NewRediscoverHandler(fake)
			rec := postJSON(t, h, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if fake.gotTargets != nil {
				t.Errorf("Rediscover called on bad request: %v", fake.gotTargets)
			}
		})
	}
}
