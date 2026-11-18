package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
)

type unitIn struct {
	Unit string `path:"unit"`
}

type patchIn struct {
	Unit string `path:"unit"`
	Body struct {
		Enabled *bool `json:"enabled,omitempty"`
	}
}

type listOut struct{ Body []string }

// testAPIs builds both APIs with one operation per scope, the way the daemon
// registers its features.
func testAPIs(t *testing.T) (op, stack *http.ServeMux) {
	t.Helper()
	op, stack = http.NewServeMux(), http.NewServeMux()
	a := APIs{Operator: NewAPI(op, "t", "1"), Stack: NewAPI(stack, "t stack", "1")}
	Register(a, ScopeRead, huma.Operation{OperationID: "list", Method: http.MethodGet, Path: "/things"},
		func(context.Context, *struct{}) (*listOut, error) { return &listOut{Body: []string{"a"}}, nil })
	Register(a, ScopeControl, huma.Operation{OperationID: "start", Method: http.MethodPost, Path: "/things/{unit}/start", DefaultStatus: http.StatusNoContent},
		func(_ context.Context, in *unitIn) (*struct{}, error) {
			switch in.Unit {
			case "missing":
				return nil, fmt.Errorf("looking up: %w", NotFound("unknown_unit", "no unit %s", in.Unit).With("unit", in.Unit))
			case "boom":
				return nil, errors.New("exec: /dev/sda1 permission denied (secret detail)")
			}
			return nil, nil
		})
	Register(a, ScopeOperator, huma.Operation{OperationID: "patch", Method: http.MethodPatch, Path: "/things/{unit}"},
		func(_ context.Context, in *patchIn) (*struct{}, error) {
			if in.Body.Enabled == nil {
				return nil, BadRequest("absent", "enabled absent")
			}
			return nil, nil
		})
	return op, stack
}

func serve(h http.Handler, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w, m
}

func TestHumaProblems(t *testing.T) {
	t.Parallel()
	op, _ := testAPIs(t)
	cases := []struct {
		name, method, path, body string
		status                   int
		code                     string
	}{
		{"wrapped problem keeps its code", "POST", "/api/things/missing/start", "", 404, "unknown_unit"},
		{"plain error is a generic 500", "POST", "/api/things/boom/start", "", 500, "internal"},
		{"validation failure", "PATCH", "/api/things/x", `{"enabled":"no"}`, 422, "bad_request"},
		{"unknown route", "GET", "/api/nope", "", 404, "not_found"},
		{"absent optional field", "PATCH", "/api/things/x", `{}`, 400, "absent"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w, m := serve(op, c.method, c.path, c.body)
			if w.Code != c.status || m["code"] != c.code {
				t.Fatalf("got %d %v, want %d %s: %s", w.Code, m["code"], c.status, c.code, w.Body)
			}
			if ct := w.Header().Get("Content-Type"); ct != ContentTypeProblem {
				t.Fatalf("content type %q", ct)
			}
			if m["type"] != "about:blank" || m["instance"] != c.path {
				t.Fatalf("standard members missing: %s", w.Body)
			}
		})
	}
	t.Run("internals stay hidden", func(t *testing.T) {
		t.Parallel()
		w, _ := serve(op, "POST", "/api/things/boom/start", "")
		if strings.Contains(w.Body.String(), "secret detail") {
			t.Fatalf("500 leaked the error: %s", w.Body)
		}
	})
	t.Run("extension members", func(t *testing.T) {
		t.Parallel()
		_, m := serve(op, "POST", "/api/things/missing/start", "")
		if m["unit"] != "missing" {
			t.Fatalf("extension member missing: %v", m)
		}
	})
	t.Run("zero is not absent", func(t *testing.T) {
		t.Parallel()
		if w, _ := serve(op, "PATCH", "/api/things/x", `{"enabled":false}`); w.Code != http.StatusNoContent {
			t.Fatalf("explicit false: %d %s", w.Code, w.Body)
		}
	})
}

func TestStackAPIServesOnlyScopedOperations(t *testing.T) {
	t.Parallel()
	_, stack := testAPIs(t)
	if w, _ := serve(stack, "GET", "/api/things", ""); w.Code != 200 {
		t.Fatalf("read op: %d", w.Code)
	}
	if w, _ := serve(stack, "POST", "/api/things/x/start", ""); w.Code != 204 {
		t.Fatalf("control op: %d", w.Code)
	}
	if w, m := serve(stack, "PATCH", "/api/things/x", `{"enabled":true}`); w.Code != 404 || m["code"] != "not_found" {
		t.Fatalf("operator op reachable on the stack API: %d %s", w.Code, w.Body)
	}
}

func TestRegisterRefusesANonGETRead(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("a POST registered with the read scope")
		}
	}()
	a := APIs{Operator: NewAPI(http.NewServeMux(), "t", "1")}
	Register(a, ScopeRead, huma.Operation{OperationID: "x", Method: http.MethodPost, Path: "/x"},
		func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })
}

func TestOperationScopeIsRecorded(t *testing.T) {
	t.Parallel()
	a := APIs{Operator: NewAPI(http.NewServeMux(), "t", "1")}
	Register(a, ScopeControl, huma.Operation{OperationID: "x", Method: http.MethodPost, Path: "/x"},
		func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })
	op := a.Operator.OpenAPI().Paths["/x"].Post
	if OperationScope(op) != ScopeControl {
		t.Fatalf("scope = %q", OperationScope(op))
	}
}
