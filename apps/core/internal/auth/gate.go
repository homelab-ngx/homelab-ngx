package auth

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"homelab-ngx.com/core/internal/httpx"
)

// The session cookie. Over HTTPS it carries the __Host- prefix, which a
// sibling name under the same domain (a stack's site) cannot set. Plain
// HTTP, before the host has a domain, cannot use the prefix.
const (
	cookieSecure = "__Host-homelab_session"
	cookiePlain  = "homelab_session"
)

// webRequest is what Gate learned about a request on the web socket.
type webRequest struct {
	client string // the browser's address, as Caddy saw it
	secure bool   // the browser reached Caddy over HTTPS
	token  string // the session cookie's value, if any
}

type webKey struct{}

// fromWeb returns the web request ctx belongs to; false on the CLI's socket.
func fromWeb(ctx context.Context) (webRequest, bool) {
	w, ok := ctx.Value(webKey{}).(webRequest)
	return w, ok
}

// open are the operations a browser may call without a session.
var open = map[string]bool{
	"GET " + httpx.Prefix + "/auth":         true,
	"POST " + httpx.Prefix + "/auth/setup":  true,
	"POST " + httpx.Prefix + "/auth/login":  true,
	"POST " + httpx.Prefix + "/auth/logout": true,
}

// Gate is the web socket's middleware: only Caddy can open that socket, so
// every request on it is a browser's. A request from another origin is
// refused (403 cross_origin). Past the sign-in operations, a request needs
// a live session: 401 setup_required while no password is set, 401
// login_required after.
func Gate(s *Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wr := webRequest{client: clientAddr(r), secure: r.Header.Get("X-Forwarded-Proto") == "https"}
			if c, err := r.Cookie(cookieName(wr.secure)); err == nil {
				wr.token = c.Value
			}
			if !sameOrigin(r) {
				httpx.Write(w, httpx.Forbidden("cross_origin", "the dashboard's API answers only the dashboard's own pages"))
				return
			}
			ctx := context.WithValue(r.Context(), webKey{}, wr)
			r = r.WithContext(ctx)
			if open[r.Method+" "+r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			ok, err := s.Valid(ctx, wr.token)
			if err != nil {
				s.log.Error("cannot check a dashboard session", "error", err)
				httpx.Write(w, httpx.Internal("internal", "cannot check the session"))
				return
			}
			if ok {
				next.ServeHTTP(w, r)
				return
			}
			set, err := s.PasswordSet(ctx)
			switch {
			case err != nil:
				s.log.Error("cannot read the dashboard password", "error", err)
				httpx.Write(w, httpx.Internal("internal", "cannot check the session"))
			case !set:
				httpx.Write(w, httpx.Unauthorized("setup_required", "set a password for the dashboard first"))
			default:
				httpx.Write(w, httpx.Unauthorized("login_required", "sign in to the dashboard"))
			}
		})
	}
}

func cookieName(secure bool) string {
	if secure {
		return cookieSecure
	}
	return cookiePlain
}

// sameOrigin reports whether r comes from a page of the dashboard's own
// origin. A browser says so in Sec-Fetch-Site over HTTPS. Without it (plain
// HTTP, or a script), a read passes and anything else needs an Origin that
// names this host. A stack's site under the same domain is same-site, not
// same-origin, so it is refused too.
func sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "":
	default:
		return false
	}
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.Header.Get("Upgrade") == "" {
		return true
	}
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	u, err := url.Parse(o)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

// clientAddr is the browser's address: the last X-Forwarded-For entry,
// which Caddy writes itself.
func clientAddr(r *http.Request) string {
	xff := r.Header.Values("X-Forwarded-For")
	if len(xff) > 0 {
		parts := strings.Split(xff[len(xff)-1], ",")
		if a := strings.TrimSpace(parts[len(parts)-1]); a != "" {
			return a
		}
	}
	return r.RemoteAddr
}
