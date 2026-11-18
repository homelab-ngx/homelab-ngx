package drafts

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/llm"
)

func TestParseAnswerTakesNotesAndBothFiles(t *testing.T) {
	a := ParseAnswer("The port was wrong.\n\n<file path=\"homelab-stack.json\">\n{\"version\": 8}\n</file>\n\n" +
		"<file path=\"flake.nix\">\n{ outputs = _: { }; }\n</file>\nBuild it again.")
	if a.Notes != "The port was wrong.\n\nBuild it again." {
		t.Fatalf("notes = %q", a.Notes)
	}
	if a.Files.Manifest == nil || *a.Files.Manifest != "{\"version\": 8}\n" || a.Files.Flake == nil || *a.Files.Flake != "{ outputs = _: { }; }\n" {
		t.Fatalf("files = %+v", a.Files)
	}
	if len(a.Dropped) != 0 {
		t.Fatalf("dropped = %v", a.Dropped)
	}
}

func TestParseAnswerReadsTheParts(t *testing.T) {
	a := ParseAnswer("<summary>Moved the port.</summary>\n<changes>\n- Port 8080 to 3000: the app listens there\n</changes>\n<todo>\n- Fill in API_KEY\n</todo>\n<file path=\"flake.nix\">\n{ }\n</file>")
	if a.Summary != "Moved the port." || len(a.Changes) != 1 || a.Changes[0] != "Port 8080 to 3000: the app listens there" || len(a.Todo) != 1 || a.Notes != "" || a.Files.Flake == nil {
		t.Fatalf("answer = %+v", a)
	}
}

func TestParseAnswerDropsAnUnknownPath(t *testing.T) {
	a := ParseAnswer("Notes.\n<file path=\"stack.nix\">\n{ }\n</file>\n<file path=\"flake.nix\">x</file>")
	if a.Files.Flake == nil || *a.Files.Flake != "x\n" || a.Files.Manifest != nil {
		t.Fatalf("files = %+v", a.Files)
	}
	if len(a.Dropped) != 1 || !strings.Contains(a.Dropped[0], "stack.nix") {
		t.Fatalf("dropped = %v", a.Dropped)
	}
}

func TestParseAnswerDropsATruncatedBlock(t *testing.T) {
	a := ParseAnswer("Here it is.\n<file path=\"homelab-stack.json\">\n{}\n</file>\n<file path=\"flake.nix\">\n{ outputs = _: {")
	if a.Files.Manifest == nil || a.Files.Flake != nil {
		t.Fatalf("files = %+v", a.Files)
	}
	if len(a.Dropped) != 1 || !strings.Contains(a.Dropped[0], "ended inside flake.nix") {
		t.Fatalf("dropped = %v", a.Dropped)
	}
	if a.Notes != "Here it is." {
		t.Fatalf("notes = %q", a.Notes)
	}
}

func TestParseAnswerStripsAFenceInsideABlock(t *testing.T) {
	a := ParseAnswer("<file path='flake.nix'>\n```nix\n{ }\n```\n</file>")
	if a.Files.Flake == nil || *a.Files.Flake != "{ }\n" {
		t.Fatalf("flake = %q", *a.Files.Flake)
	}
}

func TestPromptCarriesFilesCheckAndBuild(t *testing.T) {
	d := Draft{Name: "site", Rev: "abc", Files: Template("site"),
		Build: &DraftBuild{Rev: "abc", Result: BuildFailed, Error: "error: The option `services.nginxx' does not exist."}}
	edited := d.Files
	edited.Manifest = strings.Replace(edited.Manifest, `"port": 8080 }]`, `"port": 0 }]`, 1)
	p, err := prompt(d, DraftAssist{Kind: AssistFix, Files: edited})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Fix the problems", `installed under the name "site"`, "edits not saved yet",
		`<file path="flake.nix">`, "routes[0]", "services.nginxx", "(the files have changed since)"} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, p)
		}
	}
	// Nothing to fix: refused before a request is made.
	d.Build = nil
	if _, err := prompt(d, DraftAssist{Kind: AssistFix, Files: d.Files}); code(err) != "nothing_to_fix" {
		t.Fatalf("fix without errors: %v", err)
	}
	if _, err := prompt(d, DraftAssist{Kind: AssistDraft, Files: d.Files}); code(err) != "text_required" {
		t.Fatalf("draft without a description: %v", err)
	}
}

func TestGuideHoldsTheTemplate(t *testing.T) {
	g := Guide()
	if !strings.Contains(g, `<file path="flake.nix">`) || !strings.Contains(g, "services.nginx") || !strings.Contains(g, "example, a homelab stack") {
		t.Fatalf("guide ends with %q", g[len(g)-300:])
	}
}

type fakeProvider struct {
	req    llm.Request
	answer []string
}

func (f *fakeProvider) Stream(_ context.Context, r llm.Request, onText func(string)) (llm.AnswerUsage, error) {
	f.req = r
	for _, a := range f.answer {
		onText(a)
	}
	return llm.AnswerUsage{}, nil
}

func (f *fakeProvider) Models(context.Context) ([]string, error) { return nil, nil }

type fakeAssistant struct{ p llm.Provider }

func (a fakeAssistant) Provider(context.Context) (llm.Provider, string, error) {
	if a.p == nil {
		return nil, "", llm.ErrOff
	}
	return a.p, "test-model", nil
}

func TestAssistStreamsAndReadsTheAnswer(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	d, err := s.Create(ctx, "site")
	if err != nil {
		t.Fatal(err)
	}
	in := DraftAssist{Kind: AssistReview, Files: d.Files}
	if _, err := s.Assist(ctx, "site", in, func(string) {}, nil); code(err) != "assistant_off" {
		t.Fatalf("without an assistant: %v", err)
	}
	s.SetAssistant(fakeAssistant{})
	if _, err := s.Assist(ctx, "site", in, func(string) {}, nil); code(err) != "assistant_off" {
		t.Fatalf("with the assistant off: %v", err)
	}
	f := &fakeProvider{answer: []string{"Looks fine, one fix.\n<file path=\"flake.n", "ix\">\n{ }\n</file>"}}
	s.SetAssistant(fakeAssistant{p: f})
	var streamed strings.Builder
	a, err := s.Assist(ctx, "site", in, func(t string) { streamed.WriteString(t) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Model != "test-model" || a.Notes != "Looks fine, one fix." || a.Files.Flake == nil || *a.Files.Flake != "{ }\n" {
		t.Fatalf("answer = %+v", a)
	}
	if streamed.String() != strings.Join(f.answer, "") {
		t.Fatalf("streamed %q", streamed.String())
	}
	if f.req.System != Guide() || !strings.Contains(f.req.User, "Review this stack") {
		t.Fatalf("request = %+v", f.req)
	}
}

func TestAssistStreamsOverHTTP(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	d, err := s.Create(ctx, "site")
	if err != nil {
		t.Fatal(err)
	}
	s.SetAssistant(fakeAssistant{p: &fakeProvider{answer: []string{"Fine.", "\n<file path=\"flake.nix\">\n{ }\n</file>"}}})
	mux := http.NewServeMux()
	NewHTTP(s).Register(httpx.APIs{Operator: httpx.NewAPI(mux, "test", "1")})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(DraftAssist{Kind: AssistReview, Files: d.Files})
	res, err := http.Post(srv.URL+"/api/drafts/site/assist", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("%d %s: %s", res.StatusCode, res.Header.Get("Content-Type"), out)
	}
	for _, want := range []string{"event: text\ndata: {\"text\":\"Fine.\"}", "event: answer\ndata: {", `"flake":"{ }\n"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("stream lacks %q:\n%s", want, out)
		}
	}

	// A failure is an error event.
	res, err = http.Post(srv.URL+"/api/drafts/site/assist", "application/json", strings.NewReader(`{"kind":"draft","files":{"manifest":"","flake":""}}`))
	if err != nil {
		t.Fatal(err)
	}
	out, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(out), "event: error\ndata: {\"message\":\"describe the stack to write\"}") {
		t.Fatalf("error stream:\n%s", out)
	}
}
