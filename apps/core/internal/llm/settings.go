package llm

import (
	"context"
	"encoding/base64"
	"log/slog"
	"net/url"
	"strings"
	"time"
	"unicode"

	"homelab-ngx.com/core/internal/agefile"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/store"
)

// The settings live in the meta table. The API key is sealed to the host's
// age key, so a database snapshot holds it unreadable; it is never returned.
const (
	metaKind  = "assistant_provider"
	metaURL   = "assistant_base_url"
	metaModel = "assistant_model"
	metaKey   = "assistant_api_key"
	// metaAuditModel is the model a security audit asks, on the same
	// provider; empty is the main model.
	metaAuditModel = "assistant_audit_model"
)

// AssistantSettings is the assistant's setup as the API shows it.
type AssistantSettings struct {
	Provider Kind   `json:"provider" enum:"none,anthropic,openai" doc:"none (off), anthropic (Claude) or openai (any OpenAI-compatible endpoint: OpenAI, Ollama, LM Studio, OpenRouter, vLLM)."`
	BaseURL  string `json:"base_url" doc:"The API's address; empty is the provider's own."`
	Model    string `json:"model"`
	// AuditModel is the model a security audit asks; empty is Model.
	AuditModel string `json:"audit_model" doc:"The model a security audit asks, on the same provider: a more capable one that reasons. Empty uses model."`
	KeySet     bool   `json:"key_set" doc:"An API key is stored. The key itself is never shown."`
}

// AssistantSettingsSet changes the setup.
type AssistantSettingsSet struct {
	Provider Kind    `json:"provider" enum:"none,anthropic,openai"`
	BaseURL  string  `json:"base_url,omitempty" maxLength:"500" doc:"Empty uses the provider's own: https://api.anthropic.com or https://api.openai.com/v1."`
	Model    string  `json:"model,omitempty" maxLength:"200" doc:"The model's id; empty for anthropic is claude-opus-5-5."`
	APIKey   *string `json:"api_key,omitempty" maxLength:"4096" doc:"Absent keeps the stored key (dropped when the provider changes); empty removes it."`
	// AuditModel is the security audit's model; absent keeps the stored one.
	AuditModel *string `json:"audit_model,omitempty" maxLength:"200" doc:"The model a security audit asks. Absent keeps the stored one (dropped when the provider changes); empty uses model."`
}

// AssistantModelsQuery names the server to list models from, which may
// not be applied yet.
type AssistantModelsQuery struct {
	Provider Kind    `json:"provider" enum:"anthropic,openai"`
	BaseURL  string  `json:"base_url,omitempty" maxLength:"500" doc:"Empty is the provider's own."`
	APIKey   *string `json:"api_key,omitempty" maxLength:"4096" doc:"Absent uses the stored key when the provider is the stored one."`
}

// AssistantModels is what a server offers.
type AssistantModels struct {
	Models []string `json:"models" doc:"Model ids, sorted."`
}

// AssistantLimits is how much of the provider's allowance is spent.
type AssistantLimits struct {
	Limits []Limit `json:"limits" doc:"Empty when the server reports none; only OpenCode Go does."`
}

// AssistantTest is the answer to a test request.
type AssistantTest struct {
	OK      bool        `json:"ok"`
	Model   string      `json:"model"`
	Reply   string      `json:"reply,omitempty" doc:"What the model answered, cut short."`
	Seconds int         `json:"seconds" doc:"How long the answer took."`
	Usage   AnswerUsage `json:"usage" doc:"What the answer cost, as far as the server says."`
	Error   string      `json:"error,omitempty"`
}

// Settings keeps the assistant's setup and makes its provider.
type Settings struct {
	st      *store.Store
	keyFile string
	log     *slog.Logger
	// newProvider is New; a seam for tests.
	newProvider func(Config) (Provider, error)
}

// NewSettings returns the settings. keyFile holds the host's age identity.
func NewSettings(st *store.Store, keyFile string, log *slog.Logger) *Settings {
	return &Settings{st: st, keyFile: keyFile, log: log, newProvider: New}
}

func (s *Settings) meta(ctx context.Context, key string) (string, error) {
	v, _, err := s.st.Meta(ctx, key)
	return v, err
}

// Get returns the setup.
func (s *Settings) Get(ctx context.Context) (AssistantSettings, error) {
	kind, err := s.meta(ctx, metaKind)
	if err != nil {
		return AssistantSettings{}, err
	}
	out := AssistantSettings{Provider: Kind(kind)}
	if out.Provider != KindAnthropic && out.Provider != KindOpenAI {
		return AssistantSettings{Provider: KindNone}, nil
	}
	if out.BaseURL, err = s.meta(ctx, metaURL); err != nil {
		return AssistantSettings{}, err
	}
	if out.Model, err = s.meta(ctx, metaModel); err != nil {
		return AssistantSettings{}, err
	}
	if out.AuditModel, err = s.meta(ctx, metaAuditModel); err != nil {
		return AssistantSettings{}, err
	}
	sealed, err := s.meta(ctx, metaKey)
	out.KeySet = sealed != ""
	return out, err
}

// Set changes the setup. Turning it off forgets the key too.
func (s *Settings) Set(ctx context.Context, in AssistantSettingsSet) (AssistantSettings, error) {
	old, err := s.Get(ctx)
	if err != nil {
		return AssistantSettings{}, err
	}
	if in.Provider == KindNone || in.Provider == "" {
		for _, k := range []string{metaKind, metaURL, metaModel, metaKey, metaAuditModel} {
			if err := s.st.Queries().DeleteMeta(ctx, k); err != nil {
				return AssistantSettings{}, err
			}
		}
		s.log.Info("assistant turned off")
		return AssistantSettings{Provider: KindNone}, nil
	}
	if in.Provider != KindAnthropic && in.Provider != KindOpenAI {
		return AssistantSettings{}, httpx.BadRequest("invalid_provider", "the provider is none, anthropic or openai")
	}
	base, err := baseURL(in.Provider, in.BaseURL)
	if err != nil {
		return AssistantSettings{}, err
	}
	model := strings.TrimSpace(in.Model)
	if model == "" && in.Provider == KindAnthropic {
		model = AnthropicModel
	}
	if model == "" {
		return AssistantSettings{}, httpx.Unprocessable("model_required", "name the model to use, such as llama3.1 or gpt-5")
	}
	if strings.IndexFunc(model, unicode.IsSpace) >= 0 {
		return AssistantSettings{}, httpx.Unprocessable("invalid_model", "a model id has no spaces")
	}
	// A key belongs to its provider: changing the provider drops it unless
	// a new one comes with the change.
	sealed, err := s.meta(ctx, metaKey)
	if err != nil {
		return AssistantSettings{}, err
	}
	audit := old.AuditModel
	if old.Provider != in.Provider {
		sealed, audit = "", ""
	}
	if in.AuditModel != nil {
		audit = strings.TrimSpace(*in.AuditModel)
	}
	if strings.IndexFunc(audit, unicode.IsSpace) >= 0 {
		return AssistantSettings{}, httpx.Unprocessable("invalid_model", "a model id has no spaces")
	}
	if in.APIKey != nil {
		sealed = ""
		if key := strings.TrimSpace(*in.APIKey); key != "" {
			b, err := agefile.Seal(s.keyFile, []byte(key))
			if err != nil {
				return AssistantSettings{}, httpx.Internal("key_not_sealed", "the API key could not be stored: %v", err)
			}
			sealed = base64.StdEncoding.EncodeToString(b)
		}
	}
	if in.Provider == KindAnthropic && sealed == "" && (base == "" || base == AnthropicURL) {
		return AssistantSettings{}, httpx.Unprocessable("api_key_required", "Claude needs an API key from console.anthropic.com")
	}
	err = s.st.Tx(ctx, func(q *store.Queries) error {
		for k, v := range map[string]string{metaKind: string(in.Provider), metaURL: base, metaModel: model, metaKey: sealed, metaAuditModel: audit} {
			if err := q.SetMeta(ctx, store.SetMetaParams{Key: k, Value: v}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return AssistantSettings{}, err
	}
	s.log.Info("assistant set up", "provider", in.Provider, "base_url", base, "model", model, "audit_model", audit, "key_set", sealed != "")
	return s.Get(ctx)
}

// endpointSuffixes are the paths a client adds to a base URL; a pasted
// endpoint loses them, so .../v1/chat/completions is .../v1.
var endpointSuffixes = map[Kind]string{KindOpenAI: "/chat/completions", KindAnthropic: "/v1/messages"}

// baseURL checks and tidies an address: no trailing slash, no endpoint path.
func baseURL(kind Kind, raw string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	base = strings.TrimRight(strings.TrimSuffix(base, endpointSuffixes[kind]), "/")
	if base == "" {
		return "", nil
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return "", httpx.Unprocessable("invalid_base_url", "%q is not an http(s) address such as http://localhost:11434/v1", raw)
	}
	return base, nil
}

// storedKey is the plain API key; empty when none is stored.
func (s *Settings) storedKey(ctx context.Context) (string, error) {
	sealed, err := s.meta(ctx, metaKey)
	if err != nil || sealed == "" {
		return "", err
	}
	b, err := base64.StdEncoding.DecodeString(sealed)
	if err == nil {
		b, err = agefile.Open(s.keyFile, b)
	}
	if err != nil {
		return "", httpx.Internal("key_not_readable", "the stored API key could not be read: %v; enter it again in Settings", err)
	}
	return string(b), nil
}

// Models lists the models a server offers. The server need not be the
// applied one, so the form can list before Apply; the stored key goes only
// with its own provider.
func (s *Settings) Models(ctx context.Context, in AssistantModelsQuery) (AssistantModels, error) {
	if in.Provider != KindAnthropic && in.Provider != KindOpenAI {
		return AssistantModels{}, httpx.BadRequest("invalid_provider", "the provider is anthropic or openai")
	}
	base, err := baseURL(in.Provider, in.BaseURL)
	if err != nil {
		return AssistantModels{}, err
	}
	key := ""
	if in.APIKey != nil {
		key = strings.TrimSpace(*in.APIKey)
	} else if old, err := s.Get(ctx); err != nil {
		return AssistantModels{}, err
	} else if old.Provider == in.Provider {
		if key, err = s.storedKey(ctx); err != nil {
			return AssistantModels{}, err
		}
	}
	p, err := s.newProvider(Config{Kind: in.Provider, BaseURL: base, APIKey: key})
	if err != nil {
		return AssistantModels{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ids, err := p.Models(ctx)
	if err != nil {
		return AssistantModels{}, httpx.BadGateway("models_failed", "the model list could not be read: %v", err)
	}
	if ids == nil {
		ids = []string{}
	}
	return AssistantModels{Models: ids}, nil
}

// Provider returns the configured provider and its model; ErrOff when none
// is set up.
func (s *Settings) Provider(ctx context.Context) (Provider, string, error) {
	return s.provider(ctx, false)
}

// AuditProvider is Provider with the audit model, or the main model when no
// audit model is set.
func (s *Settings) AuditProvider(ctx context.Context) (Provider, string, error) {
	return s.provider(ctx, true)
}

func (s *Settings) provider(ctx context.Context, audit bool) (Provider, string, error) {
	c, err := s.Get(ctx)
	if err != nil {
		return nil, "", err
	}
	if c.Provider == KindNone {
		return nil, "", ErrOff
	}
	key, err := s.storedKey(ctx)
	if err != nil {
		return nil, "", err
	}
	model := c.Model
	if audit && c.AuditModel != "" {
		model = c.AuditModel
	}
	p, err := s.newProvider(Config{Kind: c.Provider, BaseURL: c.BaseURL, Model: model, APIKey: key})
	return p, model, err
}

// Limits asks the applied server how much of its allowance is spent.
func (s *Settings) Limits(ctx context.Context) (AssistantLimits, error) {
	p, _, err := s.Provider(ctx)
	if err == ErrOff {
		return AssistantLimits{}, httpx.Conflict("assistant_off", "set up the assistant first")
	}
	if err != nil {
		return AssistantLimits{}, err
	}
	out := AssistantLimits{Limits: []Limit{}}
	r, ok := p.(LimitReader)
	if !ok {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	l, err := r.Limits(ctx)
	if err != nil {
		return AssistantLimits{}, httpx.BadGateway("limits_failed", "the usage limits could not be read: %v", err)
	}
	if l != nil {
		out.Limits = l
	}
	return out, nil
}

// Test asks the model for one word, which proves the address, the key and
// the model in one go.
func (s *Settings) Test(ctx context.Context) (AssistantTest, error) {
	p, model, err := s.Provider(ctx)
	if err != nil {
		if err == ErrOff {
			return AssistantTest{}, httpx.Conflict("assistant_off", "set up the assistant first")
		}
		return AssistantTest{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	start := time.Now()
	var reply strings.Builder
	usage, err := p.Stream(ctx, Request{
		System:    "You answer connection checks.",
		User:      "Reply with the single word OK.",
		MaxTokens: 2000,
		Session:   "homelab-assistant-test",
	}, func(t string) { reply.WriteString(t) })
	out := AssistantTest{OK: err == nil, Model: model, Seconds: int(time.Since(start).Seconds()), Usage: usage}
	if r := strings.TrimSpace(reply.String()); len(r) > 200 {
		out.Reply = r[:200] + "…"
	} else {
		out.Reply = r
	}
	if err != nil {
		out.Error = err.Error()
	} else if out.Reply == "" {
		out.OK, out.Error = false, "the model answered with no text"
	}
	return out, nil
}
