package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/danielgtaylor/huma/v2/sse"
)

// Prefix is where every operation is mounted, on the socket and on the stack
// listener alike.
const Prefix = "/api"

// Scope says who may call an operation. Every operation is on the operator
// API (the unix socket); read and control operations are also on the stack
// listener, for stacks whose approved `api` grant holds that scope.
type Scope string

const (
	// ScopeOperator is reachable on the unix socket only.
	ScopeOperator Scope = "operator"
	// ScopeRead is a GET a stack with the read grant may call.
	ScopeRead Scope = "read"
	// ScopeControl starts, stops or restarts stack units and stacks.
	ScopeControl Scope = "control"
)

// metaScope is the huma.Operation metadata key that carries the scope.
const metaScope = "scope"

// OperationScope returns the scope an operation was registered with.
func OperationScope(op *huma.Operation) Scope {
	if op == nil {
		return ScopeOperator
	}
	s, _ := op.Metadata[metaScope].(Scope)
	if s == "" {
		return ScopeOperator
	}
	return s
}

// APIs are the two APIs an operation can be registered on. Stack is nil when
// only the operator API is built (the OpenAPI document command).
type APIs struct {
	Operator huma.API
	Stack    huma.API
}

// Register adds one operation to the operator API and, when its scope is not
// operator-only, to the stack API. A read operation must be a GET: that is
// the whole definition of the read scope.
func Register[I, O any](a APIs, scope Scope, op huma.Operation, h func(context.Context, *I) (*O, error)) {
	if scope == ScopeRead && op.Method != http.MethodGet {
		panic(fmt.Sprintf("httpx: %s is %s; the read scope covers GET only", op.OperationID, op.Method))
	}
	if scope != ScopeRead && scope != ScopeControl && scope != ScopeOperator {
		panic(fmt.Sprintf("httpx: %s has unknown scope %q", op.OperationID, scope))
	}
	if op.Metadata == nil {
		op.Metadata = map[string]any{}
	}
	op.Metadata[metaScope] = scope
	if op.DefaultStatus == 0 && op.Method == http.MethodPost {
		op.DefaultStatus = http.StatusOK
	}
	huma.Register(a.Operator, op, h)
	if scope != ScopeOperator && a.Stack != nil {
		huma.Register(a.Stack, op, h)
	}
}

// RegisterSSE adds a Server-Sent Events operation the way Register adds
// any other: on the operator API, and on the stack API unless its scope is
// operator-only. events maps each event name to its data type.
func RegisterSSE[I any](a APIs, scope Scope, op huma.Operation, events map[string]any, h func(context.Context, *I, sse.Sender)) {
	// A stream is a GET; an operator-only one may be a POST, for a request
	// with a body (the stack editor's assistant).
	if op.Method != http.MethodGet && !(op.Method == http.MethodPost && scope == ScopeOperator) {
		panic(fmt.Sprintf("httpx: %s is %s; an event stream is a GET, or a POST on the operator API", op.OperationID, op.Method))
	}
	if scope != ScopeRead && scope != ScopeControl && scope != ScopeOperator {
		panic(fmt.Sprintf("httpx: %s has unknown scope %q", op.OperationID, scope))
	}
	if op.Metadata == nil {
		op.Metadata = map[string]any{}
	}
	op.Metadata[metaScope] = scope
	sse.Register(a.Operator, op, events, h)
	if scope != ScopeOperator && a.Stack != nil {
		sse.Register(a.Stack, op, events, h)
	}
}

// Config is the huma configuration both APIs share: OpenAPI 3.1 at
// /api/openapi.json, no docs UI, no `$schema` members or Link headers in
// responses, and problem bodies that carry the request path.
func Config(title, version string) huma.Config {
	c := huma.DefaultConfig(title, version)
	c.CreateHooks = nil
	c.DocsPath = ""
	c.SchemasPath = ""
	c.OpenAPIPath = "/openapi"
	c.Servers = []*huma.Server{{URL: Prefix}}
	c.Transformers = append(c.Transformers, problemInstance)
	return c
}

// NewAPI returns a huma API on mux under Prefix. Unknown routes under the
// prefix answer with a problem instead of the mux's plain-text 404.
func NewAPI(mux *http.ServeMux, title, version string) huma.API {
	api := humago.NewWithPrefix(mux, Prefix, Config(title, version))
	mux.Handle(Prefix+"/", NotFoundHandler)
	return api
}

// problemInstance fills a problem's instance member with the request path.
func problemInstance(ctx huma.Context, _ string, v any) (any, error) {
	if p, ok := v.(*Problem); ok && p.instance == "" {
		cp := *p
		cp.instance = ctx.URL().Path
		return &cp, nil
	}
	return v, nil
}

// GetStatus makes a Problem a huma.StatusError: a handler returns it (or an
// error wrapping it) and huma answers with this status and body.
func (p *Problem) GetStatus() int { return p.Status }

// ContentType sends every problem as application/problem+json.
func (p *Problem) ContentType(string) string { return ContentTypeProblem }

// MarshalJSON writes the RFC 9457 members, the code and the extension
// members at the top level.
func (p *Problem) MarshalJSON() ([]byte, error) {
	body := make(map[string]any, 6+len(p.Ext))
	for k, v := range p.Ext {
		body[k] = v
	}
	body["type"] = "about:blank"
	body["title"] = p.Title
	body["status"] = p.Status
	if p.Detail != "" {
		body["detail"] = p.Detail
	}
	if p.instance != "" {
		body["instance"] = p.instance
	}
	if p.Code != "" {
		body["code"] = p.Code
	}
	return json.Marshal(body)
}

// Schema documents the problem body in the OpenAPI document.
func (p *Problem) Schema(huma.Registry) *huma.Schema {
	str := &huma.Schema{Type: huma.TypeString}
	return &huma.Schema{
		Type:        huma.TypeObject,
		Description: "RFC 9457 problem details. `code` is the machine-readable reason; extension members may follow.",
		Properties: map[string]*huma.Schema{
			"type":     str,
			"title":    str,
			"status":   {Type: huma.TypeInteger, Format: "int32"},
			"detail":   str,
			"instance": str,
			"code":     str,
		},
		Required:             []string{"type", "title", "status"},
		AdditionalProperties: true,
	}
}

func init() {
	// The daemon always sends [] for an empty list; a nullable array would
	// turn every list in the dashboard's types into `T[] | null`.
	huma.DefaultArrayNullable = false
	huma.NewError = newHumaError
	huma.NewErrorWithContext = func(ctx huma.Context, status int, msg string, errs ...error) huma.StatusError {
		if status >= 500 {
			// A handler's plain error: log it, answer without its text.
			var path, method string
			var cctx context.Context = context.Background()
			if ctx != nil {
				path, method, cctx = ctx.URL().Path, ctx.Method(), ctx.Context()
			}
			if errors.Is(cctx.Err(), context.Canceled) {
				// Nobody is left to read an answer; this is not a server error.
				log.Debug("client went away", "method", method, "path", path, "errors", errs)
				return &Problem{Status: StatusClientClosedRequest, Code: "client_closed", Title: "Client Closed Request"}
			} else {
				log.ErrorContext(cctx, "unhandled handler error", "method", method, "path", path, "errors", errs)
			}
		}
		return newHumaError(status, msg, errs...)
	}
}

// newHumaError is huma's error constructor: request validation, body size
// and content negotiation failures become problems with our codes, and a 5xx
// never carries the underlying error's text.
func newHumaError(status int, msg string, errs ...error) huma.StatusError {
	for _, e := range errs {
		var p *Problem
		if errors.As(e, &p) {
			return p
		}
	}
	code := "bad_request"
	switch {
	case status == http.StatusRequestEntityTooLarge:
		code = "request_too_large"
	case status >= 500:
		return &Problem{Status: status, Code: "internal", Title: http.StatusText(status)}
	}
	detail := msg
	var parts []string
	for _, e := range errs {
		if e != nil {
			parts = append(parts, e.Error())
		}
	}
	if len(parts) > 0 {
		detail = msg + ": " + strings.Join(parts, "; ")
	}
	return &Problem{Status: status, Code: code, Title: http.StatusText(status), Detail: detail}
}

type callerKey struct{}

// WithCallerStack marks ctx as a request from the named stack, made on the
// stack listener after its token was checked.
func WithCallerStack(ctx context.Context, stack string) context.Context {
	return context.WithValue(ctx, callerKey{}, stack)
}

// CallerStack returns the stack a request came from, and false for the
// operator (the unix socket). A handler that must refuse a stack something
// the operator may do (control of a core unit) checks it.
func CallerStack(ctx context.Context) (string, bool) {
	s, ok := ctx.Value(callerKey{}).(string)
	return s, ok && s != ""
}
