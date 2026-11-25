package bridge

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	extID = "abcdefghijklmnopabcdefghijklmnop"
	token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

type tab struct {
	host string
	at   time.Time
}

type env struct {
	srv  *Server
	tabs []tab
	now  time.Time
}

func newEnv() *env {
	e := &env{now: time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)}
	e.srv = New(Config{Port: 47821, ExtensionIDs: []string{extID}, Token: func() string { return token },
		Now:   func() time.Time { return e.now },
		OnTab: func(h string, at time.Time) { e.tabs = append(e.tabs, tab{h, at}) }})
	return e
}

type req struct {
	method, path, body string
	remote, host       string
	origin, auth       string
	hdr                map[string]string
}

func goodReq(body string) req {
	return req{method: "POST", path: "/v1/tab", body: body, remote: "127.0.0.1:50000", host: "127.0.0.1:47821",
		origin: "chrome-extension://" + extID, auth: "Bearer " + token}
}

func (e *env) do(r req) *httptest.ResponseRecorder {
	hr := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
	hr.RemoteAddr, hr.Host = r.remote, r.host
	if r.origin != "" {
		hr.Header.Set("Origin", r.origin)
	}
	if r.auth != "" {
		hr.Header.Set("Authorization", r.auth)
	}
	for k, v := range r.hdr {
		hr.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(w, hr)
	return w
}

func TestAcceptsTheOwnersExtension(t *testing.T) {
	e := newEnv()
	if w := e.do(goodReq(`{"host":"www.YouTube.com","ts":1}`)); w.Code != 200 {
		t.Fatalf("good request = %d %s", w.Code, w.Body)
	}
	if len(e.tabs) != 1 || e.tabs[0].host != "youtube.com" || !e.tabs[0].at.Equal(e.now) {
		t.Fatalf("tabs %+v", e.tabs)
	}
	// localhost is the same machine; the Host header must still carry the port
	r := goodReq(`{"host":"github.com"}`)
	r.host, r.remote = "localhost:47821", "[::1]:50000"
	if w := e.do(r); w.Code != 200 {
		t.Fatalf("localhost = %d", w.Code)
	}
	// an empty host means "no web page in front"
	if w := e.do(goodReq(`{"host":""}`)); w.Code != 200 || e.tabs[len(e.tabs)-1].host != "" {
		t.Fatalf("empty host = %d %+v", w.Code, e.tabs)
	}
	ping := goodReq("")
	ping.method, ping.path = "GET", "/v1/ping"
	if w := e.do(ping); w.Code != 200 || w.Header().Get("Access-Control-Allow-Origin") != "chrome-extension://"+extID {
		t.Fatalf("ping = %d %v", w.Code, w.Header())
	}
}

func TestRefusesEveryoneElse(t *testing.T) {
	e := newEnv()
	for name, mutate := range map[string]func(*req){
		"a remote machine":         func(r *req) { r.remote = "192.168.1.20:5000" },
		"a public address":         func(r *req) { r.remote = "8.8.8.8:5000" },
		"no port in the peer":      func(r *req) { r.remote = "127.0.0.1" },
		"DNS rebinding host":       func(r *req) { r.host = "evil.example.com:47821" },
		"right name, wrong port":   func(r *req) { r.host = "127.0.0.1:9999" },
		"no port in Host":          func(r *req) { r.host = "127.0.0.1" },
		"another extension":        func(r *req) { r.origin = "chrome-extension://ponmlkjihgfedcbaponmlkjihgfedcba" },
		"a web page":               func(r *req) { r.origin = "https://youtube.com" },
		"no origin (a script)":     func(r *req) { r.origin = "" },
		"origin with a path":       func(r *req) { r.origin = "chrome-extension://" + extID + "/x" },
		"origin with credentials":  func(r *req) { r.origin = "chrome-extension://u:p@" + extID },
		"null origin":              func(r *req) { r.origin = "null" },
		"wildcard origin":          func(r *req) { r.origin = "*" },
		"wrong token":              func(r *req) { r.auth = "Bearer " + strings.Repeat("0", len(token)) },
		"token with one char off":  func(r *req) { r.auth = "Bearer " + token[:len(token)-1] + "x" },
		"shorter token":            func(r *req) { r.auth = "Bearer " + token[:10] },
		"no token":                 func(r *req) { r.auth = "" },
		"token without the scheme": func(r *req) { r.auth = token },
		"basic scheme":             func(r *req) { r.auth = "Basic " + token },
	} {
		r := goodReq(`{"host":"youtube.com"}`)
		mutate(&r)
		w := e.do(r)
		if w.Code != 403 && w.Code != 401 {
			t.Errorf("%s: %d, want 401 or 403", name, w.Code)
		}
		if w.Header().Get("Access-Control-Allow-Origin") == "*" {
			t.Errorf("%s: wildcard CORS", name)
		}
	}
	if len(e.tabs) != 0 {
		t.Fatalf("a refused request reached the agent: %+v", e.tabs)
	}
	// no extension configured, or no token made yet: nothing is accepted
	none := New(Config{Port: 47821, Token: func() string { return token }})
	hr := httptest.NewRequest("POST", "/v1/tab", strings.NewReader(`{"host":"a.com"}`))
	hr.RemoteAddr, hr.Host = "127.0.0.1:1", "127.0.0.1:47821"
	hr.Header.Set("Origin", "chrome-extension://"+extID)
	hr.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	none.Handler().ServeHTTP(w, hr)
	if w.Code != 403 {
		t.Errorf("no extension ids configured = %d", w.Code)
	}
	empty := newEnv()
	empty.srv = New(Config{Port: 47821, ExtensionIDs: []string{extID}, Token: func() string { return "" }})
	if w := empty.do(goodReq(`{"host":"a.com"}`)); w.Code != 401 {
		t.Errorf("empty token = %d", w.Code)
	}
}

func TestBodyRules(t *testing.T) {
	e := newEnv()
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"a URL instead of a host": {`{"host":"https://youtube.com/watch?v=abc"}`, 400},
		"a path":                  {`{"host":"youtube.com/watch"}`, 400},
		"credentials":             {`{"host":"user:pass@youtube.com"}`, 400},
		"a port":                  {`{"host":"youtube.com:8080"}`, 400},
		"an IP address":           {`{"host":"192.168.1.5"}`, 400},
		"localhost":               {`{"host":"localhost"}`, 400},
		"a page title":            {`{"host":"youtube.com","title":"Video lucu - YouTube"}`, 400},
		"a full url field":        {`{"host":"youtube.com","url":"https://youtube.com/x"}`, 400},
		"not json":                {`host=youtube.com`, 400},
		"two documents":           {`{"host":"a.com"}{"host":"b.com"}`, 400},
		"an array":                {`["youtube.com"]`, 400},
		"too large (padding)":     {`{"host":"youtube.com","ts":1}` + strings.Repeat(" ", MaxBody), 413},
		"too large (long host)":   {`{"host":"` + strings.Repeat("a", 2000) + `.com"}`, 413},
		"just under the limit":    {`{"host":"youtube.com","ts":1}` + strings.Repeat(" ", MaxBody-40), 200},
	} {
		w := e.do(goodReq(tc.body))
		if w.Code != tc.want {
			t.Errorf("%s: %d, want %d", name, w.Code, tc.want)
		}
	}
	for _, tb := range e.tabs {
		if tb.host != "youtube.com" {
			t.Errorf("stored %q: only plain hosts may pass", tb.host)
		}
	}
	// a body that lies about its length is still capped while reading
	r := goodReq(strings.Repeat("x", 5000))
	r.hdr = map[string]string{"Content-Length": "10"}
	if w := e.do(r); w.Code == 200 {
		t.Errorf("garbage = %d", w.Code)
	}
}

func TestPreflightIsExact(t *testing.T) {
	e := newEnv()
	r := goodReq("")
	r.method, r.auth = "OPTIONS", ""
	r.hdr = map[string]string{"Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "authorization,content-type"}
	w := e.do(r)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "chrome-extension://"+extID || w.Header().Get("Access-Control-Allow-Methods") != "GET, POST" {
		t.Fatalf("preflight = %d %v", w.Code, w.Header())
	}
	for name, mutate := range map[string]func(*req){
		"web origin":   func(r *req) { r.origin = "https://evil.example" },
		"other ext":    func(r *req) { r.origin = "chrome-extension://zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz" },
		"no origin":    func(r *req) { r.origin = "" },
		"remote peer":  func(r *req) { r.remote = "10.0.0.5:1" },
		"foreign Host": func(r *req) { r.host = "evil.example:47821" },
	} {
		r := goodReq("")
		r.method, r.auth = "OPTIONS", ""
		mutate(&r)
		if w := e.do(r); w.Code == 204 || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s: %d %v", name, w.Code, w.Header())
		}
	}
	// only the two routes exist
	for _, p := range []string{"/", "/v1/tabs", "/debug/pprof", "/v1/tab/../x"} {
		r := goodReq("")
		r.method, r.path = "GET", p
		if w := e.do(r); w.Code == 200 {
			t.Errorf("GET %s = 200", p)
		}
	}
	if got := e.srv.Addr(); got != "127.0.0.1:47821" {
		t.Errorf("addr %q: the bridge must listen on loopback only", got)
	}
	_ = http.StatusOK
}
