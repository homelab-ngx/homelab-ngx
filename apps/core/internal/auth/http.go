package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"homelab-ngx.com/core/internal/httpx"
)

// HTTP is the sign-in slice of the operator API.
type HTTP struct{ s *Service }

// NewHTTP returns the HTTP adapter for s.
func NewHTTP(s *Service) *HTTP { return &HTTP{s: s} }

// Status is whether the dashboard has a password and the caller a session.
type Status struct {
	PasswordSet bool `json:"password_set" doc:"A password is set; without one, the next visitor sets it."`
	SignedIn    bool `json:"signed_in" doc:"The caller has a live session (always true on the CLI's socket)."`
}

type passwordIn struct {
	Body struct {
		Password string `json:"password" maxLength:"256"`
	}
}

type changeIn struct {
	Body struct {
		Current  string `json:"current_password,omitempty" maxLength:"256" doc:"The password now; required from the dashboard, not from the CLI."`
		Password string `json:"password" maxLength:"256"`
	}
}

type statusOut struct {
	Body Status
}

type sessionOut struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      Status
}

type changeOut struct {
	SetCookie []http.Cookie `header:"Set-Cookie"`
	Body      Status
}

type logoutOut struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
}

// Register adds the sign-in operations. They are operator operations: the
// stack API never has them.
func (h *HTTP) Register(a httpx.APIs) {
	tags := []string{"auth"}
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "getAuthStatus", Method: http.MethodGet, Path: "/auth", Tags: tags,
		Summary: "Whether a password is set and the caller is signed in.",
	}, h.status)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "setupAuth", Method: http.MethodPost, Path: "/auth/setup", Tags: tags,
		Summary:     "Set the first password and sign in.",
		Description: "Only while no password is set (`409 password_set` after). Dashboard only.",
		Errors:      []int{http.StatusBadRequest, http.StatusForbidden, http.StatusConflict},
	}, h.setup)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "login", Method: http.MethodPost, Path: "/auth/login", Tags: tags,
		Summary:     "Sign in with the password.",
		Description: "`429 too_many_attempts` after five wrong passwords in 15 minutes. Dashboard only.",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusTooManyRequests},
	}, h.login)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "logout", Method: http.MethodPost, Path: "/auth/logout", Tags: tags,
		Summary: "End this session. Dashboard only.", DefaultStatus: http.StatusNoContent,
		Errors: []int{http.StatusForbidden},
	}, h.logout)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "changePassword", Method: http.MethodPut, Path: "/auth/password", Tags: tags,
		Summary:     "Change the password and end every session.",
		Description: "From the dashboard it needs the current password and signs this browser in again; the CLI sets it outright.",
		Errors:      []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusConflict, http.StatusTooManyRequests},
	}, h.change)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "resetAuth", Method: http.MethodDelete, Path: "/auth/password", Tags: tags,
		Summary:       "Remove the password and every session; the next visitor sets one.",
		Description:   "CLI only (`403 cli_only` from the dashboard).",
		DefaultStatus: http.StatusNoContent, Errors: []int{http.StatusForbidden},
	}, h.reset)
}

func (h *HTTP) status(ctx context.Context, _ *struct{}) (*statusOut, error) {
	set, err := h.s.PasswordSet(ctx)
	if err != nil {
		return nil, err
	}
	signedIn := true
	if w, ok := fromWeb(ctx); ok {
		if signedIn, err = h.s.Valid(ctx, w.token); err != nil {
			return nil, err
		}
	}
	return &statusOut{Body: Status{PasswordSet: set, SignedIn: signedIn}}, nil
}

func (h *HTTP) setup(ctx context.Context, in *passwordIn) (*sessionOut, error) {
	w, err := web(ctx)
	if err != nil {
		return nil, err
	}
	token, err := h.s.Setup(ctx, in.Body.Password)
	if err != nil {
		return nil, problem(err)
	}
	return &sessionOut{SetCookie: sessionCookie(w, token), Body: Status{PasswordSet: true, SignedIn: true}}, nil
}

func (h *HTTP) login(ctx context.Context, in *passwordIn) (*sessionOut, error) {
	w, err := web(ctx)
	if err != nil {
		return nil, err
	}
	token, err := h.s.Login(ctx, w.client, in.Body.Password)
	if err != nil {
		return nil, problem(err)
	}
	return &sessionOut{SetCookie: sessionCookie(w, token), Body: Status{PasswordSet: true, SignedIn: true}}, nil
}

func (h *HTTP) logout(ctx context.Context, _ *struct{}) (*logoutOut, error) {
	w, err := web(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.s.Logout(ctx, w.token); err != nil {
		return nil, err
	}
	c := sessionCookie(w, "")
	c.MaxAge = -1
	return &logoutOut{SetCookie: c}, nil
}

func (h *HTTP) change(ctx context.Context, in *changeIn) (*changeOut, error) {
	out := &changeOut{Body: Status{PasswordSet: true, SignedIn: true}}
	w, ok := fromWeb(ctx)
	if !ok {
		if err := h.s.SetPassword(ctx, in.Body.Password); err != nil {
			return nil, problem(err)
		}
		return out, nil
	}
	token, err := h.s.ChangePassword(ctx, w.client, in.Body.Current, in.Body.Password)
	if err != nil {
		return nil, problem(err)
	}
	out.SetCookie = []http.Cookie{sessionCookie(w, token)}
	return out, nil
}

func (h *HTTP) reset(ctx context.Context, _ *struct{}) (*struct{}, error) {
	if _, ok := fromWeb(ctx); ok {
		return nil, httpx.Forbidden("cli_only", "removing the password is done with `homelab auth reset` on the host")
	}
	return nil, h.s.Reset(ctx)
}

// web is the request's web side, or 403 web_only on the CLI's socket.
func web(ctx context.Context) (webRequest, error) {
	w, ok := fromWeb(ctx)
	if !ok {
		return w, httpx.Forbidden("web_only", "signing in is for the dashboard; the CLI needs none")
	}
	return w, nil
}

// cookieMaxAge is the browser's cap on a cookie's life. The session's own
// expiry, kept by the daemon, is what ends it.
const cookieMaxAge = 400 * 24 * time.Hour

func sessionCookie(w webRequest, token string) http.Cookie {
	return http.Cookie{
		Name: cookieName(w.secure), Value: token, Path: "/",
		MaxAge: int(cookieMaxAge.Seconds()), HttpOnly: true, Secure: w.secure, SameSite: http.SameSiteStrictMode,
	}
}

// problem maps the service's errors onto problems.
func problem(err error) error {
	var tm *TooManyError
	switch {
	case errors.As(err, &tm):
		return httpx.TooManyRequests("too_many_attempts", "%s", err).With("retry_after", int(tm.Wait.Seconds())+1)
	case errors.Is(err, ErrWrongPassword):
		return httpx.Unauthorized("wrong_password", "wrong password")
	case errors.Is(err, ErrWeakPassword):
		return httpx.BadRequest("weak_password", "%s", strings.TrimPrefix(err.Error(), ErrWeakPassword.Error()+": "))
	case errors.Is(err, ErrPasswordSet):
		return httpx.Conflict("password_set", "a password is already set; sign in instead")
	case errors.Is(err, ErrNoPassword):
		return httpx.Conflict("no_password", "no password is set yet")
	}
	return err
}
