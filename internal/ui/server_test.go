package ui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) (*server, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("REROOT_HOME", filepath.Join(dir, ".reroot"))
	s := &server{
		opts:  Options{Root: dir, PlanFile: filepath.Join(dir, "plan.yaml")},
		token: "secret",
		host:  "127.0.0.1:4000",
	}
	return s, s.routes()
}

func do(h http.Handler, method, host, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = host
	if token != "" {
		req.Header.Set("X-Reroot-Token", token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGuards(t *testing.T) {
	_, h := newTestServer(t)
	cases := []struct {
		name, method, host, path, token, body string
		want                                  int
	}{
		{"page is public", "GET", "127.0.0.1:4000", "/", "", "", 200},
		{"api needs token", "GET", "127.0.0.1:4000", "/api/plan", "", "", 401},
		{"api rejects wrong token", "GET", "127.0.0.1:4000", "/api/plan", "nope", "", 401},
		{"api with token", "GET", "127.0.0.1:4000", "/api/plan", "secret", "", 200},
		{"localhost alias", "GET", "localhost:4000", "/api/plan", "secret", "", 200},
		{"dns rebinding host", "GET", "evil.example:4000", "/api/plan", "secret", "", 403},
		{"apply needs confirm", "POST", "127.0.0.1:4000", "/api/apply", "secret", `{"operations":[]}`, 400},
	}
	for _, c := range cases {
		if rec := do(h, c.method, c.host, c.path, c.token, c.body); rec.Code != c.want {
			t.Errorf("%s: got %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body.String())
		}
	}
}

func TestPlanRoundTrip(t *testing.T) {
	s, h := newTestServer(t)
	body := `{"root":"/somewhere/else","operations":[{"op":"move","from":"missing","to":"x"}]}`
	rec := do(h, "PUT", s.host, "/api/plan", "secret", body)
	if rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	// The root sent by the browser is ignored, and validation reports the missing source.
	if !strings.Contains(rec.Body.String(), `"root":"`+s.opts.Root+`"`) || !strings.Contains(rec.Body.String(), "does not exist") {
		t.Errorf("unexpected response: %s", rec.Body.String())
	}
	rec = do(h, "GET", s.host, "/api/plan", "secret", "")
	if !strings.Contains(rec.Body.String(), `"from":"missing"`) {
		t.Errorf("plan not persisted: %s", rec.Body.String())
	}
}
