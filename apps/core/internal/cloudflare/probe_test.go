package cloudflare_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"homelab-ngx.com/core/internal/cloudflare"
)

func probeResult(t *testing.T, results []cloudflare.HostnameStatus, host string) cloudflare.HostnameStatus {
	t.Helper()
	for _, r := range results {
		if r.Hostname == host {
			return r
		}
	}
	t.Fatalf("no probe result for %q in %+v", host, results)
	return cloudflare.HostnameStatus{}
}

func probeServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// Cloudflare Access answers an anonymous request with a redirect to the team
// domain. That redirect is the signal, so the probe must NOT follow it.
func TestProbeReportsGuardedOnAnAccessRedirect(t *testing.T) {
	srv := probeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "https://example.cloudflareaccess.com/cdn-cgi/access/login/app")
		w.WriteHeader(http.StatusFound)
	})
	got := probeResult(t, cloudflare.ProbeHostnames(context.Background(), srv.Client(),
		[]string{srv.URL}, nil), srv.URL)
	if got.Protection != cloudflare.Guarded {
		t.Fatalf("protection = %q, want guarded (status %d)", got.Protection, got.StatusCode)
	}
}

// The finding that matters: published through the tunnel with nothing in front.
func TestProbeReportsOpenWhenTheAppAnswersAnonymously(t *testing.T) {
	srv := probeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>the actual app</html>"))
	})
	got := probeResult(t, cloudflare.ProbeHostnames(context.Background(), srv.Client(),
		[]string{srv.URL}, nil), srv.URL)
	if got.Protection != cloudflare.Open {
		t.Fatalf("protection = %q, want open", got.Protection)
	}
}

// Deliberately-public hostnames carry their reason, so the panel can show WHY
// rather than leaving a reader to guess whether it was a decision or an
// oversight.
func TestProbeHonoursThePublicAllowlistAndCarriesTheReason(t *testing.T) {
	srv := probeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>pocket-id login</html>"))
	})
	got := probeResult(t, cloudflare.ProbeHostnames(context.Background(), srv.Client(),
		[]string{srv.URL}, map[string]string{srv.URL: "it is the identity provider"}), srv.URL)
	if got.Protection != cloudflare.Public {
		t.Fatalf("protection = %q, want public", got.Protection)
	}
	if got.Reason != "it is the identity provider" {
		t.Errorf("reason = %q; a public verdict without its reason is just a silenced alarm", got.Reason)
	}
}

// A redirect somewhere other than Cloudflare Access is not protection. An app
// bouncing anonymous users to its own /login is still anonymously reachable.
func TestProbeDoesNotMistakeAnAppLoginRedirectForAccess(t *testing.T) {
	srv := probeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/login")
		w.WriteHeader(http.StatusFound)
	})
	got := probeResult(t, cloudflare.ProbeHostnames(context.Background(), srv.Client(),
		[]string{srv.URL}, nil), srv.URL)
	if got.Protection == cloudflare.Guarded {
		t.Fatal("an app's own /login redirect was mistaken for Access protection")
	}
}

// Verified against the live tunnel: helium routes only /ubo, /ext and /push,
// and calibre publishes only ^/opds, so a bare GET / legitimately 404s on both
// while the services are entirely healthy. Calling that "open" would alarm
// every day on a correct configuration.
func TestProbeDoesNotTreatAnUnservedRootAsExposure(t *testing.T) {
	srv := probeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	got := probeResult(t, cloudflare.ProbeHostnames(context.Background(), srv.Client(),
		[]string{srv.URL}, nil), srv.URL)
	if got.Protection == cloudflare.Open {
		t.Fatal("a 404 root was reported as unprotected exposure")
	}
	if got.Protection == cloudflare.Guarded {
		t.Fatal("a 404 root was reported as guarded, which is false reassurance")
	}
	if got.Protection != cloudflare.NotServed {
		t.Fatalf("protection = %q, want not-served", got.Protection)
	}
}

// An unreachable host is unknown, never "guarded" — silence must not read as
// safety.
func TestProbeReportsUnknownWhenUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	got := probeResult(t, cloudflare.ProbeHostnames(context.Background(), http.DefaultClient,
		[]string{url}, nil), url)
	if got.Protection != cloudflare.Unknown {
		t.Fatalf("protection = %q, want unknown", got.Protection)
	}
	if got.Err == "" {
		t.Error("unreachable host recorded no error")
	}
}

func TestProbeReturnsOneResultPerHostnameInOrder(t *testing.T) {
	srv := probeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	hosts := []string{srv.URL, srv.URL + "/b", srv.URL + "/c"}
	got := cloudflare.ProbeHostnames(context.Background(), srv.Client(), hosts, nil)
	if len(got) != 3 {
		t.Fatalf("got %d results for %d hostnames", len(got), len(hosts))
	}
	for i, h := range hosts {
		if got[i].Hostname != h {
			t.Errorf("result %d is %q, want %q — order must match the input", i, got[i].Hostname, h)
		}
	}
}
