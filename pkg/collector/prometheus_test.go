package collector

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestBearerRoundTripperAddsHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer srv.Close()

	rt := &bearerRoundTripper{token: "abc123", rt: http.DefaultTransport}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	if gotAuth != "Bearer abc123" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer abc123")
	}
}

// A rotated on-disk token (projected SA token refreshed by the kubelet) must be
// picked up per request, not cached for the client lifetime.
func TestBearerRoundTripperRefreshesTokenFromFile(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("first-token\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	rt := &bearerRoundTripper{tokenPath: path, rt: http.DefaultTransport}

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()
	if gotAuth != "Bearer first-token" {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer first-token")
	}

	// Rotate the file; the next request must reflect the new token.
	if err := os.WriteFile(path, []byte("second-token"), 0o600); err != nil {
		t.Fatalf("rewrite token: %v", err)
	}
	req2, _ := http.NewRequest("GET", srv.URL, nil)
	resp2, err := rt.RoundTrip(req2)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp2.Body.Close()
	if gotAuth != "Bearer second-token" {
		t.Errorf("Authorization = %q, want %q (rotated token not picked up)", gotAuth, "Bearer second-token")
	}
}

func TestBearerRoundTripperEmptyTokenNoHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	}))
	defer srv.Close()
	rt := &bearerRoundTripper{token: "", rt: http.DefaultTransport}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty", gotAuth)
	}
}
