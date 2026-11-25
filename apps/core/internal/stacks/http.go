package stacks

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"homelab-ngx.com/core/internal/httpx"
)

// HTTP is the stacks slice of the daemon API.
type HTTP struct{ svc *Service }

// NewHTTP returns the HTTP adapter for svc. svc may be nil when only the
// OpenAPI document is built.
func NewHTTP(svc *Service) *HTTP { return &HTTP{svc: svc} }

type stackBody struct{ Body Stack }

type stacksBody struct{ Body []Stack }

type nameIn struct {
	Name string `path:"name" doc:"The stack's name, as it was installed."`
}

type addIn struct{ Body StackAdd }

type channelsBody struct{ Body StackNixpkgsChannels }
type schemaBody struct{ Body json.RawMessage }

type patchIn struct {
	Name string `path:"name" doc:"The stack's name, as it was installed."`
	Body StackPatch
}

type approveIn struct {
	Name string `path:"name" doc:"The stack's name, as it was installed."`
	Body StackApprove
}

type rollbackIn struct {
	Name string         `path:"name" doc:"The stack's name, as it was installed."`
	Body *StackRollback `required:"false"`
}

type stopIn struct {
	Name string     `path:"name" doc:"The stack's name, as it was installed."`
	Body *StackStop `required:"false"`
}

type variableIn struct {
	Name string `path:"name" doc:"The stack's name, as it was installed."`
	Var  string `path:"var" doc:"The variable's name: 1-64 characters of A-Z, 0-9 and '_', starting with a letter."`
}

type setVariableIn struct {
	Name string `path:"name" doc:"The stack's name, as it was installed."`
	Var  string `path:"var" doc:"The variable's name: 1-64 characters of A-Z, 0-9 and '_', starting with a letter."`
	Body StackVariableSet
}

type foldersSetIn struct {
	Name string `path:"name" doc:"The stack's name, as it was installed."`
	Body StackFoldersSet
}

type folderCreateIn struct {
	Name string `path:"name" doc:"The stack's name, as it was installed."`
	Var  string `path:"var" doc:"A path variable of the stack."`
	Body StackFolderCreate
}

type folderOwnIn struct {
	Name string `path:"name" doc:"The stack's name, as it was installed."`
	Var  string `path:"var" doc:"A path variable of the stack."`
	Body StackFolderOwn
}

type stackIDsBody struct{ Body StackIDList }

type stackIDIn struct {
	Name string `path:"name" doc:"A user or group name a stack declares."`
	Body StackIDSet
}

type queuedBody struct{ Body StackUpdateAll }

type updatesIn struct {
	Stack string `query:"stack" doc:"Only this stack's attempts."`
	Limit int    `query:"limit" minimum:"1" maximum:"500" default:"50"`
}

type updatesBody struct{ Body []StackUpdate }

type updateIn struct {
	Id int64 `path:"id"`
}

type updateBody struct{ Body StackUpdate }

type lockBody struct{ Body StackLock }

type nixStoreBody struct{ Body NixStore }

type restoreIn struct{ Body StackLock }

type restoreBody struct{ Body StackRestoreResult }

type iconIn struct {
	Name string `path:"name" doc:"The stack's name, as it was installed."`
	Unit string `query:"unit" doc:"A service's unit as the manifest's services list names it, such as plex.service. Without it, the stack's own icon."`
}

// iconOut is an icon: the image (200), or a redirect to its https URL (302).
type iconOut struct {
	Status       int
	ContentType  string `header:"Content-Type"`
	NoSniff      string `header:"X-Content-Type-Options"`
	CacheControl string `header:"Cache-Control"`
	CSP          string `header:"Content-Security-Policy" doc:"Set on an SVG: it is shown as an image, never run as a page."`
	Location     string `header:"Location"`
	Body         []byte
}

// svgCSP keeps an SVG opened on its own from loading or running anything.
const svgCSP = "default-src 'none'; style-src 'unsafe-inline'; sandbox"

func iconAnswer(ic Icon) *iconOut {
	if ic.URL != "" {
		return &iconOut{Status: http.StatusFound, Location: ic.URL}
	}
	out := &iconOut{
		Status: http.StatusOK, ContentType: ic.Type, NoSniff: "nosniff",
		CacheControl: "private, max-age=86400", Body: ic.Data,
	}
	if ic.Type == "image/svg+xml" {
		out.CSP = svgCSP
	}
	return out
}

// Register adds the stacks operations. Reading stacks and their history is
// `read`; starting and stopping a stack is `control` (a stack may run
// others); everything that changes what is installed or approved is the
// operator's alone.
func (h *HTTP) Register(a httpx.APIs) {
	tags := []string{"stacks"}
	op := func(id, method, path, summary string, status int) huma.Operation {
		return huma.Operation{OperationID: id, Method: method, Path: path, Summary: summary, Tags: tags, DefaultStatus: status}
	}
	stack := func(f func(context.Context, string) (Stack, error)) func(context.Context, *nameIn) (*stackBody, error) {
		return func(ctx context.Context, in *nameIn) (*stackBody, error) {
			s, err := f(ctx, in.Name)
			if err != nil {
				return nil, err
			}
			return &stackBody{Body: s}, nil
		}
	}

	httpx.Register(a, httpx.ScopeRead, op("listStacks", http.MethodGet, "/stacks",
		"Installed stacks with their fetched and approved commits.", 0),
		func(ctx context.Context, _ *struct{}) (*stacksBody, error) {
			l, err := h.svc.List(ctx)
			if err != nil {
				return nil, err
			}
			return &stacksBody{Body: l}, nil
		})
	httpx.Register(a, httpx.ScopeOperator, op("addStack", http.MethodPost, "/stacks",
		"Install a stack from its repo.", http.StatusCreated),
		func(ctx context.Context, in *addIn) (*stackBody, error) {
			s, err := h.svc.Add(ctx, in.Body.Source, in.Body.Name)
			if err != nil {
				return nil, err
			}
			return &stackBody{Body: s}, nil
		})
	httpx.Register(a, httpx.ScopeRead, op("getStack", http.MethodGet, "/stacks/{name}",
		"One stack.", 0), stack(func(ctx context.Context, n string) (Stack, error) { return h.svc.Get(ctx, n) }))
	iconOp := op("getStackIcon", http.MethodGet, "/stacks/{name}/icon",
		"The icon of a stack, or of one of its services: the image, or a redirect to it when the manifest names an https URL. Answers no_icon when there is none.", 0)
	image := &huma.MediaType{Schema: &huma.Schema{Type: huma.TypeString, Format: "binary"}}
	iconOp.Responses = map[string]*huma.Response{
		"200": {Description: "The image.", Content: map[string]*huma.MediaType{"image/svg+xml": image, "image/png": image, "image/webp": image}},
		"302": {Description: "The icon is an https URL, which Location names.",
			Headers: map[string]*huma.Param{"Location": {Schema: &huma.Schema{Type: huma.TypeString}}}},
	}
	httpx.Register(a, httpx.ScopeRead, iconOp,
		func(ctx context.Context, in *iconIn) (*iconOut, error) {
			ic, err := h.svc.Icon(ctx, in.Name, in.Unit)
			if err != nil {
				return nil, err
			}
			return iconAnswer(ic), nil
		})
	httpx.Register(a, httpx.ScopeOperator, op("patchStack", http.MethodPatch, "/stacks/{name}",
		"Change what the operator sets on a stack: its label, whether it starts after the host boots, the nixpkgs a version 7 stack is built with, and the source it is fetched from.", 0),
		func(ctx context.Context, in *patchIn) (*stackBody, error) {
			s, err := h.svc.Get(ctx, in.Name)
			if err == nil && in.Body.Source != nil {
				s, err = h.svc.SetSource(ctx, in.Name, *in.Body.Source)
			}
			if err == nil && in.Body.Label != nil {
				s, err = h.svc.SetLabel(ctx, in.Name, *in.Body.Label)
			}
			if err == nil && in.Body.Autostart != nil {
				s, err = h.svc.SetAutostart(ctx, in.Name, *in.Body.Autostart)
			}
			if err == nil && in.Body.Nixpkgs != nil {
				s, err = h.svc.SetNixpkgs(ctx, in.Name, *in.Body.Nixpkgs, in.Body.AllowDowngrade)
			}
			if err != nil {
				return nil, err
			}
			return &stackBody{Body: s}, nil
		})
	httpx.Register(a, httpx.ScopeRead, op("listNixpkgsChannels", http.MethodGet, "/stacks-nixpkgs",
		"The nixpkgs channels offered for a version 7 stack: nixos-unstable and the newest NixOS releases.", 0),
		func(ctx context.Context, _ *struct{}) (*channelsBody, error) {
			c, err := h.svc.Channels(ctx)
			if err != nil {
				return nil, err
			}
			return &channelsBody{Body: StackNixpkgsChannels{Channels: c}}, nil
		})
	httpx.Register(a, httpx.ScopeRead, op("getNixStore", http.MethodGet, "/stacks-store",
		"The Nix store the stacks are built into: its size, the room on its disk and its last garbage collection.", 0),
		func(ctx context.Context, _ *struct{}) (*nixStoreBody, error) {
			return &nixStoreBody{Body: h.svc.NixStore(ctx)}, nil
		})
	httpx.Register(a, httpx.ScopeRead, op("getManifestSchema", http.MethodGet, "/stacks-manifest-schema",
		"The JSON Schema of homelab-stack.json, version 10, for editors. Check and an install stay the authority.", 0),
		func(context.Context, *struct{}) (*schemaBody, error) {
			return &schemaBody{Body: json.RawMessage(ManifestSchema)}, nil
		})
	httpx.Register(a, httpx.ScopeOperator, op("removeStack", http.MethodDelete, "/stacks/{name}",
		"Forget a stack, its approvals and its built generations.", http.StatusNoContent),
		func(ctx context.Context, in *nameIn) (*struct{}, error) {
			return nil, h.svc.Remove(ctx, in.Name)
		})
	httpx.Register(a, httpx.ScopeOperator, op("fetchStack", http.MethodPost, "/stacks/{name}/fetch",
		"Read the newest commit and its manifest from the stack's source.", 0),
		stack(func(ctx context.Context, n string) (Stack, error) { return h.svc.Fetch(ctx, n) }))
	httpx.Register(a, httpx.ScopeOperator, op("approveStack", http.MethodPost, "/stacks/{name}/approve",
		"Approve the fetched commit and every grant its manifest asks for.", 0),
		func(ctx context.Context, in *approveIn) (*stackBody, error) {
			s, err := h.svc.Approve(ctx, in.Name, in.Body.Rev)
			if err != nil {
				return nil, err
			}
			return &stackBody{Body: s}, nil
		})
	httpx.Register(a, httpx.ScopeControl, op("startStack", http.MethodPost, "/stacks/{name}/start",
		"Start a stack, building its approved commit first when nothing is built.", http.StatusAccepted),
		stack(func(ctx context.Context, n string) (Stack, error) { return h.svc.Start(ctx, n) }))
	httpx.Register(a, httpx.ScopeControl, op("stopStack", http.MethodPost, "/stacks/{name}/stop",
		"Stop a stack and remove its routes and network rules. Cancels a start that is waiting for health.", http.StatusAccepted),
		func(ctx context.Context, in *stopIn) (*stackBody, error) {
			s, err := h.svc.Stop(ctx, in.Name)
			if err != nil {
				return nil, err
			}
			return &stackBody{Body: s}, nil
		})
	httpx.Register(a, httpx.ScopeOperator, op("freezeStack", http.MethodPost, "/stacks/{name}/freeze",
		"Freeze a running stack: its processes stop where they are and keep their memory, and its addresses stop answering until it is thawed. A reboot loses what they held. Refused with stack_not_running, and with busy while an operation runs.", 0),
		stack(func(ctx context.Context, n string) (Stack, error) { return h.svc.Freeze(ctx, n) }))
	httpx.Register(a, httpx.ScopeOperator, op("thawStack", http.MethodPost, "/stacks/{name}/thaw",
		"Thaw a frozen stack: its processes carry on where they stopped. A stack that is not frozen is left as it is.", 0),
		stack(func(ctx context.Context, n string) (Stack, error) { return h.svc.Thaw(ctx, n) }))
	httpx.Register(a, httpx.ScopeOperator, op("updateStack", http.MethodPost, "/stacks/{name}/update",
		"Build the approved commit and switch the stack to it.", http.StatusAccepted),
		stack(func(ctx context.Context, n string) (Stack, error) { return h.svc.Update(ctx, n) }))
	httpx.Register(a, httpx.ScopeOperator, op("rollbackStack", http.MethodPost, "/stacks/{name}/rollback",
		"Switch a stack back to an earlier generation.", http.StatusAccepted),
		func(ctx context.Context, in *rollbackIn) (*stackBody, error) {
			var to int64
			if in.Body != nil {
				to = int64(in.Body.Generation)
			}
			s, err := h.svc.Rollback(ctx, in.Name, to)
			if err != nil {
				return nil, err
			}
			return &stackBody{Body: s}, nil
		})
	httpx.Register(a, httpx.ScopeOperator, op("setStackVariable", http.MethodPut, "/stacks/{name}/variables/{var}",
		"Set a stack variable's value; the stack sees it at /run/secrets.", 0),
		func(ctx context.Context, in *setVariableIn) (*stackBody, error) {
			s, err := h.svc.SetVariable(ctx, in.Name, in.Var, in.Body)
			if err != nil {
				return nil, err
			}
			return &stackBody{Body: s}, nil
		})
	httpx.Register(a, httpx.ScopeOperator, op("createStackFolder", http.MethodPost, "/stacks/{name}/variables/{var}/folder",
		"Make the folder for a path variable, owned by the stack ids of its owner and group, and store it as the value.", 0),
		func(ctx context.Context, in *folderCreateIn) (*stackBody, error) {
			s, err := h.svc.CreateFolder(ctx, in.Name, in.Var, in.Body)
			if err != nil {
				return nil, err
			}
			return &stackBody{Body: s}, nil
		})
	httpx.Register(a, httpx.ScopeOperator, op("setStackFolders", http.MethodPut, "/stacks/{name}/folders",
		"Replace the folders the operator binds into a stack on top of its manifest; the stack sees them at its next restart.", 0),
		func(ctx context.Context, in *foldersSetIn) (*stackBody, error) {
			s, err := h.svc.SetFolders(ctx, in.Name, in.Body)
			if err != nil {
				return nil, err
			}
			return &stackBody{Body: s}, nil
		})
	httpx.Register(a, httpx.ScopeOperator, op("ownStackFolder", http.MethodPost, "/stacks/{name}/variables/{var}/own",
		"Change the owner of a path variable's folder, and of everything in it, to the stack ids. It cannot be undone; the stack must be stopped.", 0),
		func(ctx context.Context, in *folderOwnIn) (*stackBody, error) {
			s, err := h.svc.OwnFolder(ctx, in.Name, in.Var, in.Body)
			if err != nil {
				return nil, err
			}
			return &stackBody{Body: s}, nil
		})
	httpx.Register(a, httpx.ScopeRead, op("getStackIDs", http.MethodGet, "/stacks-ids",
		"This host's ids for the users and groups its stacks declare.", 0),
		func(ctx context.Context, _ *struct{}) (*stackIDsBody, error) {
			l, err := h.svc.StackIDs(ctx)
			if err != nil {
				return nil, err
			}
			return &stackIDsBody{Body: l}, nil
		})
	httpx.Register(a, httpx.ScopeOperator, op("setStackID", http.MethodPut, "/stacks-ids/{name}",
		"Pin the id of a user or group name. Folders made before keep the id they have.", 0),
		func(ctx context.Context, in *stackIDIn) (*stackIDsBody, error) {
			l, err := h.svc.SetStackID(ctx, in.Name, in.Body.ID)
			if err != nil {
				return nil, err
			}
			return &stackIDsBody{Body: l}, nil
		})
	httpx.Register(a, httpx.ScopeOperator, op("deleteStackVariable", http.MethodDelete, "/stacks/{name}/variables/{var}",
		"Remove a stack variable, declared or not.", 0),
		func(ctx context.Context, in *variableIn) (*stackBody, error) {
			s, err := h.svc.DeleteVariable(ctx, in.Name, in.Var)
			if err != nil {
				return nil, err
			}
			return &stackBody{Body: s}, nil
		})
	httpx.Register(a, httpx.ScopeOperator, op("checkStacks", http.MethodPost, "/stacks-check",
		"Read every stack's source for new commits now.", 0),
		func(ctx context.Context, _ *struct{}) (*stacksBody, error) {
			l, err := h.svc.CheckUpdates(ctx)
			if err != nil {
				return nil, err
			}
			return &stacksBody{Body: l}, nil
		})
	httpx.Register(a, httpx.ScopeOperator, op("updateAllStacks", http.MethodPost, "/stacks-update",
		"Update every stack whose approved commit is not running yet.", http.StatusAccepted),
		func(ctx context.Context, _ *struct{}) (*queuedBody, error) {
			q, err := h.svc.UpdateAll(ctx)
			if err != nil {
				return nil, err
			}
			out := &queuedBody{}
			out.Body.Queued = q
			return out, nil
		})
	httpx.Register(a, httpx.ScopeRead, op("listStackUpdates", http.MethodGet, "/stack-updates",
		"Installs, updates and rollbacks of stacks, newest first.", 0),
		func(ctx context.Context, in *updatesIn) (*updatesBody, error) {
			l, err := h.svc.Updates(ctx, in.Stack, in.Limit)
			if err != nil {
				return nil, err
			}
			return &updatesBody{Body: l}, nil
		})
	httpx.Register(a, httpx.ScopeRead, op("getStackUpdate", http.MethodGet, "/stack-updates/{id}",
		"One attempt with its log.", 0),
		func(ctx context.Context, in *updateIn) (*updateBody, error) {
			u, err := h.svc.UpdateAttempt(ctx, in.Id)
			if err != nil {
				return nil, err
			}
			return &updateBody{Body: u}, nil
		})
	httpx.Register(a, httpx.ScopeRead, op("getStackLock", http.MethodGet, "/stacks-lock",
		"The lock of the installed stacks.", 0),
		func(ctx context.Context, _ *struct{}) (*lockBody, error) {
			l, err := h.svc.CurrentLock(ctx)
			if err != nil {
				return nil, err
			}
			out := &lockBody{}
			if err := convert(l, &out.Body); err != nil {
				return nil, err
			}
			return out, nil
		})
	httpx.Register(a, httpx.ScopeOperator, op("restoreStacks", http.MethodPost, "/stacks-lock/restore",
		"Reinstall the stacks a lock lists.", 0),
		func(ctx context.Context, in *restoreIn) (*restoreBody, error) {
			var l Lock
			if err := convert(in.Body, &l); err != nil {
				return nil, badLock("%v", err)
			}
			res, err := h.svc.Restore(ctx, l)
			if err != nil {
				return nil, err
			}
			return &restoreBody{Body: res}, nil
		})
}

// convert copies between the lock and its API shape, which mirror each
// other's JSON.
func convert(from, to any) error {
	b, err := json.Marshal(from)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, to)
}
