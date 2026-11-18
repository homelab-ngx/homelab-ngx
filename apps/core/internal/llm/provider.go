// Package llm is the optional language model the dashboard's stack editor
// asks for help: Claude (Anthropic's API) or any OpenAI-compatible endpoint
// (OpenAI, Ollama, LM Studio, OpenRouter, vLLM). A request is one system and
// one user text in and the answer's text out, streamed. The model gets no
// tools and runs no loop.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	aoption "github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/openai/openai-go/v3"
	ooption "github.com/openai/openai-go/v3/option"
)

// Kind is the API a provider speaks.
type Kind string

const (
	KindNone      Kind = "none"
	KindAnthropic Kind = "anthropic"
	KindOpenAI    Kind = "openai"
)

// Default base URLs and the model a new Anthropic setup starts with.
const (
	AnthropicURL   = "https://api.anthropic.com"
	OpenAIURL      = "https://api.openai.com/v1"
	AnthropicModel = "claude-opus-5-5"
)

// Request is one question.
type Request struct {
	// System is the stable part (the authoring guide); a provider that
	// caches prompts caches it.
	System string
	// User is the question with the files and the latest output.
	User string
	// MaxTokens bounds the answer; 0 is DefaultMaxTokens.
	MaxTokens int64
	// Session names the conversation, the same for each question about one
	// thing. An OpenAI-compatible request carries it as x-opencode-session,
	// which OpenCode Go requires and other servers ignore.
	Session string
	// OnThinking, when set, gets the model's reasoning as it streams, from a
	// server that sends it (reasoning_content, reasoning, or Claude's
	// thinking). It is never part of the answer.
	OnThinking func(string)
	// Reasoning asks for the model's deeper reasoning where the API has a
	// switch for it: Claude thinks adaptively at high effort. An
	// OpenAI-compatible request is unchanged; a reasoning model reasons.
	Reasoning bool
}

// UserAgent is how the daemon names itself to a provider; OpenCode Go
// refuses a generic SDK name.
const UserAgent = "homelab-assistant/1.0"

// DefaultMaxTokens fits an answer with both files in full.
const DefaultMaxTokens = 32000

// AnswerUsage is what one answer cost, as far as the server says.
type AnswerUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	// ReasoningTokens is the part of OutputTokens the model spent thinking,
	// when the server says.
	ReasoningTokens int64    `json:"reasoning_tokens,omitempty" doc:"The part of output_tokens spent on reasoning, when the server says."`
	Cost            *float64 `json:"cost,omitempty" doc:"Dollars charged, when the server reports it (OpenCode, OpenRouter)."`
	Allowance       *float64 `json:"allowance,omitempty" doc:"OpenCode Go: dollars of the plan's monthly limit the answer used."`
	Billing         string   `json:"billing,omitempty" doc:"OpenCode: what paid, such as free, balance, go or byok."`
}

// Provider answers a request, calling onText with each piece of the answer
// as it arrives, and returns what the answer cost.
type Provider interface {
	Stream(ctx context.Context, r Request, onText func(string)) (AnswerUsage, error)
	// Models lists the model ids the server offers, sorted.
	Models(ctx context.Context) ([]string, error)
}

// Config is what a provider is made from.
type Config struct {
	Kind    Kind
	BaseURL string
	Model   string
	APIKey  string
}

// ErrOff means no provider is set up.
var ErrOff = errors.New("no assistant is set up")

// New returns the provider c describes.
func New(c Config) (Provider, error) {
	switch c.Kind {
	case KindAnthropic:
		return newAnthropic(c), nil
	case KindOpenAI:
		return newOpenAI(c), nil
	default:
		return nil, ErrOff
	}
}

// ── Anthropic ───────────────────────────────────────────────────────────

type anthropicProvider struct {
	client anthropic.Client
	model  string
}

func newAnthropic(c Config) *anthropicProvider {
	opts := []aoption.RequestOption{aoption.WithAPIKey(c.APIKey), aoption.WithMaxRetries(2), aoption.WithHeader("User-Agent", UserAgent)}
	if c.BaseURL != "" && c.BaseURL != AnthropicURL {
		opts = append(opts, aoption.WithBaseURL(c.BaseURL))
	}
	return &anthropicProvider{client: anthropic.NewClient(opts...), model: c.Model}
}

// fallbackModels are the models that take server-side refusal fallbacks:
// a request a safety classifier declines is answered by another model in
// the same call instead of stopping.
var fallbackModels = []string{"claude-opus-5-5", "claude-opus-5", "claude-fable-5-1", "claude-fable-5-2", "claude-sonnet-5-5"}

func (p *anthropicProvider) Stream(ctx context.Context, r Request, onText func(string)) (AnswerUsage, error) {
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(p.model),
		MaxTokens: maxTokens(r),
		// The guide is the same on every request: cached, a follow-up
		// question within minutes reads it at a tenth of the price.
		System: []anthropic.BetaTextBlockParam{{
			Text:         r.System,
			CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
		}},
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(r.User)),
		},
	}
	if r.Reasoning {
		params.Thinking = anthropic.BetaThinkingConfigParamUnion{OfAdaptive: &anthropic.BetaThinkingConfigAdaptiveParam{
			Display: anthropic.BetaThinkingConfigAdaptiveDisplaySummarized,
		}}
		params.OutputConfig = anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortHigh}
	}
	if slices.Contains(fallbackModels, p.model) {
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		params.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
	}
	stream := p.client.Beta.Messages.NewStreaming(ctx, params)
	defer stream.Close()
	stop := ""
	var u AnswerUsage
	for stream.Next() {
		switch ev := stream.Current().AsAny().(type) {
		case anthropic.BetaRawMessageStartEvent:
			u.InputTokens = ev.Message.Usage.InputTokens
		case anthropic.BetaRawContentBlockDeltaEvent:
			switch d := ev.Delta.AsAny().(type) {
			case anthropic.BetaTextDelta:
				if d.Text != "" {
					onText(d.Text)
				}
			case anthropic.BetaThinkingDelta:
				if d.Thinking != "" && r.OnThinking != nil {
					r.OnThinking(d.Thinking)
				}
			}
		case anthropic.BetaRawMessageDeltaEvent:
			if ev.Delta.StopReason != "" {
				stop = string(ev.Delta.StopReason)
			}
			u.read(ev.Usage.RawJSON())
		}
	}
	if err := stream.Err(); err != nil {
		return u, describe(err)
	}
	return u, stopError(stop)
}

func (p *anthropicProvider) Models(ctx context.Context) ([]string, error) {
	var ids []string
	pages := p.client.Models.ListAutoPaging(ctx, anthropic.ModelListParams{Limit: anthropic.Int(1000)})
	for pages.Next() {
		ids = append(ids, pages.Current().ID)
	}
	if err := pages.Err(); err != nil {
		return nil, describe(err)
	}
	slices.Sort(ids)
	return ids, nil
}

// ── OpenAI-compatible ───────────────────────────────────────────────────

type openAIProvider struct {
	client openai.Client
	model  string
}

func newOpenAI(c Config) *openAIProvider {
	key := c.APIKey
	if key == "" {
		// Local servers (Ollama, LM Studio) take no key, but the client
		// would read OPENAI_API_KEY from the environment without one.
		key = "none"
	}
	base := c.BaseURL
	if base == "" {
		base = OpenAIURL
	}
	return &openAIProvider{
		client: openai.NewClient(ooption.WithAPIKey(key), ooption.WithBaseURL(base), ooption.WithMaxRetries(2),
			ooption.WithHeader("User-Agent", UserAgent)),
		model: c.Model,
	}
}

func (p *openAIProvider) Stream(ctx context.Context, r Request, onText func(string)) (AnswerUsage, error) {
	var opts []ooption.RequestOption
	if r.Session != "" {
		opts = append(opts, ooption.WithHeader("x-opencode-session", r.Session))
	}
	stream := p.client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
		Model: p.model,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(r.System),
			openai.UserMessage(r.User),
		},
		MaxCompletionTokens: openai.Int(maxTokens(r)),
		// The last chunk then carries the usage, and OpenCode's cost in it.
		StreamOptions: openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)},
	}, opts...)
	defer stream.Close()
	stop := ""
	var u AnswerUsage
	for stream.Next() {
		chunk := stream.Current()
		u.read(chunk.Usage.RawJSON())
		for _, c := range chunk.Choices {
			if r.OnThinking != nil {
				if t := thinkingOf(c.Delta.RawJSON()); t != "" {
					r.OnThinking(t)
				}
			}
			if c.Delta.Content != "" {
				onText(c.Delta.Content)
			}
			if c.FinishReason != "" {
				stop = c.FinishReason
			}
		}
	}
	if err := stream.Err(); err != nil {
		return u, describe(err)
	}
	return u, stopError(stop)
}

func (p *openAIProvider) Models(ctx context.Context) ([]string, error) {
	var ids []string
	pages := p.client.Models.ListAutoPaging(ctx)
	for pages.Next() {
		ids = append(ids, pages.Current().ID)
	}
	if err := pages.Err(); err != nil {
		return nil, describe(err)
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

// Limit is how much of one usage window is spent.
type Limit struct {
	Window   string     `json:"window" doc:"rolling (OpenCode Go's five hours), weekly or monthly."`
	Percent  float64    `json:"percent" doc:"How much of the window's allowance is spent, 0 to 100."`
	Status   string     `json:"status" doc:"The server's word for it, such as ok."`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
}

// LimitReader is a provider whose server reports usage limits.
type LimitReader interface {
	// Limits is nil when the server reports none.
	Limits(ctx context.Context) ([]Limit, error)
}

// Limits reads OpenCode Go's GET /usage. A server without it (a 404) has
// no limits to report.
func (p *openAIProvider) Limits(ctx context.Context) ([]Limit, error) {
	var out struct {
		Usage map[string]struct {
			Status   string     `json:"status"`
			Percent  float64    `json:"percent"`
			ResetsAt *time.Time `json:"resetsAt"`
		} `json:"usage"`
	}
	if err := p.client.Get(ctx, "usage", nil, &out); err != nil {
		var oe *openai.Error
		if errors.As(err, &oe) && (oe.StatusCode == http.StatusNotFound || oe.StatusCode == http.StatusMethodNotAllowed) {
			return nil, nil
		}
		return nil, describe(err)
	}
	var limits []Limit
	for _, w := range []string{"rolling", "weekly", "monthly"} {
		if u, ok := out.Usage[w]; ok {
			limits = append(limits, Limit{Window: w, Percent: u.Percent, Status: u.Status, ResetsAt: u.ResetsAt})
		}
	}
	return limits, nil
}

// ── Shared ──────────────────────────────────────────────────────────────

func maxTokens(r Request) int64 {
	if r.MaxTokens > 0 {
		return r.MaxTokens
	}
	return DefaultMaxTokens
}

// thinkingOf is the reasoning in an OpenAI-compatible delta: servers name
// it reasoning_content (DeepSeek, GLM, vLLM) or reasoning (OpenRouter,
// Ollama).
func thinkingOf(raw string) string {
	if !strings.Contains(raw, `"reasoning`) {
		return ""
	}
	var v struct {
		ReasoningContent string `json:"reasoning_content"`
		Reasoning        string `json:"reasoning"`
	}
	if json.Unmarshal([]byte(raw), &v) != nil {
		return ""
	}
	return v.ReasoningContent + v.Reasoning
}

// read takes what a usage object in an answer holds: OpenAI's or
// Anthropic's token counts, and the cost OpenCode and OpenRouter add. An
// absent or null usage changes nothing.
func (u *AnswerUsage) read(raw string) {
	var v struct {
		PromptTokens     *int64 `json:"prompt_tokens"`
		CompletionTokens *int64 `json:"completion_tokens"`
		InputTokens      *int64 `json:"input_tokens"`
		OutputTokens     *int64 `json:"output_tokens"`
		Completion       struct {
			Reasoning *int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
		Cost     json.RawMessage `json:"cost"`
		OpenCode struct {
			BillingSource string          `json:"billing_source"`
			Allowance     json.RawMessage `json:"allowance"`
		} `json:"opencode"`
	}
	if raw == "" || raw == "null" || json.Unmarshal([]byte(raw), &v) != nil {
		return
	}
	for _, n := range []struct{ from, to *int64 }{{v.PromptTokens, &u.InputTokens}, {v.InputTokens, &u.InputTokens}, {v.CompletionTokens, &u.OutputTokens}, {v.OutputTokens, &u.OutputTokens}} {
		if n.from != nil && *n.from > 0 {
			*n.to = *n.from
		}
	}
	if v.Completion.Reasoning != nil && *v.Completion.Reasoning > 0 {
		u.ReasoningTokens = *v.Completion.Reasoning
	}
	if c := number(v.Cost); c != nil {
		u.Cost = c
	}
	if a := number(v.OpenCode.Allowance); a != nil {
		u.Allowance = a
	}
	if v.OpenCode.BillingSource != "" {
		u.Billing = v.OpenCode.BillingSource
	}
}

// number reads a JSON number, or a number in a string; nil otherwise.
func number(raw json.RawMessage) *float64 {
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return &f
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return &f
		}
	}
	return nil
}

// stopError is an answer that ended short: cut at the token limit, or
// declined.
func stopError(stop string) error {
	switch stop {
	case "max_tokens", "length":
		return errors.New("the answer was cut off at the token limit")
	case "refusal", "content_filter":
		return errors.New("the model declined to answer this request")
	}
	return nil
}

// describe shortens an SDK error to its status and the provider's message.
func describe(err error) error {
	var ae *anthropic.Error
	if errors.As(err, &ae) {
		return fmt.Errorf("the provider answered %d: %s", ae.StatusCode, apiMessage(ae.RawJSON(), ae.Error()))
	}
	var oe *openai.Error
	if errors.As(err, &oe) {
		return fmt.Errorf("the provider answered %d: %s", oe.StatusCode, apiMessage(oe.RawJSON(), oe.Error()))
	}
	return err
}

// apiMessage is the "message" of an API error body, else fallback.
func apiMessage(raw, fallback string) string {
	for _, key := range []string{`"message":"`, `"message": "`} {
		if _, rest, ok := strings.Cut(raw, key); ok {
			if msg, _, ok := strings.Cut(rest, `"`); ok && msg != "" {
				return msg
			}
		}
	}
	return fallback
}

// AssistText is a piece of a streamed answer as it arrives: the "text"
// event of every assistant stream.
type AssistText struct {
	Text string `json:"text"`
}

// AssistError ends an assistant stream that failed: its "error" event.
type AssistError struct {
	Message string `json:"message"`
}

// AssistThinking is a piece of the model's reasoning as it streams: the
// "thinking" event. It shows that the model works; it is not the answer.
type AssistThinking struct {
	Text string `json:"text"`
}

// Timer notes when an answer's reasoning and text began, for the log.
type Timer struct {
	start                    time.Time
	firstThinking, firstText time.Duration
	thinkingChars, textChars int
}

// StartTimer starts timing an answer.
func StartTimer() *Timer { return &Timer{start: time.Now()} }

// Thinking notes a piece of reasoning.
func (t *Timer) Thinking(s string) {
	if t.firstThinking == 0 {
		t.firstThinking = time.Since(t.start)
	}
	t.thinkingChars += len(s)
}

// Text notes a piece of the answer.
func (t *Timer) Text(s string) {
	if t.firstText == 0 {
		t.firstText = time.Since(t.start)
	}
	t.textChars += len(s)
}

// LogArgs are the timings and sizes as slog attributes.
func (t *Timer) LogArgs() []any {
	return []any{"seconds", int(time.Since(t.start).Seconds()),
		"first_thinking_seconds", round1(t.firstThinking), "first_text_seconds", round1(t.firstText),
		"thinking_chars", t.thinkingChars, "chars", t.textChars}
}

func round1(d time.Duration) float64 { return float64(d.Milliseconds()/100) / 10 }
