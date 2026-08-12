package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testToken = "test-token"

// newTestServer builds the real router with a nil Store. Every case here is
// rejected by auth or by parameter validation, so no handler reaches the
// database — a nil dereference would mean validation let something through.
func newTestServer(ping func(context.Context) error) http.Handler {
	if ping == nil {
		ping = func(context.Context) error { return nil }
	}
	return Routes(&API{Ping: ping}, testToken)
}

func do(t *testing.T, h http.Handler, method, target, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, target, nil)
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestProbesAreNotBehindAuth(t *testing.T) {
	h := newTestServer(nil)

	for _, path := range []string{"/healthz", "/readyz"} {
		rec := do(t, h, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Errorf("%s without auth: got %d, want 200", path, rec.Code)
		}
	}
}

func TestReadyzReportsUnavailableWhenPingFails(t *testing.T) {
	h := newTestServer(func(context.Context) error { return errors.New("db is down") })

	rec := do(t, h, http.MethodGet, "/readyz", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", rec.Code)
	}
}

func TestAuthRejectsBadCredentials(t *testing.T) {
	h := newTestServer(nil)

	cases := map[string]string{
		"no header":       "",
		"wrong scheme":    "Basic " + testToken,
		"missing prefix":  testToken,
		"wrong token":     "Bearer nope",
		"empty token":     "Bearer ",
		"token as prefix": "Bearer test-token-extra",
	}

	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			rec := do(t, h, http.MethodGet, "/api/v1/vms", header)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("got %d, want 401", rec.Code)
			}
		})
	}
}

func TestAuthChallengeOnMissingHeader(t *testing.T) {
	h := newTestServer(nil)

	rec := do(t, h, http.MethodGet, "/api/v1/vms", "")
	if got := rec.Header().Get("WWW-Authenticate"); got == "" {
		t.Fatal("missing WWW-Authenticate header on 401")
	}
}

func TestListVMsRejectsBadParams(t *testing.T) {
	h := newTestServer(nil)

	cases := map[string]string{
		"limit not a number": "/api/v1/vms?limit=abc",
		"limit zero":         "/api/v1/vms?limit=0",
		"limit negative":     "/api/v1/vms?limit=-5",
		"unknown sort":       "/api/v1/vms?sort=drop_table",
		"garbage cursor":     "/api/v1/vms?cursor=!!!",
	}

	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			rec := do(t, h, http.MethodGet, target, "Bearer "+testToken)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400", rec.Code)
			}
		})
	}
}

func TestNodeInventoryRejectsBadID(t *testing.T) {
	h := newTestServer(nil)

	for _, id := range []string{"abc", "0", "-1"} {
		t.Run(id, func(t *testing.T) {
			rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+id+"/inventory", "Bearer "+testToken)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400", rec.Code)
			}
		})
	}
}

func TestErrorBodyIsJSON(t *testing.T) {
	h := newTestServer(nil)

	rec := do(t, h, http.MethodGet, "/api/v1/vms", "")

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: got %q, want application/json", ct)
	}

	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Error == "" {
		t.Error("error body has no message")
	}
}
