package cloudflare

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// Protection is what an ANONYMOUS visitor gets from a published hostname.
//
// Measured rather than read from Cloudflare's policy API: the probe tests the
// behaviour an attacker would actually receive, which is the question worth
// answering, and it needs no credential to ask. Adding credentials to it would
// destroy the thing it measures.
type Protection string

const (
	// Guarded — Cloudflare Access intercepted the request.
	Guarded Protection = "guarded"
	// Public — anonymously reachable ON PURPOSE, per the allowlist.
	Public Protection = "public"
	// Open — anonymously reachable and not meant to be. The only finding.
	Open Protection = "open"
	// NotServed — published, but nothing answers at "/". Normal and NOT a
	// finding: helium routes only /ubo, /ext and /push to separate containers,
	// and calibre publishes only ^/opds through the tunnel, so a bare GET /
	// correctly 404s on both while the services are entirely healthy (verified
	// 2026-09-05: identical 404 internally and through the tunnel).
	//
	// Calling this "open" would alarm daily on correct configuration; calling
	// it "guarded" would be false reassurance. It is neither.
	NotServed Protection = "not-served"
	// Unknown — the probe could not tell. Never treat as safe.
	Unknown Protection = "unknown"
)

type HostnameStatus struct {
	Hostname   string     `json:"hostname"`
	Protection Protection `json:"protection"`
	StatusCode int        `json:"status_code,omitempty"`
	// Reason is why a Public hostname is deliberately not behind Access.
	Reason string `json:"reason,omitempty"`
	Err    string `json:"err,omitempty"`
}

const probeTimeout = 8 * time.Second

// ProbeHostnames asks each hostname anonymously what it does. Results are
// returned in input order, one per hostname, always.
func ProbeHostnames(ctx context.Context, client *http.Client, hostnames []string, publicAllowlist map[string]string) []HostnameStatus {
	allow := make(map[string]string, len(publicAllowlist))
	for h, reason := range publicAllowlist {
		allow[strings.ToLower(h)] = reason
	}

	var transport http.RoundTripper
	if client != nil {
		transport = client.Transport
	}
	// Never follow redirects: the Access login redirect IS the signal, and
	// following it would land on the login page and look like an ordinary 200.
	probeClient := &http.Client{
		Transport: transport,
		Timeout:   probeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	out := make([]HostnameStatus, 0, len(hostnames))
	for _, host := range hostnames {
		reason, isPublic := allow[strings.ToLower(host)]
		out = append(out, probeOne(ctx, probeClient, host, isPublic, reason))
	}
	return out
}

func probeOne(ctx context.Context, client *http.Client, host string, isPublic bool, publicReason string) HostnameStatus {
	res := HostnameStatus{Hostname: host, Protection: Unknown}

	url := host
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	resp, err := client.Do(req)
	if err != nil {
		// Unreachable is Unknown, not Guarded. Silence is not safety.
		res.Err = err.Error()
		return res
	}
	defer resp.Body.Close()
	res.StatusCode = resp.StatusCode

	if isAccessChallenge(resp) {
		res.Protection = Guarded
		return res
	}
	// A 404 means nothing is published at "/", which says nothing about whether
	// the paths that ARE published are guarded.
	if resp.StatusCode == http.StatusNotFound {
		res.Protection = NotServed
		return res
	}
	// The app answered for itself. Whether that is acceptable is a policy
	// question the allowlist answers, not something the probe can infer.
	if isPublic {
		res.Protection = Public
		res.Reason = publicReason
	} else {
		res.Protection = Open
	}
	return res
}

// isAccessChallenge recognises Cloudflare Access intercepting the request.
//
// An app redirecting anonymous users to its own /login is NOT protection — the
// app is still anonymously reachable and still serving. Only a redirect to
// Cloudflare's own Access endpoints counts.
func isAccessChallenge(resp *http.Response) bool {
	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return false
	}
	loc := strings.ToLower(resp.Header.Get("Location"))
	return strings.Contains(loc, "cloudflareaccess.com") || strings.Contains(loc, "/cdn-cgi/access/")
}
