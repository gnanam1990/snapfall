package ownerapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ownerRequest is httptest.NewRequest addressed the way the owner's own tooling addresses
// the daemon: Host is the loopback bind, and a request with a body is JSON.
func ownerRequest(method, target string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	r.Host = "127.0.0.1:4010"
	if r.ContentLength > 0 {
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}

const guardToken = "tttttttttttttttttttttttttttttttttttttttt"

// guardRig wires every seam the guard tests touch, in tokenless or token mode.
func guardRig(t *testing.T, withToken bool, allowedHosts string) http.Handler {
	t.Helper()
	if withToken {
		t.Setenv("SNAPFALL_OWNER_TOKEN", guardToken)
	} else {
		t.Setenv("SNAPFALL_OWNER_TOKEN", "")
	}
	t.Setenv(allowedHostsEnv, allowedHosts)
	s, _ := newAPI(t)
	tok := wireAccept(s)
	*tok = "act_" + strings.Repeat("cd", 32) // a minted customer credential
	s.HireWorker = func(context.Context, HireWorkerRequest) (HireWorkerResult, error) {
		return HireWorkerResult{JobID: "milestone_x", State: "assigned"}, nil
	}
	return s.Handler()
}

type guardCase struct {
	name    string
	method  string
	path    string
	host    string
	headers map[string]string
	body    string
	// customer routes authenticate with the job credential, owner routes with the owner
	// bearer when the rig has one.
	customer bool
}

const hireBody = `{"repository":"/tmp/x","quoteUsdc":"1.00","by":"owner"}`

func serveGuardCase(t *testing.T, h http.Handler, withToken bool, tc guardCase) (*httptest.ResponseRecorder, string) {
	t.Helper()
	var body io.Reader
	if tc.body != "" {
		body = strings.NewReader(tc.body)
	}
	r := httptest.NewRequest(tc.method, tc.path, body)
	r.Host = tc.host
	for k, v := range tc.headers {
		r.Header.Set(k, v)
	}
	switch {
	case tc.customer:
		r.Header.Set("Authorization", "Bearer act_"+strings.Repeat("cd", 32))
	case withToken:
		r.Header.Set("Authorization", "Bearer "+guardToken)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out.Error.Code
}

// Every browser-relayed or foreign-Host shape is refused before auth, in BOTH modes, on
// owner and customer routes alike — the CSRF and DNS-rebinding shapes of the finding.
func TestGuard_RefusesBrowserAndForeignHostRequests(t *testing.T) {
	const lo = "127.0.0.1:4010"
	cases := []struct {
		guardCase
		status int
		code   string
	}{
		// DNS rebinding: the page's own hostname arrives in Host.
		{guardCase{name: "rebound host reads inbox", method: "GET", path: "/api/v1/approvals", host: "attacker.example:4010"}, 403, "BAD_HOST"},
		{guardCase{name: "rebound host hires", method: "POST", path: "/api/v1/workforce/build-monitor/hire", host: "attacker.example:4010", body: hireBody, headers: map[string]string{"Content-Type": "application/json"}}, 403, "BAD_HOST"},
		{guardCase{name: "rebound host on SSE", method: "GET", path: "/api/v1/events/stream", host: "attacker.example"}, 403, "BAD_HOST"},
		{guardCase{name: "loopback-looking subdomain", method: "GET", path: "/api/v1/health", host: "127.0.0.1.attacker.example:4010"}, 403, "BAD_HOST"},
		{guardCase{name: "localhost subdomain", method: "GET", path: "/api/v1/health", host: "localhost.attacker.example"}, 403, "BAD_HOST"},
		{guardCase{name: "unconfigured LAN address", method: "GET", path: "/api/v1/health", host: "192.168.1.5:4010"}, 403, "BAD_HOST"},
		{guardCase{name: "empty host", method: "GET", path: "/api/v1/health", host: ""}, 403, "BAD_HOST"},
		{guardCase{name: "rebound host on customer accept", method: "POST", path: "/api/v1/customer/jobs/job_acc/accept", host: "attacker.example:4010", customer: true}, 403, "BAD_HOST"},

		// Cross-origin browser requests to the real loopback Host.
		{guardCase{name: "cross-origin decision as text/plain", method: "POST", path: "/api/v1/approvals/apr_x/decision", host: lo,
			headers: map[string]string{"Origin": "https://attacker.example", "Content-Type": "text/plain"}, body: `{"kind":"approve"}`}, 403, "CROSS_ORIGIN"},
		{guardCase{name: "cross-origin bodyless advance", method: "POST", path: "/api/v1/jobs/job_x/advance", host: lo,
			headers: map[string]string{"Origin": "https://attacker.example"}}, 403, "CROSS_ORIGIN"},
		{guardCase{name: "another local app's origin", method: "POST", path: "/api/v1/jobs/job_acc/accept-link", host: lo,
			headers: map[string]string{"Origin": "http://localhost:3000", "Content-Type": "application/json"}}, 403, "CROSS_ORIGIN"},
		{guardCase{name: "same host other port origin", method: "POST", path: "/api/v1/jobs/job_acc/accept-link", host: lo,
			headers: map[string]string{"Origin": "http://127.0.0.1:5000"}}, 403, "CROSS_ORIGIN"},
		{guardCase{name: "opaque null origin", method: "POST", path: "/api/v1/jobs/job_x/invoice", host: lo,
			headers: map[string]string{"Origin": "null"}}, 403, "CROSS_ORIGIN"},
		{guardCase{name: "cross-site fetch metadata without origin", method: "GET", path: "/api/v1/approvals", host: lo,
			headers: map[string]string{"Sec-Fetch-Site": "cross-site"}}, 403, "CROSS_ORIGIN"},
		{guardCase{name: "same-site fetch metadata", method: "POST", path: "/api/v1/jobs/job_x/advance", host: lo,
			headers: map[string]string{"Sec-Fetch-Site": "same-site"}}, 403, "CROSS_ORIGIN"},
		{guardCase{name: "cross-origin EventSource", method: "GET", path: "/api/v1/events/stream", host: lo,
			headers: map[string]string{"Origin": "https://attacker.example"}}, 403, "CROSS_ORIGIN"},
		{guardCase{name: "cross-origin customer accept", method: "POST", path: "/api/v1/customer/jobs/job_acc/accept", host: lo, customer: true,
			headers: map[string]string{"Origin": "https://attacker.example", "Sec-Fetch-Site": "cross-site"}}, 403, "CROSS_ORIGIN"},

		// CORS-simple bodies are refused even with no browser headers at all.
		{guardCase{name: "text/plain JSON body", method: "POST", path: "/api/v1/workforce/build-monitor/hire", host: lo,
			headers: map[string]string{"Content-Type": "text/plain"}, body: hireBody}, 415, "UNSUPPORTED_MEDIA_TYPE"},
		{guardCase{name: "form body", method: "POST", path: "/api/v1/approvals/apr_x/decision", host: lo,
			headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, body: "kind=approve"}, 415, "UNSUPPORTED_MEDIA_TYPE"},
		{guardCase{name: "multipart body", method: "POST", path: "/api/v1/workforce/build-monitor/hire", host: lo,
			headers: map[string]string{"Content-Type": "multipart/form-data; boundary=x"}, body: "--x--"}, 415, "UNSUPPORTED_MEDIA_TYPE"},
		{guardCase{name: "body with no content type", method: "POST", path: "/api/v1/workforce/build-monitor/hire", host: lo, body: hireBody}, 415, "UNSUPPORTED_MEDIA_TYPE"},
	}
	for _, withToken := range []bool{false, true} {
		mode := map[bool]string{false: "tokenless", true: "token"}[withToken]
		h := guardRig(t, withToken, "")
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				w, code := serveGuardCase(t, h, withToken, tc.guardCase)
				if w.Code != tc.status || code != tc.code {
					t.Fatalf("got %d %s, want %d %s (%s)", w.Code, code, tc.status, tc.code, w.Body.String())
				}
			})
		}
	}
}

// The shapes the owner's own tooling sends still work: curl and Go clients (no Origin, no
// fetch metadata), the dashboard's server-side Node proxy (Sec-Fetch-Mode: cors, JSON
// Content-Type, empty body, no Origin), a same-origin browser request, and loopback
// reached by another name or through a tunnel.
func TestGuard_AllowsOwnerToolingShapes(t *testing.T) {
	nodeProxy := map[string]string{"Content-Type": "application/json", "Sec-Fetch-Mode": "cors", "Accept": "application/json"}
	cases := []struct {
		guardCase
		status int
	}{
		{guardCase{name: "curl inbox read", method: "GET", path: "/api/v1/approvals", host: "127.0.0.1:4010"}, 200},
		{guardCase{name: "localhost name", method: "GET", path: "/api/v1/health", host: "localhost:4010"}, 200},
		{guardCase{name: "IPv6 loopback", method: "GET", path: "/api/v1/health", host: "[::1]:4010"}, 200},
		{guardCase{name: "SSH tunnel port", method: "GET", path: "/api/v1/health", host: "127.0.0.1:5000"}, 200},
		{guardCase{name: "typed into the address bar", method: "GET", path: "/api/v1/health", host: "127.0.0.1:4010",
			headers: map[string]string{"Sec-Fetch-Site": "none"}}, 200},
		{guardCase{name: "same-origin browser request", method: "POST", path: "/api/v1/workforce/build-monitor/hire", host: "127.0.0.1:4010",
			headers: map[string]string{"Origin": "http://127.0.0.1:4010", "Sec-Fetch-Site": "same-origin", "Content-Type": "application/json"}, body: hireBody}, 201},
		{guardCase{name: "curl JSON hire with charset", method: "POST", path: "/api/v1/workforce/build-monitor/hire", host: "127.0.0.1:4010",
			headers: map[string]string{"Content-Type": "application/json; charset=utf-8"}, body: hireBody}, 201},
		{guardCase{name: "curl bodyless mint (spine_run)", method: "POST", path: "/api/v1/jobs/job_acc/accept-link", host: "127.0.0.1:4010"}, 200},
		{guardCase{name: "dashboard proxy mint", method: "POST", path: "/api/v1/jobs/job_acc/accept-link", host: "127.0.0.1:4010", headers: nodeProxy}, 200},
		{guardCase{name: "dashboard proxy customer accept", method: "POST", path: "/api/v1/customer/jobs/job_acc/accept", host: "127.0.0.1:4010", headers: nodeProxy, customer: true}, 200},
		{guardCase{name: "dashboard proxy customer read", method: "GET", path: "/api/v1/customer/jobs/job_acc/acceptance", host: "127.0.0.1:4010",
			headers: map[string]string{"Sec-Fetch-Mode": "cors"}, customer: true}, 200},
		{guardCase{name: "curl bodyless customer accept (spine_run)", method: "POST", path: "/api/v1/customer/jobs/job_acc/accept", host: "127.0.0.1:4010", customer: true}, 200},
	}
	for _, withToken := range []bool{false, true} {
		mode := map[bool]string{false: "tokenless", true: "token"}[withToken]
		h := guardRig(t, withToken, "")
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				w, code := serveGuardCase(t, h, withToken, tc.guardCase)
				if w.Code != tc.status {
					t.Fatalf("got %d %s, want %d (%s)", w.Code, code, tc.status, w.Body.String())
				}
			})
		}
	}
}

// A non-loopback deployment names its public host(s) explicitly; nothing else is implied.
func TestGuard_ConfiguredHosts(t *testing.T) {
	h := guardRig(t, true, "daemon.example, Ops.Example:8443")
	cases := []struct {
		guardCase
		status int
	}{
		{guardCase{name: "configured host any port", method: "GET", path: "/api/v1/health", host: "daemon.example"}, 200},
		{guardCase{name: "configured host is case-insensitive", method: "GET", path: "/api/v1/health", host: "DAEMON.example:443"}, 200},
		{guardCase{name: "configured host:port", method: "GET", path: "/api/v1/health", host: "ops.example:8443"}, 200},
		{guardCase{name: "configured host wrong port", method: "GET", path: "/api/v1/health", host: "ops.example:9999"}, 403},
		{guardCase{name: "suffix of a configured host", method: "GET", path: "/api/v1/health", host: "evil-daemon.example"}, 403},
		{guardCase{name: "same-origin through the proxy", method: "POST", path: "/api/v1/jobs/job_acc/accept-link", host: "daemon.example",
			headers: map[string]string{"Origin": "https://daemon.example", "Sec-Fetch-Site": "same-origin"}}, 200},
		{guardCase{name: "foreign origin through the proxy", method: "POST", path: "/api/v1/jobs/job_acc/accept-link", host: "daemon.example",
			headers: map[string]string{"Origin": "https://attacker.example"}}, 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if w, code := serveGuardCase(t, h, true, tc.guardCase); w.Code != tc.status {
				t.Fatalf("got %d %s, want %d", w.Code, code, tc.status)
			}
		})
	}
}

// The startup half: where the daemon may bind, and which Hosts that implies.
func TestGuard_BindPosture(t *testing.T) {
	cases := []struct {
		name, addr, token, hosts string
		ok                       bool
		allowsHost               string
	}{
		{name: "loopback tokenless", addr: "127.0.0.1:4010", ok: true},
		{name: "localhost tokenless", addr: "localhost:4010", ok: true},
		{name: "IPv6 loopback tokenless", addr: "[::1]:4010", ok: true},
		{name: "LAN bind without token", addr: "192.168.1.5:4010", ok: false},
		{name: "LAN bind with token allows its own address", addr: "192.168.1.5:4010", token: guardToken, ok: true, allowsHost: "192.168.1.5:4010"},
		{name: "wildcard bind with token but no hosts", addr: "0.0.0.0:4010", token: guardToken, ok: false},
		{name: "empty-host bind with token but no hosts", addr: ":4010", token: guardToken, ok: false},
		{name: "wildcard bind with token and hosts", addr: "0.0.0.0:4010", token: guardToken, hosts: "daemon.example", ok: true, allowsHost: "daemon.example"},
		{name: "allowed hosts without token", addr: "127.0.0.1:4010", hosts: "daemon.example", ok: false},
		{name: "short token", addr: "0.0.0.0:4010", token: "short", hosts: "daemon.example", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SNAPFALL_OWNER_TOKEN", tc.token)
			t.Setenv(allowedHostsEnv, tc.hosts)
			s, _ := newAPI(t)
			err := s.checkBindPosture(tc.addr)
			if (err == nil) != tc.ok {
				t.Fatalf("checkBindPosture(%q) err=%v, want ok=%v", tc.addr, err, tc.ok)
			}
			if tc.allowsHost != "" && !s.hostAllowed(tc.allowsHost) {
				t.Fatalf("host %q not allowed after binding %q", tc.allowsHost, tc.addr)
			}
		})
	}
}

// Over a real socket, the PoC's request shapes: what the server sees as Host is what the
// client sent, so a rebound name is refused and the loopback address it listens on is not.
func TestGuard_RealSocketReplaysTheFinding(t *testing.T) {
	h := guardRig(t, false, "")
	srv := httptest.NewServer(h)
	defer srv.Close()
	send := func(method, path, host, origin, ctype, body string) int {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if host != "" {
			req.Host = host
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	const hire = "/api/v1/workforce/build-monitor/hire"
	if c := send("POST", hire, "attacker.example:4010", "https://attacker.example", "text/plain", hireBody); c != 403 {
		t.Fatalf("PoC hire: %d, want 403", c)
	}
	if c := send("GET", "/api/v1/approvals", "attacker.example:4010", "", "", ""); c != 403 {
		t.Fatalf("PoC rebound inbox read: %d, want 403", c)
	}
	if c := send("POST", hire, "", "https://attacker.example", "text/plain", hireBody); c != 403 {
		t.Fatalf("cross-origin hire on the loopback host: %d, want 403", c)
	}
	if c := send("GET", "/api/v1/approvals", "", "", "", ""); c != 200 {
		t.Fatalf("loopback inbox read: %d, want 200", c)
	}
	if c := send("POST", hire, "", "", "application/json", hireBody); c != 201 {
		t.Fatalf("loopback JSON hire: %d, want 201", c)
	}
}
