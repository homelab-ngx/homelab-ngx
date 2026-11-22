package drafts

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"

	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/llm"
)

// HTTP is the drafts slice of the daemon API. Every operation is the
// operator's: a draft becomes code the host builds.
type HTTP struct{ svc *Service }

// NewHTTP returns the adapter for svc. svc may be nil when only the OpenAPI
// document is built.
func NewHTTP(svc *Service) *HTTP { return &HTTP{svc: svc} }

type nameIn struct {
	Name string `path:"name" doc:"The draft's name."`
}

type stackIn struct {
	Name string `path:"name" doc:"The installed stack's name."`
}

type stackFilesBody struct{ Body StackFiles }

type draftBody struct{ Body Draft }

type draftsBody struct{ Body []DraftSummary }

type createIn struct{ Body DraftCreate }

type saveIn struct {
	Name string `path:"name" doc:"The draft's name."`
	Body DraftSave
}

type checkIn struct {
	Name string        `path:"name" doc:"The draft's name."`
	Body *DraftCheckIn `required:"false"`
}

type checkBody struct{ Body DraftCheck }

type assistIn struct {
	Name string `path:"name" doc:"The draft's name."`
	Body DraftAssist
}

// Register adds the drafts operations.
func (h *HTTP) Register(a httpx.APIs) {
	tags := []string{"drafts"}
	op := func(id, method, path, summary string, status int) huma.Operation {
		return huma.Operation{OperationID: id, Method: method, Path: path, Summary: summary, Tags: tags, DefaultStatus: status}
	}
	draft := func(f func(context.Context, string) (Draft, error)) func(context.Context, *nameIn) (*draftBody, error) {
		return func(ctx context.Context, in *nameIn) (*draftBody, error) {
			d, err := f(ctx, in.Name)
			if err != nil {
				return nil, err
			}
			return &draftBody{Body: d}, nil
		}
	}

	httpx.Register(a, httpx.ScopeOperator, op("listDrafts", http.MethodGet, "/drafts",
		"Stacks written in the dashboard.", 0),
		func(ctx context.Context, _ *struct{}) (*draftsBody, error) {
			l, err := h.svc.List(ctx)
			if err != nil {
				return nil, err
			}
			return &draftsBody{Body: l}, nil
		})
	httpx.Register(a, httpx.ScopeOperator, op("createDraft", http.MethodPost, "/drafts",
		"Start writing a stack from the template, or fork an installed stack.", http.StatusCreated),
		func(ctx context.Context, in *createIn) (*draftBody, error) {
			if in.Body.From != "" {
				d, err := h.svc.Fork(ctx, in.Body.Name, in.Body.From)
				if err != nil {
					return nil, err
				}
				return &draftBody{Body: d}, nil
			}
			d, err := h.svc.Create(ctx, in.Body.Name)
			if err != nil {
				return nil, err
			}
			return &draftBody{Body: d}, nil
		})
	httpx.Register(a, httpx.ScopeOperator, op("convertStack", http.MethodPost, "/stacks/{name}/fork",
		"Fork an installed stack into a draft of its name and move the stack onto it, so it is edited in the dashboard.", http.StatusCreated),
		draft(func(ctx context.Context, n string) (Draft, error) {
			return h.svc.Convert(ctx, n)
		}))
	httpx.Register(a, httpx.ScopeOperator, op("getStackFiles", http.MethodGet, "/stacks/{name}/files",
		"An installed stack's files at its approved commit, or the fetched one while nothing is approved. "+
			"A repo of more than 500 files or 16 MiB of text is refused.", 0),
		func(ctx context.Context, in *stackIn) (*stackFilesBody, error) {
			f, err := h.svc.StackFiles(ctx, in.Name)
			if err != nil {
				return nil, err
			}
			return &stackFilesBody{Body: f}, nil
		})
	httpx.Register(a, httpx.ScopeOperator, op("getDraft", http.MethodGet, "/drafts/{name}",
		"A draft's saved files with its last check and build.", 0), draft(func(ctx context.Context, n string) (Draft, error) {
		return h.svc.Get(ctx, n)
	}))
	httpx.Register(a, httpx.ScopeOperator, op("saveDraft", http.MethodPut, "/drafts/{name}",
		"Save changes to a draft's files as a commit. A stack installed from it shows the commit as an update.", 0),
		func(ctx context.Context, in *saveIn) (*draftBody, error) {
			d, err := h.svc.Save(ctx, in.Name, in.Body)
			if err != nil {
				return nil, err
			}
			return &draftBody{Body: d}, nil
		})
	httpx.Register(a, httpx.ScopeOperator, op("deleteDraft", http.MethodDelete, "/drafts/{name}",
		"Delete a draft and its repo; refused while a stack is installed from it.", http.StatusNoContent),
		func(ctx context.Context, in *nameIn) (*struct{}, error) {
			return nil, h.svc.Delete(ctx, in.Name)
		})
	httpx.Register(a, httpx.ScopeOperator, op("checkDraft", http.MethodPost, "/drafts/{name}/check",
		"Check a draft's files the way an install reads them: the manifest parsed and validated, flake.nix present, the Nix files parsed.", 0),
		func(ctx context.Context, in *checkIn) (*checkBody, error) {
			var changes *[]DraftChange
			if in.Body != nil {
				changes = in.Body.Changes
			}
			c, err := h.svc.Check(ctx, in.Name, changes)
			if err != nil {
				return nil, err
			}
			return &checkBody{Body: c}, nil
		})
	httpx.Register(a, httpx.ScopeOperator, op("buildDraft", http.MethodPost, "/drafts/{name}/build",
		"Build a draft's saved commit the way an install would. It runs in the background; the draft shows it.", http.StatusAccepted),
		draft(func(ctx context.Context, n string) (Draft, error) { return h.svc.Build(ctx, n) }))
	httpx.RegisterSSE(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "assistDraft", Method: http.MethodPost, Path: "/drafts/{name}/assist", Tags: tags,
		Summary: "Ask the assistant about the editor's files. The answer streams as text events, then one answer event " +
			"with its notes and the whole files it proposes; nothing is saved. A failure is an error event.",
	}, map[string]any{"text": AssistText{}, "thinking": llm.AssistThinking{}, "answer": DraftAnswer{}, "error": AssistError{}},
		func(ctx context.Context, in *assistIn, send sse.Sender) {
			a, err := h.svc.Assist(ctx, in.Name, in.Body, func(t string) {
				_ = send.Data(AssistText{Text: t})
			}, func(t string) {
				_ = send.Data(llm.AssistThinking{Text: t})
			})
			if err != nil {
				msg := err.Error()
				var p *httpx.Problem
				if errors.As(err, &p) {
					msg = p.Detail
				}
				_ = send.Data(AssistError{Message: msg})
				return
			}
			_ = send.Data(a)
		})
}
