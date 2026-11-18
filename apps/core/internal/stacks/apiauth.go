package stacks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/danielgtaylor/huma/v2"

	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/store"
)

// APIPort is the stack listener's TCP port. A stack whose approved manifest
// has an `api` scope may reach it on its host address (.1); its token, its
// source address and the operation's scope decide the rest (APIAuth).
const APIPort = 7781

const (
	// apiEnvDirName holds the token file in a stack's state directory, next
	// to the stored variables. The directory is bound read-only into the
	// stack at stackAPIDir, so the host never writes into the stack's root.
	apiEnvDirName = "homelab-api"
	stackAPIDir   = "/var/lib/homelab-api"
	apiEnvName    = "env"
)

func (s *Service) apiEnvDir(name string) string {
	return filepath.Join(s.stateDir(name), apiEnvDirName)
}
func (s *Service) apiEnvFile(name string) string { return filepath.Join(s.apiEnvDir(name), apiEnvName) }

// apiURL is the stack listener's address as the stack reaches it.
func apiURL(subnet string) (string, error) {
	host, _, err := addrs(subnet)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://%s:%d%s", host, APIPort, httpx.Prefix), nil
}

func apiEnv(url, token string) string {
	return "HOMELAB_API_URL=" + url + "\nHOMELAB_API_TOKEN=" + token + "\n"
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// syncAPIToken makes the stack's token follow its approved manifest. With an
// `api` scope approved, the stack gets a token unless its env file already
// holds the one whose hash is stored, for the current address: a missing
// or stale file means a new token, since only the hash is kept. Without
// one, the hash and the file are deleted.
func (s *Service) syncAPIToken(ctx context.Context, name string) error {
	// An approval and a boot may sync at once; the file and the hash must
	// come from the same token.
	s.auth.issue.Lock()
	defer s.auth.issue.Unlock()
	defer s.auth.invalidate()
	r, err := s.row(ctx, name)
	if err != nil {
		return err
	}
	approved, ok, err := approvedManifest(r)
	if err != nil {
		return err
	}
	if !ok || len(approved.API) == 0 {
		return s.dropAPIToken(ctx, name, r.ApiTokenHash != "")
	}
	subnet, err := s.ensureSubnet(ctx, name)
	if err != nil {
		return err
	}
	url, err := apiURL(subnet)
	if err != nil {
		return err
	}
	if r.ApiTokenHash != "" {
		if b, err := os.ReadFile(s.apiEnvFile(name)); err == nil {
			if tok, ok := parseAPIEnv(string(b), url); ok && subtle.ConstantTimeCompare([]byte(hashToken(tok)), []byte(r.ApiTokenHash)) == 1 {
				return nil
			}
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if err := mkdirs(0o700, s.stateDir(name), s.apiEnvDir(name)); err != nil {
		return err
	}
	if err := writeFile(s.apiEnvFile(name), apiEnv(url, token), 0o400); err != nil {
		return fmt.Errorf("writing the stack's API token: %w", err)
	}
	if err := s.st.Queries().SetStackAPITokenHash(ctx, store.SetStackAPITokenHashParams{Name: name, Hash: hashToken(token)}); err != nil {
		return err
	}
	s.log.Info("stack API token issued", "stack", name, "scopes", strings.Join(approved.API, ","))
	return nil
}

// removeAPIToken deletes a removed stack's token file.
func (s *Service) removeAPIToken(ctx context.Context, name string) error {
	s.auth.issue.Lock()
	defer s.auth.issue.Unlock()
	return s.dropAPIToken(ctx, name, false)
}

// dropAPIToken deletes the stack's token hash (when it has one) and its env
// file. The directory stays: a running stack may still have it bound. The
// caller holds s.auth.issue.
func (s *Service) dropAPIToken(ctx context.Context, name string, hasHash bool) error {
	defer s.auth.invalidate()
	if hasHash {
		if err := s.st.Queries().SetStackAPITokenHash(ctx, store.SetStackAPITokenHashParams{Name: name, Hash: ""}); err != nil {
			return err
		}
		s.log.Info("stack API token revoked", "stack", name)
	}
	if err := os.Remove(s.apiEnvFile(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing the stack's API token: %w", err)
	}
	return nil
}

// parseAPIEnv returns the token of an env file written for url.
func parseAPIEnv(content, url string) (string, bool) {
	var gotURL, token string
	for _, l := range strings.Split(content, "\n") {
		if v, ok := strings.CutPrefix(l, "HOMELAB_API_URL="); ok {
			gotURL = v
		} else if v, ok := strings.CutPrefix(l, "HOMELAB_API_TOKEN="); ok {
			token = v
		}
	}
	return token, gotURL == url && token != ""
}

// syncAPITokenLogged is syncAPIToken after an approval: the approval
// stands either way, and the next start issues the token again.
func (s *Service) syncAPITokenLogged(ctx context.Context, name string) {
	if err := s.syncAPIToken(ctx, name); err != nil {
		s.log.Warn("stack API token not updated", "stack", name, "error", err)
	}
}

// ── the stack listener's authentication ─────────────────────────────────

// apiCaller is a stack that may call the stack listener.
type apiCaller struct {
	name   string
	addr   netip.Addr
	hash   []byte // hex SHA-256 of the token
	scopes []string
}

// apiAuth caches the callers. Every change of a token, an approval or a
// subnet invalidates it, and the next request rebuilds it from the store.
type apiAuth struct {
	issue sync.Mutex // held while a token is issued or revoked

	mu      sync.Mutex
	valid   bool
	callers []apiCaller
}

func (a *apiAuth) invalidate() {
	a.mu.Lock()
	a.valid = false
	a.mu.Unlock()
}

func (s *Service) apiCallers(ctx context.Context) ([]apiCaller, error) {
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	if s.auth.valid {
		return s.auth.callers, nil
	}
	rows, err := s.st.Queries().ListStacks(ctx)
	if err != nil {
		return nil, err
	}
	var out []apiCaller
	for _, r := range rows {
		if r.ApiTokenHash == "" {
			continue
		}
		m, ok, err := approvedManifest(r)
		if err != nil || !ok || len(m.API) == 0 {
			continue
		}
		_, addr, err := addrs(r.Subnet)
		if err != nil {
			continue
		}
		out = append(out, apiCaller{name: r.Name, addr: addr, hash: []byte(r.ApiTokenHash), scopes: m.API})
	}
	s.auth.callers, s.auth.valid = out, true
	return out, nil
}

// caller finds the stack token belongs to. Every caller's hash is compared,
// in constant time, whether or not an earlier one matched.
func (s *Service) caller(ctx context.Context, token string) (apiCaller, bool, error) {
	callers, err := s.apiCallers(ctx)
	if err != nil {
		return apiCaller{}, false, err
	}
	h := []byte(hashToken(token))
	var found apiCaller
	match := 0
	for _, c := range callers {
		if subtle.ConstantTimeCompare(h, c.hash) == 1 {
			found, match = c, 1
		}
	}
	return found, match == 1, nil
}

// APIAuth is the stack listener's middleware. A request needs a stack's
// token as `Authorization: Bearer <token>` (401 unauthorized), must come
// from that stack's address (403 wrong_source), and may call only an
// operation whose scope the stack's approved manifest grants (403
// scope_not_granted). The handler then sees the stack as its caller
// (httpx.CallerStack). With s nil, as when only the document is built,
// every request is refused.
func APIAuth(api huma.API, s *Service) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		refuse := func(p *httpx.Problem) {
			_ = huma.WriteErr(api, ctx, p.Status, p.Detail, p)
		}
		token, ok := bearer(ctx.Header("Authorization"))
		if !ok || s == nil {
			refuse(httpx.Unauthorized("unauthorized", "a stack's API token is required as Authorization: Bearer <token>"))
			return
		}
		c, found, err := s.caller(ctx.Context(), token)
		if err != nil {
			s.log.Error("cannot read the stack API tokens", "error", err)
			refuse(httpx.Internal("internal", "cannot check the token"))
			return
		}
		if !found {
			refuse(httpx.Unauthorized("unauthorized", "the token is not a stack's API token"))
			return
		}
		if from, ok := remoteAddr(ctx.RemoteAddr()); !ok || from != c.addr {
			refuse(httpx.Forbidden("wrong_source", "stack %q's token is accepted only from %s", c.name, c.addr))
			return
		}
		scope := httpx.OperationScope(ctx.Operation())
		if scope == httpx.ScopeOperator || !slices.Contains(c.scopes, string(scope)) {
			refuse(httpx.Forbidden("scope_not_granted", "stack %q has no %q API grant", c.name, scope))
			return
		}
		next(huma.WithContext(ctx, httpx.WithCallerStack(ctx.Context(), c.name)))
	}
}

func bearer(h string) (string, bool) {
	scheme, token, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

func remoteAddr(hostport string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}
