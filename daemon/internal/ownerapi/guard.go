package ownerapi

import (
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// The request guard runs in front of EVERY route, owner and customer, in token and
// tokenless mode alike. It answers a question the bearer token does not: did this request
// come from the owner's own tooling, or from a web page relayed through the owner's
// browser? Loopback reachability alone is not owner identity. Any page the owner opens can
// send CORS-simple POSTs to 127.0.0.1, and a page that DNS-rebinds its own hostname to
// 127.0.0.1 can also read the responses. Three checks close that, each one independent:
//
//   - Host allowlist. A rebound page reaches the daemon with ITS hostname in Host, so only
//     loopback names (127.0.0.0/8, ::1, localhost, on any port: SSH tunnels and port maps
//     legitimately change it) and the hosts named in SNAPFALL_OWNER_ALLOWED_HOSTS pass.
//   - Browser cross-origin. Browsers send Origin on every non-GET request and Sec-Fetch-Site
//     on every request. An Origin that is not this same host, or a Sec-Fetch-Site other
//     than same-origin/none, is refused. No Origin and no Sec-Fetch-Site is the non-browser
//     shape (curl, the dashboard's server-side proxy, Go clients) and passes.
//   - JSON only. A mutating request with a body must be application/json, which a page
//     cannot send cross-origin without a CORS preflight this server never answers. A
//     bodyless POST with no Content-Type (curl -X POST) is still accepted; a browser cannot
//     send one without the Origin header the previous check refuses.

// allowedHostsEnv names the extra Host values (comma-separated, "host" for any port or
// "host:port" for one) a non-loopback deployment is reached by, for example the public
// name a TLS reverse proxy forwards. Setting it requires SNAPFALL_OWNER_TOKEN.
const allowedHostsEnv = "SNAPFALL_OWNER_ALLOWED_HOSTS"

// parseAllowedHosts normalizes the env list; empty entries are dropped.
func parseAllowedHosts(raw string) []string {
	var out []string
	for _, e := range strings.Split(raw, ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// splitHost lowercases a Host value and splits off the port, accepting a bare host,
// host:port, and bracketed IPv6 with or without a port.
func splitHost(hostport string) (host, port string) {
	hostport = strings.ToLower(strings.TrimSpace(hostport))
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		return strings.Trim(h, "[]"), p
	}
	return strings.Trim(hostport, "[]"), ""
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// hostAllowed reports whether a request's Host names this daemon.
func (s *Server) hostAllowed(hostport string) bool {
	host, port := splitHost(hostport)
	if host == "" {
		return false
	}
	if isLoopbackHost(host) {
		return true
	}
	for _, e := range s.allowedHosts {
		eh, ep := splitHost(e)
		if eh == host && (ep == "" || ep == port) {
			return true
		}
	}
	return false
}

// sameOrigin reports whether an Origin header names the very host the request was sent
// to. Scheme is not compared: a TLS-terminating proxy turns https at the edge into http
// here. "null" and anything that is not an http(s) origin fail.
func sameOrigin(origin, requestHost string) bool {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return false
	}
	oh, op := splitHost(u.Host)
	rh, rp := splitHost(requestHost)
	return oh == rh && op == rp
}

func isSafeMethod(m string) bool { return m == http.MethodGet || m == http.MethodHead }

// guard applies the three checks above, before any authentication.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hostAllowed(r.Host) {
			writeErr(w, http.StatusForbidden, "BAD_HOST",
				"the owner API answers loopback hosts and "+allowedHostsEnv+" only", nil)
			return
		}
		if site := strings.ToLower(r.Header.Get("Sec-Fetch-Site")); site != "" && site != "same-origin" && site != "none" {
			writeErr(w, http.StatusForbidden, "CROSS_ORIGIN", "cross-site browser requests are refused", nil)
			return
		}
		if origins, present := r.Header["Origin"]; present {
			if len(origins) != 1 || !sameOrigin(origins[0], r.Host) {
				writeErr(w, http.StatusForbidden, "CROSS_ORIGIN", "cross-origin browser requests are refused", nil)
				return
			}
		}
		if !isSafeMethod(r.Method) {
			if ct := r.Header.Get("Content-Type"); ct != "" {
				if mt, _, err := mime.ParseMediaType(ct); err != nil || mt != "application/json" {
					writeErr(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE",
						"request bodies must be application/json", nil)
					return
				}
			} else if r.ContentLength != 0 {
				writeErr(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE",
					"a request with a body must declare Content-Type: application/json", nil)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// checkBindPosture is Run's startup half of the posture, before listening. A non-loopback
// bind needs the token (H2 §1) and must know the Host its clients will send: a specific
// bind address is allowed automatically, a wildcard bind needs SNAPFALL_OWNER_ALLOWED_HOSTS.
// Extra allowed hosts mean non-loopback clients, so they too require the token.
func (s *Server) checkBindPosture(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("owner api addr %q: %w", addr, err)
	}
	if len(s.allowedHosts) > 0 && len(s.token) < 32 {
		return fmt.Errorf("owner api: %s is set but SNAPFALL_OWNER_TOKEN (>=32 bytes) is not; non-loopback clients require the bearer token", allowedHostsEnv)
	}
	h := strings.Trim(strings.ToLower(host), "[]")
	if isLoopbackHost(h) {
		return nil
	}
	if len(s.token) < 32 {
		return fmt.Errorf("owner api: refusing non-loopback bind %q without SNAPFALL_OWNER_TOKEN (>=32 bytes) — H2 §1", addr)
	}
	if ip := net.ParseIP(h); h == "" || (ip != nil && ip.IsUnspecified()) {
		if len(s.allowedHosts) == 0 {
			return fmt.Errorf("owner api: wildcard bind %q needs %s naming the host(s) clients connect to", addr, allowedHostsEnv)
		}
		return nil
	}
	s.allowedHosts = append(s.allowedHosts, net.JoinHostPort(h, port))
	return nil
}
