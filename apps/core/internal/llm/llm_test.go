package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/store/storetest"
)

// sse writes server-sent events: each pair is an event name ("" for none)
// and its data.
func sse(w http.ResponseWriter, events ...[2]string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		if e[0] != "" {
			fmt.Fprintf(w, "event: %s\n", e[0])
		}
		fmt.Fprintf(w, "data: %s\n\n", e[1])
	}
}

func anthropicStream(stop string, texts ...string) [][2]string {
	ev := [][2]string{
		{"message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":1}}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
	}
	for _, t := range texts {
		b, _ := json.Marshal(t)
		ev = append(ev, [2]string{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + string(b) + `}}`})
	}
	return append(ev,
		[2]string{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		[2]string{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"` + stop + `","stop_sequence":null},"usage":{"output_tokens":2}}`},
		[2]string{"message_stop", `{"type":"message_stop"}`},
	)
}

func collect(t *testing.T, p Provider, r Request) (string, error) {
	t.Helper()
	got, _, err := stream(p, r)
	return got, err
}

func stream(p Provider, r Request) (string, AnswerUsage, error) {
	var b strings.Builder
	u, err := p.Stream(context.Background(), r, func(s string) { b.WriteString(s) })
	return b.String(), u, err
}

func TestAnthropicStreamsTextAndCachesTheSystem(t *testing.T) {
	var body map[string]any
	var header http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		header = r.Header
		_ = json.NewDecoder(r.Body).Decode(&body)
		sse(w, anthropicStream("end_turn", "Hel", "lo")...)
	}))
	defer srv.Close()
	p, err := New(Config{Kind: KindAnthropic, BaseURL: srv.URL, Model: "claude-opus-5-5", APIKey: "sk-test"})
	if err != nil {
		t.Fatal(err)
	}
	got, u, err := stream(p, Request{System: "guide", User: "question"})
	if err != nil || got != "Hello" {
		t.Fatalf("got %q, %v", got, err)
	}
	if u.InputTokens != 3 || u.OutputTokens != 2 || u.Cost != nil {
		t.Fatalf("usage = %+v", u)
	}
	if header.Get("Authorization") != "Bearer sk-test" {
		t.Fatalf("key header = %q", header.Get("Authorization"))
	}
	sys := body["system"].([]any)[0].(map[string]any)
	if sys["text"] != "guide" || sys["cache_control"].(map[string]any)["type"] != "ephemeral" {
		t.Fatalf("system = %v", sys)
	}
	if body["model"] != "claude-opus-5-5" || body["stream"] != true || body["fallbacks"] != "default" {
		t.Fatalf("body = %v", body)
	}
	if !strings.Contains(header.Get("Anthropic-Beta"), "server-side-fallback-2026-07-01") {
		t.Fatalf("beta header = %q", header.Get("Anthropic-Beta"))
	}
}

func TestAnthropicOlderModelGetsNoFallbacks(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		sse(w, anthropicStream("end_turn", "ok")...)
	}))
	defer srv.Close()
	p, _ := New(Config{Kind: KindAnthropic, BaseURL: srv.URL, Model: "claude-haiku-4-5", APIKey: "k"})
	if _, err := collect(t, p, Request{System: "s", User: "u"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["fallbacks"]; ok {
		t.Fatalf("fallbacks sent for an older model: %v", body)
	}
}

func TestAnthropicReportsAShortAnswerAndErrors(t *testing.T) {
	status := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
			return
		}
		sse(w, anthropicStream("max_tokens", "par")...)
	}))
	defer srv.Close()
	p, _ := New(Config{Kind: KindAnthropic, BaseURL: srv.URL, Model: "m", APIKey: "k"})
	if got, err := collect(t, p, Request{}); got != "par" || err == nil || !strings.Contains(err.Error(), "cut off") {
		t.Fatalf("max_tokens: %q, %v", got, err)
	}
	status = http.StatusUnauthorized
	if _, err := collect(t, p, Request{}); err == nil || err.Error() != "the provider answered 401: invalid x-api-key" {
		t.Fatalf("401: %v", err)
	}
}

func TestOpenAICompatibleStreamsText(t *testing.T) {
	var body map[string]any
	var auth, session, agent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		auth, session, agent = r.Header.Get("Authorization"), r.Header.Get("x-opencode-session"), r.Header.Get("User-Agent")
		_ = json.NewDecoder(r.Body).Decode(&body)
		chunk := func(content, finish string) [2]string {
			f := "null"
			if finish != "" {
				f = `"` + finish + `"`
			}
			return [2]string{"", `{"id":"c1","object":"chat.completion.chunk","created":1,"model":"llama3.1","choices":[{"index":0,"delta":{"content":"` + content + `"},"finish_reason":` + f + `}]}`}
		}
		// The last chunk is OpenCode's usage: tokens, cost and the Go
		// allowance.
		usage := [2]string{"", `{"id":"c1","object":"chat.completion.chunk","created":1,"model":"llama3.1","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15,"cost":0,"opencode":{"billing_source":"go","list_cost":0.0021,"allowance":0.0021}}}`}
		sse(w, chunk("Hel", ""), chunk("lo", "stop"), usage, [2]string{"", "[DONE]"})
	}))
	defer srv.Close()
	// Like Ollama: a base URL with /v1 and no key.
	p, _ := New(Config{Kind: KindOpenAI, BaseURL: srv.URL + "/v1", Model: "llama3.1"})
	got, u, err := stream(p, Request{System: "guide", User: "question", Session: "homelab-draft-x"})
	if err != nil || got != "Hello" {
		t.Fatalf("got %q, %v", got, err)
	}
	if u.InputTokens != 12 || u.OutputTokens != 3 || u.Cost == nil || *u.Cost != 0 || u.Allowance == nil || *u.Allowance != 0.0021 || u.Billing != "go" {
		t.Fatalf("usage = %+v", u)
	}
	if opts, _ := body["stream_options"].(map[string]any); opts["include_usage"] != true {
		t.Fatalf("stream_options = %v", body["stream_options"])
	}
	// OpenCode Go routes on the session and refuses a generic SDK name.
	if auth != "Bearer none" || session != "homelab-draft-x" || agent != UserAgent {
		t.Fatalf("auth = %q, session = %q, user agent = %q", auth, session, agent)
	}
	msgs := body["messages"].([]any)
	if body["model"] != "llama3.1" || len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" || msgs[1].(map[string]any)["content"] != "question" {
		t.Fatalf("body = %v", body)
	}
}

func newSettings(t *testing.T) *Settings {
	t.Helper()
	st, _ := storetest.Open(t)
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "keys.txt")
	if err := os.WriteFile(keyFile, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return NewSettings(st, keyFile, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func problemCode(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

func TestSettingsKeepTheKeyOutOfEveryAnswer(t *testing.T) {
	s := newSettings(t)
	ctx := context.Background()
	if g, err := s.Get(ctx); err != nil || g.Provider != KindNone {
		t.Fatalf("fresh = %+v, %v", g, err)
	}
	if _, err := s.Set(ctx, AssistantSettingsSet{Provider: KindAnthropic}); problemCode(err) != "api_key_required" {
		t.Fatalf("no key: %v", err)
	}
	key := "sk-ant-secret"
	got, err := s.Set(ctx, AssistantSettingsSet{Provider: KindAnthropic, APIKey: &key})
	if err != nil || !got.KeySet || got.Model != AnthropicModel {
		t.Fatalf("set = %+v, %v", got, err)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), key) {
		t.Fatalf("the key is in the answer: %s", b)
	}
	stored, _, _ := s.st.Meta(ctx, metaKey)
	if stored == "" || strings.Contains(stored, key) {
		t.Fatalf("stored key = %q", stored)
	}
	// The provider gets the plain key.
	var cfg Config
	s.newProvider = func(c Config) (Provider, error) { cfg = c; return New(c) }
	if _, _, err := s.Provider(ctx); err != nil || cfg.APIKey != key {
		t.Fatalf("provider config = %+v, %v", cfg, err)
	}
	// A new model keeps the key; another provider drops it.
	if got, _ := s.Set(ctx, AssistantSettingsSet{Provider: KindAnthropic, Model: "claude-sonnet-5-5"}); !got.KeySet || got.Model != "claude-sonnet-5-5" {
		t.Fatalf("model change = %+v", got)
	}
	got, err = s.Set(ctx, AssistantSettingsSet{Provider: KindOpenAI, BaseURL: "http://192.168.1.10:11434/v1/", Model: "llama3.1"})
	if err != nil || got.KeySet || got.BaseURL != "http://192.168.1.10:11434/v1" {
		t.Fatalf("openai = %+v, %v", got, err)
	}
	// A pasted endpoint is cut back to the base URL.
	if got, err := s.Set(ctx, AssistantSettingsSet{Provider: KindOpenAI, BaseURL: " https://opencode.ai/zen/go/v1/chat/completions/ ", Model: "m"}); err != nil || got.BaseURL != "https://opencode.ai/zen/go/v1" {
		t.Fatalf("endpoint = %+v, %v", got, err)
	}
	if _, err := s.Set(ctx, AssistantSettingsSet{Provider: KindOpenAI, BaseURL: "ftp://x", Model: "m"}); problemCode(err) != "invalid_base_url" {
		t.Fatalf("bad url: %v", err)
	}
	if got, err := s.Set(ctx, AssistantSettingsSet{Provider: KindNone}); err != nil || got.Provider != KindNone {
		t.Fatalf("off = %+v, %v", got, err)
	}
	if _, _, err := s.Provider(ctx); !errors.Is(err, ErrOff) {
		t.Fatalf("provider when off: %v", err)
	}
}

func TestSettingsTestAsksForOneWord(t *testing.T) {
	s := newSettings(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, anthropicStream("end_turn", "OK")...)
	}))
	defer srv.Close()
	key := "k"
	if _, err := s.Set(ctx, AssistantSettingsSet{Provider: KindAnthropic, BaseURL: srv.URL, Model: "m", APIKey: &key}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Test(ctx)
	if err != nil || !r.OK || r.Reply != "OK" || r.Model != "m" {
		t.Fatalf("test = %+v, %v", r, err)
	}
}

func TestSettingsListModelsWithTheStoredKey(t *testing.T) {
	s := newSettings(t)
	ctx := context.Background()
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		auth = append(auth, r.Header.Get("Authorization")+r.Header.Get("X-Api-Key"))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"qwen","object":"model"},{"id":"deepseek-v4.1-flash","object":"model"}]}`)
	}))
	defer srv.Close()
	key := "sk-stored"
	if _, err := s.Set(ctx, AssistantSettingsSet{Provider: KindOpenAI, BaseURL: srv.URL + "/v1", Model: "m", APIKey: &key}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Models(ctx, AssistantModelsQuery{Provider: KindOpenAI, BaseURL: srv.URL + "/v1/chat/completions"})
	if err != nil || strings.Join(got.Models, ",") != "deepseek-v4.1-flash,qwen" {
		t.Fatalf("models = %+v, %v", got, err)
	}
	// A typed key wins; another provider never gets the stored one.
	typed := "sk-typed"
	if _, err := s.Models(ctx, AssistantModelsQuery{Provider: KindOpenAI, BaseURL: srv.URL + "/v1", APIKey: &typed}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Models(ctx, AssistantModelsQuery{Provider: KindAnthropic, BaseURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if len(auth) != 3 || auth[0] != "Bearer sk-stored" || auth[1] != "Bearer sk-typed" || strings.Contains(auth[2], "sk-") {
		t.Fatalf("auth = %q", auth)
	}
}

func TestSettingsReadOpenCodeGoLimits(t *testing.T) {
	s := newSettings(t)
	ctx := context.Background()
	found := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage" || !found {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"usage":{"rolling":{"status":"ok","percent":12.5,"resetsAt":"2026-11-18T00:54:55.133Z"},"weekly":{"status":"ok","percent":3,"resetsAt":"2026-11-23T00:00:00.000Z"},"monthly":{"status":"ok","percent":1,"resetsAt":"2026-12-11T22:37:37.000Z"}}}`)
	}))
	defer srv.Close()
	if _, err := s.Set(ctx, AssistantSettingsSet{Provider: KindOpenAI, BaseURL: srv.URL + "/v1", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Limits(ctx)
	if err != nil || len(got.Limits) != 3 || got.Limits[0].Window != "rolling" || got.Limits[0].Percent != 12.5 || got.Limits[2].ResetsAt == nil {
		t.Fatalf("limits = %+v, %v", got, err)
	}
	// Any other server has none.
	found = false
	if got, err := s.Limits(ctx); err != nil || got.Limits == nil || len(got.Limits) != 0 {
		t.Fatalf("no endpoint = %+v, %v", got, err)
	}
}

func TestOpenAICompatibleSendsReasoningApart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delta := func(d string) [2]string {
			return [2]string{"", `{"id":"c1","object":"chat.completion.chunk","created":1,"model":"glm","choices":[{"index":0,"delta":` + d + `,"finish_reason":null}]}`}
		}
		// GLM and DeepSeek name it reasoning_content, OpenRouter reasoning.
		sse(w, delta(`{"role":"assistant","reasoning_content":"Look at "}`), delta(`{"reasoning":"the log."}`),
			delta(`{"content":"Restart it."}`),
			[2]string{"", `{"id":"c1","object":"chat.completion.chunk","created":1,"model":"glm","choices":[],"usage":{"prompt_tokens":9,"completion_tokens":40,"completion_tokens_details":{"reasoning_tokens":35}}}`},
			[2]string{"", "[DONE]"})
	}))
	defer srv.Close()
	p, _ := New(Config{Kind: KindOpenAI, BaseURL: srv.URL + "/v1", Model: "glm"})
	var thought strings.Builder
	got, u, err := stream(p, Request{User: "q", OnThinking: func(s string) { thought.WriteString(s) }})
	if err != nil || got != "Restart it." || thought.String() != "Look at the log." {
		t.Fatalf("answer %q, thinking %q, %v", got, thought.String(), err)
	}
	if u.OutputTokens != 40 || u.ReasoningTokens != 35 {
		t.Fatalf("usage = %+v", u)
	}
	// Without OnThinking the reasoning is dropped, not mixed into the answer.
	if got, _, _ := stream(p, Request{User: "q"}); got != "Restart it." {
		t.Fatalf("got %q", got)
	}
}

func TestAnthropicSendsThinkingApart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ev := anthropicStream("end_turn", "Done.")
		thinking := [][2]string{
			{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Hmm."}}`},
			{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		}
		sse(w, append(append([][2]string{ev[0]}, thinking...), ev[1:]...)...)
	}))
	defer srv.Close()
	p, _ := New(Config{Kind: KindAnthropic, BaseURL: srv.URL, Model: "claude-haiku-5-5", APIKey: "k"})
	var thought strings.Builder
	got, _, err := stream(p, Request{User: "q", OnThinking: func(s string) { thought.WriteString(s) }})
	if err != nil || got != "Done." || thought.String() != "Hmm." {
		t.Fatalf("answer %q, thinking %q, %v", got, thought.String(), err)
	}
}

func TestAuditModelFollowsItsProvider(t *testing.T) {
	s := newSettings(t)
	ctx := context.Background()
	var made []Config
	s.newProvider = func(c Config) (Provider, error) { made = append(made, c); return nil, nil }
	key, audit := "sk-ant-secret", "claude-fable-5-2"
	if _, err := s.Set(ctx, AssistantSettingsSet{Provider: KindAnthropic, APIKey: &key, AuditModel: &audit}); err != nil {
		t.Fatal(err)
	}
	if _, model, err := s.AuditProvider(ctx); err != nil || model != audit || made[0].Model != audit {
		t.Fatalf("audit provider = %q, %v, %+v", model, err, made)
	}
	if _, model, _ := s.Provider(ctx); model != AnthropicModel {
		t.Fatalf("main provider = %q", model)
	}
	// Absent keeps it.
	if g, _ := s.Set(ctx, AssistantSettingsSet{Provider: KindAnthropic}); g.AuditModel != audit {
		t.Fatalf("kept = %+v", g)
	}
	// Empty falls back to the main model.
	empty := ""
	if _, err := s.Set(ctx, AssistantSettingsSet{Provider: KindAnthropic, AuditModel: &empty}); err != nil {
		t.Fatal(err)
	}
	if _, model, _ := s.AuditProvider(ctx); model != AnthropicModel {
		t.Fatalf("fallback = %q", model)
	}
	// Another provider drops it, like the key.
	if _, err := s.Set(ctx, AssistantSettingsSet{Provider: KindAnthropic, AuditModel: &audit}); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.Set(ctx, AssistantSettingsSet{Provider: KindOpenAI, BaseURL: "http://127.0.0.1:11434/v1", Model: "llama3.1"}); g.AuditModel != "" {
		t.Fatalf("after a provider change = %+v", g)
	}
}
