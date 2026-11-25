// Package bridge is the agent's local HTTP server for the browser extension
// (PRD Fase 8c). It listens on 127.0.0.1 only and accepts a hostname and
// nothing else: from the owner's own extension (exact Origin), with the
// token the agent made, in a body of at most 1 KB.
package bridge

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ardwiinoo/tunas/internal/domains"
)

// MaxBody is the largest accepted request body (PRD: 1 KB).
const MaxBody = 1024

// DefaultPort is the bridge's port unless config.yaml sets extension.port.
const DefaultPort = 47821

// Config configures the bridge.
type Config struct {
	Port         int
	ExtensionIDs []string      // Chrome/Edge extension ids that may call (empty = every call is refused)
	Token        func() string // the current token ("" = refuse everything)
	OnTab        func(host string, at time.Time)
	Now          func() time.Time
	Log          *slog.Logger
}

// Server is the bridge.
type Server struct {
	cfg     Config
	origins map[string]bool
}

// New builds the bridge.
func New(cfg Config) *Server {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	s := &Server{cfg: cfg, origins: map[string]bool{}}
	for _, id := range cfg.ExtensionIDs {
		s.origins["chrome-extension://"+id] = true
	}
	return s
}

// Addr is where the bridge listens: loopback only.
func (s *Server) Addr() string { return fmt.Sprintf("127.0.0.1:%d", s.cfg.Port) }

// Run serves until ctx is done.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp4", s.Addr())
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 5 * time.Second, MaxHeaderBytes: 4 << 10}
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sh)
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Handler is the bridge's HTTP handler (exported for tests).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tab", s.tab)
	mux.HandleFunc("GET /v1/ping", s.ping)
	mux.HandleFunc("OPTIONS /", s.preflight)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.local(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// local checks who is asking: a loopback peer, with the Host header of the
// bridge itself (a DNS-rebinding page would carry its own name).
func (s *Server) local(r *http.Request) bool {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(h)
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	port := fmt.Sprint(s.cfg.Port)
	return r.Host == "127.0.0.1:"+port || r.Host == "localhost:"+port
}

// originOK reports whether the request comes from the owner's extension.
func (s *Server) originOK(r *http.Request) (string, bool) {
	o := r.Header.Get("Origin")
	if o == "" {
		return "", false
	}
	if u, err := url.Parse(o); err != nil || u.Path != "" || u.RawQuery != "" || u.User != nil {
		return "", false
	}
	return o, s.origins[o]
}

func (s *Server) tokenOK(r *http.Request) bool {
	want := ""
	if s.cfg.Token != nil {
		want = s.cfg.Token()
	}
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || want == "" || len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (s *Server) cors(w http.ResponseWriter, origin string) {
	w.Header().Set("Access-Control-Allow-Origin", origin) // the one allowed origin, never "*"
	w.Header().Add("Vary", "Origin")
}

func (s *Server) preflight(w http.ResponseWriter, r *http.Request) {
	origin, ok := s.originOK(r)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	s.cors(w, origin)
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
	w.Header().Set("Access-Control-Allow-Headers", "authorization, content-type")
	w.Header().Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

// guard runs the checks every real call must pass.
func (s *Server) guard(w http.ResponseWriter, r *http.Request) bool {
	origin, ok := s.originOK(r)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	if !s.tokenOK(r) {
		s.cors(w, origin)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	s.cors(w, origin)
	return true
}

func (s *Server) ping(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

type tabRequest struct {
	Host string `json:"host"`
	TS   int64  `json:"ts"`
}

func (s *Server) tab(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r) {
		return
	}
	if r.ContentLength > MaxBody {
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
	var req tabRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.More() {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	host := ""
	if req.Host != "" { // "" = no web page in front (browser lost focus, internal page)
		h, ok := domains.Normalize(req.Host)
		if !ok {
			http.Error(w, "bad host", http.StatusBadRequest)
			return
		}
		host = h
	}
	if s.cfg.OnTab != nil {
		s.cfg.OnTab(host, s.cfg.Now())
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}
