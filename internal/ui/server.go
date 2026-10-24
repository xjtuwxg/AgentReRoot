// Package ui serves the local web interface of reroot: an overview of the
// workspace, a plan editor, dry-run previews, apply and undo.
//
// The server binds to 127.0.0.1 only. Every API request must carry the
// random token printed in the start URL, and the Host header must be the
// loopback address, which blocks other web pages (CSRF, DNS rebinding) from
// driving it.
package ui

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/xjtuwxg/AgentReRoot/internal/adapter"
	"github.com/xjtuwxg/AgentReRoot/internal/adapter/claude"
	"github.com/xjtuwxg/AgentReRoot/internal/adapter/registry"
	"github.com/xjtuwxg/AgentReRoot/internal/apply"
	"github.com/xjtuwxg/AgentReRoot/internal/journal"
	"github.com/xjtuwxg/AgentReRoot/internal/plan"
	"github.com/xjtuwxg/AgentReRoot/internal/scan"
)

//go:embed web
var webFS embed.FS

// Options configure the UI server.
type Options struct {
	Root     string
	PlanFile string // where the plan is loaded from and saved to
	Addr     string // listen address, default 127.0.0.1:0
	Depth    int
	Open     bool // open the browser
}

type server struct {
	opts  Options
	token string
	host  string

	mu     sync.Mutex
	report *scan.Report
}

// DefaultPlanFile is where the UI keeps the plan for root when no file is given.
func DefaultPlanFile(root string) (string, error) {
	home, err := journal.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "plans", claude.Encode(root)+".yaml"), nil
}

// Serve runs the UI until the process is interrupted. ready is called with
// the URL once the server listens.
func Serve(opts Options, ready func(url string)) error {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:0"
	}
	host, _, err := net.SplitHostPort(opts.Addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("refusing to listen on non-loopback address %s", opts.Addr)
	}
	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return err
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	s := &server{opts: opts, token: hex.EncodeToString(b), host: ln.Addr().String()}
	url := fmt.Sprintf("http://%s/?t=%s", s.host, s.token)
	if ready != nil {
		ready(url)
	}
	if opts.Open {
		openBrowser(url)
	}
	return http.Serve(ln, s.routes())
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/scan", s.api(s.handleScan))
	mux.HandleFunc("GET /api/plan", s.api(s.handleGetPlan))
	mux.HandleFunc("PUT /api/plan", s.api(s.handlePutPlan))
	mux.HandleFunc("POST /api/dryrun", s.api(s.handleDryRun))
	mux.HandleFunc("POST /api/apply", s.api(s.handleApply))
	mux.HandleFunc("GET /api/history", s.api(s.handleHistory))
	mux.HandleFunc("POST /api/undo", s.api(s.handleUndo))
	mux.HandleFunc("GET /api/verify", s.api(s.handleVerify))
	return s.guard(mux)
}

// guard rejects requests whose Host is not our loopback address.
func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, port, _ := net.SplitHostPort(s.host)
		if r.Host != s.host && r.Host != "localhost:"+port {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data:")
		next.ServeHTTP(w, r)
	})
}

type apiFunc func(r *http.Request) (any, error)

// httpError carries a status code through an apiFunc.
type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return e.msg }

func (s *server) api(f apiFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-Reroot-Token")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			http.Error(w, "missing or wrong token", http.StatusUnauthorized)
			return
		}
		v, err := f(r)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			code := http.StatusInternalServerError
			var he *httpError
			if errors.As(err, &he) {
				code = he.code
			}
			w.WriteHeader(code)
			v = map[string]string{"error": err.Error()}
		}
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(v)
	}
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	if err := dec.Decode(v); err != nil {
		return &httpError{http.StatusBadRequest, "bad request body: " + err.Error()}
	}
	return nil
}

func (s *server) handleScan(r *http.Request) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.report == nil || r.URL.Query().Get("refresh") == "1" {
		rep, err := scan.Run(s.opts.Root, scan.Options{MaxDepth: s.opts.Depth, Adapters: registry.Available()})
		if err != nil {
			return nil, err
		}
		s.report = rep
	}
	return s.report, nil
}

type planResponse struct {
	Plan   *plan.Plan      `json:"plan"`
	File   string          `json:"file"`
	Home   string          `json:"home"`
	Errors []*plan.OpError `json:"errors"`
}

func (s *server) loadPlan() (*plan.Plan, error) {
	p, err := plan.Load(s.opts.PlanFile)
	if errors.Is(err, os.ErrNotExist) {
		return &plan.Plan{Version: 1, Root: s.opts.Root}, nil
	}
	if err != nil {
		return nil, err
	}
	if p.Root != s.opts.Root {
		return nil, fmt.Errorf("%s is a plan for %s, not %s", s.opts.PlanFile, p.Root, s.opts.Root)
	}
	return p, nil
}

func (s *server) check(p *plan.Plan) []*plan.OpError {
	_, errs := p.Resolve()
	out := []*plan.OpError{}
	for _, e := range errs {
		var oe *plan.OpError
		if errors.As(e, &oe) {
			out = append(out, oe)
		} else {
			out = append(out, &plan.OpError{Msg: e.Error()})
		}
	}
	return out
}

func (s *server) handleGetPlan(r *http.Request) (any, error) {
	p, err := s.loadPlan()
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	return planResponse{Plan: p, File: s.opts.PlanFile, Home: home, Errors: s.check(p)}, nil
}

// incoming normalizes a plan sent by the browser: the root is always ours.
func (s *server) incoming(r *http.Request) (*plan.Plan, error) {
	var p plan.Plan
	if err := decode(r, &p); err != nil {
		return nil, err
	}
	p.Version = 1
	p.Root = s.opts.Root
	return &p, nil
}

func (s *server) handlePutPlan(r *http.Request) (any, error) {
	p, err := s.incoming(r)
	if err != nil {
		return nil, err
	}
	b, err := yaml.Marshal(p)
	if err != nil {
		return nil, err
	}
	header := "# reroot plan, edited in `reroot ui`. Operations run in order.\n"
	if err := os.MkdirAll(filepath.Dir(s.opts.PlanFile), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(s.opts.PlanFile, append([]byte(header), b...), 0o644); err != nil {
		return nil, err
	}
	return planResponse{Plan: p, File: s.opts.PlanFile, Errors: s.check(p)}, nil
}

type runResponse struct {
	Errors []*plan.OpError `json:"errors"`
	Result *apply.Result   `json:"result,omitempty"`
	Error  string          `json:"run_error,omitempty"`
}

func (s *server) run(p *plan.Plan, dryRun bool) (any, error) {
	if errs := s.check(p); len(errs) > 0 {
		return runResponse{Errors: errs}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := apply.Run(p, apply.Options{DryRun: dryRun, PlanFile: s.opts.PlanFile, Adapters: registry.Available()})
	out := runResponse{Errors: []*plan.OpError{}, Result: res}
	if err != nil {
		out.Error = err.Error()
	}
	if !dryRun {
		s.report = nil // the workspace changed; rescan on next request
	}
	return out, nil
}

func (s *server) handleDryRun(r *http.Request) (any, error) {
	p, err := s.incoming(r)
	if err != nil {
		return nil, err
	}
	return s.run(p, true)
}

func (s *server) handleApply(r *http.Request) (any, error) {
	p, err := s.incoming(r)
	if err != nil {
		return nil, err
	}
	if r.URL.Query().Get("confirm") != "yes" {
		return nil, &httpError{http.StatusBadRequest, "apply needs confirm=yes"}
	}
	return s.run(p, false)
}

func (s *server) handleHistory(r *http.Request) (any, error) {
	all, err := journal.List()
	if all == nil {
		all = []journal.Meta{}
	}
	return all, err
}

func (s *server) handleUndo(r *http.Request) (any, error) {
	var req struct {
		ID string `json:"id"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if req.ID == "" {
		return nil, &httpError{http.StatusBadRequest, "undo needs a journal id"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, acts, err := journal.Undo(req.ID, false)
	s.report = nil
	resp := map[string]any{"journal": meta, "actions": acts}
	if err != nil {
		resp["run_error"] = err.Error()
	}
	return resp, nil
}

func (s *server) handleVerify(r *http.Request) (any, error) {
	out := []adapter.Finding{}
	for _, a := range registry.Available() {
		out = append(out, a.Verify(s.opts.Root)...)
	}
	return out, nil
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "open the URL above in your browser")
	}
}
