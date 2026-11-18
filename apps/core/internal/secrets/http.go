package secrets

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"homelab-ngx.com/core/internal/httpx"
)

// HTTP is the secrets API adapter.
type HTTP struct{ svc *Service }

// NewHTTP returns the HTTP adapter for svc.
func NewHTTP(svc *Service) *HTTP { return &HTTP{svc: svc} }

// problem maps domain errors: 404 not_found, 400 invalid_name, 409
// git_preflight, 409 already_running. Everything else (sops/git failures)
// carries its message to the client: the operator needs it and it never
// contains the secret value.
func problem(err error) error {
	var in *InvalidNameError
	var pf *PreflightError
	switch {
	case errors.Is(err, ErrNotFound):
		return httpx.NotFound("not_found", "%s", err)
	case errors.As(err, &in):
		return httpx.BadRequest("invalid_name", "%s", in)
	case errors.As(err, &pf):
		return httpx.Conflict("git_preflight", "%s", pf)
	}
	return httpx.Internal("secrets_failed", "%s", err)
}

type (
	listOut  struct{ Body SecretsList }
	secretIn struct {
		Service string `path:"service" doc:"Secret group, e.g. caddy."`
		Key     string `path:"key" doc:"Secret name within the group, e.g. CLOUDFLARE_API_TOKEN."`
	}
	statusOut struct{ Body SecretStatus }
	setIn     struct {
		Service string `path:"service" doc:"Secret group, e.g. caddy."`
		Key     string `path:"key" doc:"Secret name within the group, e.g. CLOUDFLARE_API_TOKEN."`
		Body    SecretSet
	}
	mutationOut struct{ Body SecretMutation }
)

// Register adds the secrets operations. The GETs return metadata only, never
// a value, so they are read scope; changes are operator-only.
func (h *HTTP) Register(a httpx.APIs) {
	tags := []string{"secrets"}
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "listSecrets", Method: http.MethodGet, Path: "/secrets", Tags: tags,
		Summary: "All registered secrets (presence only, never values).",
	}, h.listSecrets)
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getSecret", Method: http.MethodGet, Path: "/secrets/{service}/{key}", Tags: tags,
		Summary: "Metadata of one secret.",
	}, h.getSecret)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "setSecret", Method: http.MethodPut, Path: "/secrets/{service}/{key}", Tags: tags,
		Summary: "Set or rotate a secret; commits the encrypted change.",
		Description: "`409 git_preflight` when the repo is not in a state that allows a commit.\n" +
			"`description` is accepted for compatibility and ignored.",
	}, h.setSecret)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "deleteSecret", Method: http.MethodDelete, Path: "/secrets/{service}/{key}", Tags: tags,
		Summary:       "Wipe a secret (sets it to the empty string); commits the change.",
		Description:   "`409 git_preflight` when the repo is not in a state that allows a commit.",
		DefaultStatus: http.StatusOK,
	}, h.deleteSecret)
}

func (h *HTTP) listSecrets(ctx context.Context, _ *struct{}) (*listOut, error) {
	l, err := h.svc.List(ctx)
	if err != nil {
		return nil, problem(err)
	}
	return &listOut{Body: l}, nil
}

func (h *HTTP) getSecret(ctx context.Context, in *secretIn) (*statusOut, error) {
	st, err := h.svc.Get(ctx, in.Service+"/"+in.Key)
	if err != nil {
		return nil, problem(err)
	}
	return &statusOut{Body: st}, nil
}

func mutation(r GitResult) *mutationOut {
	w := r.Warnings
	if w == nil {
		w = []string{}
	}
	return &mutationOut{Body: SecretMutation{DeployPending: true, Branch: r.Branch, Commit: r.Commit, PushPending: r.PushPending, Warnings: w}}
}

func (h *HTTP) setSecret(ctx context.Context, in *setIn) (*mutationOut, error) {
	r, err := h.svc.Set(ctx, in.Service, in.Key, in.Body.Value)
	if err != nil {
		return nil, problem(err)
	}
	return mutation(r), nil
}

func (h *HTTP) deleteSecret(ctx context.Context, in *secretIn) (*mutationOut, error) {
	r, err := h.svc.Delete(ctx, in.Service, in.Key)
	if err != nil {
		return nil, problem(err)
	}
	return mutation(r), nil
}
