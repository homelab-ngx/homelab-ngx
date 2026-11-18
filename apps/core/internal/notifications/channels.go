package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"homelab-ngx.com/core/internal/agefile"
	"homelab-ngx.com/core/internal/store"
)

// ChannelKind is how a channel delivers.
type ChannelKind string

const (
	KindNtfy    ChannelKind = "ntfy"
	KindWebhook ChannelKind = "webhook"
)

// Schema documents ChannelKind as its enum.
func (ChannelKind) Schema(huma.Registry) *huma.Schema { return enumSchema(KindNtfy, KindWebhook) }

// DefaultNtfyServer is the public ntfy server.
const DefaultNtfyServer = "https://ntfy.sh"

// Channel is a notification channel as the API shows it: no credentials.
type Channel struct {
	ID      int64       `json:"id"`
	Name    string      `json:"name"`
	Kind    ChannelKind `json:"kind"`
	Enabled bool        `json:"enabled"`
	// Server and Topic: ntfy.
	Server string `json:"server,omitempty"`
	Topic  string `json:"topic,omitempty"`
	// URL and CertSHA256: webhook.
	URL        string    `json:"url,omitempty"`
	CertSHA256 string    `json:"cert_sha256,omitempty" doc:"Pin for a self-signed certificate: SHA-256 of its DER encoding, lowercase hex."`
	HasToken   bool      `json:"has_token" doc:"Whether a token is stored; the token itself is never returned."`
	CreatedAt  time.Time `json:"created_at"`
}

// ChannelInput adds or changes a channel.
type ChannelInput struct {
	Name string      `json:"name" minLength:"1" maxLength:"64"`
	Kind ChannelKind `json:"kind" doc:"Set when adding; a channel keeps its kind."`
	// Enabled: absent means enabled when adding and unchanged when editing.
	Enabled    *bool  `json:"enabled,omitempty"`
	Server     string `json:"server,omitempty" doc:"ntfy server; empty is https://ntfy.sh."`
	Topic      string `json:"topic,omitempty" doc:"ntfy topic."`
	URL        string `json:"url,omitempty" doc:"Webhook address the payload is POSTed to."`
	CertSHA256 string `json:"cert_sha256,omitempty" doc:"Webhook over https with a self-signed certificate: its pin."`
	Token      string `json:"token,omitempty" doc:"Bearer token (ntfy access token or webhook token). Empty keeps the stored one when editing."`
	ClearToken bool   `json:"clear_token,omitempty" doc:"Remove the stored token."`
}

// ChannelTest is the answer of one test send.
type ChannelTest struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type channelConfig struct {
	Server     string `json:"server,omitempty"`
	Topic      string `json:"topic,omitempty"`
	URL        string `json:"url,omitempty"`
	CertSHA256 string `json:"cert_sha256,omitempty"`
}

type channelSecret struct {
	Token string `json:"token,omitempty"`
}

// Channel errors the API maps to statuses.
var (
	ErrChannelNotFound = errors.New("no such channel")
	ErrChannelExists   = errors.New("a channel with that name exists")
)

// Channels keeps the notification channels in the runtime store, their
// credentials sealed to the host's age key. It is the Notifier's Targets.
type Channels struct {
	st      *store.Store
	keyFile string
	now     func() time.Time
}

// NewChannels keeps channels in st, sealing credentials with the age
// identities in keyFile.
func NewChannels(st *store.Store, keyFile string) *Channels {
	return &Channels{st: st, keyFile: keyFile, now: time.Now}
}

var (
	topicRE = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)
	pinRE   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// httpURL checks an http(s) address with a host.
func httpURL(field, raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, invalidf("%s: an http or https address", field)
	}
	if u.User != nil {
		return nil, invalidf("%s: put the password in the token, not the address", field)
	}
	return u, nil
}

// check validates in for kind and returns the stored settings.
func check(kind ChannelKind, in ChannelInput) (string, channelConfig, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 64 || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", channelConfig{}, invalidf("name: 1 to 64 printable characters")
	}
	var c channelConfig
	switch kind {
	case KindNtfy:
		c.Server = strings.TrimRight(strings.TrimSpace(in.Server), "/")
		if c.Server == "" {
			c.Server = DefaultNtfyServer
		}
		if _, err := httpURL("server", c.Server); err != nil {
			return "", c, err
		}
		c.Topic = strings.TrimSpace(in.Topic)
		if !topicRE.MatchString(c.Topic) {
			return "", c, invalidf("topic: 1 to 64 letters, digits, - and _")
		}
	case KindWebhook:
		c.URL = strings.TrimSpace(in.URL)
		u, err := httpURL("url", c.URL)
		if err != nil {
			return "", c, err
		}
		c.CertSHA256 = strings.ToLower(strings.TrimSpace(in.CertSHA256))
		if c.CertSHA256 != "" {
			if !pinRE.MatchString(c.CertSHA256) {
				return "", c, invalidf("cert_sha256: 64 hex characters")
			}
			if u.Scheme != "https" {
				return "", c, invalidf("cert_sha256: only for an https address")
			}
		}
	default:
		return "", c, invalidf("kind: ntfy or webhook")
	}
	return name, c, nil
}

func (c *Channels) seal(sec channelSecret) ([]byte, error) {
	plain, err := json.Marshal(sec)
	if err != nil {
		return nil, err
	}
	return agefile.Seal(c.keyFile, plain)
}

func (c *Channels) open(row store.NotificationChannel) (channelSecret, error) {
	var sec channelSecret
	plain, err := agefile.Open(c.keyFile, row.Secret)
	if err != nil {
		return sec, fmt.Errorf("the channel's token does not open: %w", err)
	}
	return sec, json.Unmarshal(plain, &sec)
}

func view(row store.NotificationChannel, hasToken bool) Channel {
	var cfg channelConfig
	_ = json.Unmarshal([]byte(row.Config), &cfg)
	return Channel{
		ID: row.ID, Name: row.Name, Kind: ChannelKind(row.Kind), Enabled: row.Enabled,
		Server: cfg.Server, Topic: cfg.Topic, URL: cfg.URL, CertSHA256: cfg.CertSHA256,
		HasToken: hasToken, CreatedAt: time.UnixMilli(row.CreatedAt),
	}
}

// hasToken reports whether row stores a token; a secret that does not open
// counts as one, since something is stored.
func (c *Channels) hasToken(row store.NotificationChannel) bool {
	sec, err := c.open(row)
	return err != nil || sec.Token != ""
}

// List returns every channel by name.
func (c *Channels) List(ctx context.Context) ([]Channel, error) {
	rows, err := c.st.Queries().ListNotificationChannels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Channel, 0, len(rows))
	for _, r := range rows {
		out = append(out, view(r, c.hasToken(r)))
	}
	return out, nil
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// Create adds a channel.
func (c *Channels) Create(ctx context.Context, in ChannelInput) (Channel, error) {
	name, cfg, err := check(in.Kind, in)
	if err != nil {
		return Channel{}, err
	}
	sealed, err := c.seal(channelSecret{Token: strings.TrimSpace(in.Token)})
	if err != nil {
		return Channel{}, err
	}
	raw, _ := json.Marshal(cfg)
	enabled := in.Enabled == nil || *in.Enabled
	row, err := c.st.Queries().InsertNotificationChannel(ctx, store.InsertNotificationChannelParams{
		Name: name, Kind: string(in.Kind), Config: string(raw), Secret: sealed, Enabled: enabled, CreatedAt: c.now().UnixMilli(),
	})
	if isUnique(err) {
		return Channel{}, ErrChannelExists
	}
	if err != nil {
		return Channel{}, err
	}
	return view(row, strings.TrimSpace(in.Token) != ""), nil
}

func (c *Channels) get(ctx context.Context, id int64) (store.NotificationChannel, error) {
	row, err := c.st.Queries().GetNotificationChannel(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrChannelNotFound
	}
	return row, err
}

// Update changes a channel's name, settings, token or enabled state; its
// kind stays.
func (c *Channels) Update(ctx context.Context, id int64, in ChannelInput) (Channel, error) {
	row, err := c.get(ctx, id)
	if err != nil {
		return Channel{}, err
	}
	kind := ChannelKind(row.Kind)
	if in.Kind != "" && in.Kind != kind {
		return Channel{}, invalidf("kind: a channel keeps its kind; add a new one")
	}
	name, cfg, err := check(kind, in)
	if err != nil {
		return Channel{}, err
	}
	sealed := row.Secret
	token := strings.TrimSpace(in.Token)
	switch {
	case in.ClearToken:
		if sealed, err = c.seal(channelSecret{}); err != nil {
			return Channel{}, err
		}
	case token != "":
		if sealed, err = c.seal(channelSecret{Token: token}); err != nil {
			return Channel{}, err
		}
	}
	enabled := row.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	raw, _ := json.Marshal(cfg)
	row, err = c.st.Queries().UpdateNotificationChannel(ctx, store.UpdateNotificationChannelParams{
		Name: name, Config: string(raw), Secret: sealed, Enabled: enabled, ID: id,
	})
	if isUnique(err) {
		return Channel{}, ErrChannelExists
	}
	if err != nil {
		return Channel{}, err
	}
	return view(row, c.hasToken(row)), nil
}

// Delete removes a channel.
func (c *Channels) Delete(ctx context.Context, id int64) error {
	n, err := c.st.Queries().DeleteNotificationChannel(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrChannelNotFound
	}
	return nil
}

// sender builds row's sender. Credentials that do not open give a sender
// that fails with that reason.
func (c *Channels) sender(row store.NotificationChannel) Sender {
	sec, err := c.open(row)
	if err != nil {
		return failing{err}
	}
	var cfg channelConfig
	_ = json.Unmarshal([]byte(row.Config), &cfg)
	switch ChannelKind(row.Kind) {
	case KindNtfy:
		return Ntfy{Server: cfg.Server, Topic: cfg.Topic, Token: sec.Token}
	case KindWebhook:
		w := Webhook{URL: cfg.URL, Token: sec.Token}
		if cfg.CertSHA256 != "" {
			w.HTTP = PinnedHTTP(cfg.CertSHA256)
		}
		return w
	}
	return failing{fmt.Errorf("unknown channel kind %q", row.Kind)}
}

// Targets returns the enabled channels, by name.
func (c *Channels) Targets(ctx context.Context) ([]Target, error) {
	rows, err := c.st.Queries().ListNotificationChannels(ctx)
	if err != nil {
		return nil, err
	}
	var out []Target
	for _, r := range rows {
		if r.Enabled {
			out = append(out, Target{Name: r.Name, Sender: c.sender(r)})
		}
	}
	return out, nil
}

// Test sends a test notification through one channel, enabled or not.
func (c *Channels) Test(ctx context.Context, id int64, linkBase string) (ChannelTest, error) {
	row, err := c.get(ctx, id)
	if err != nil {
		return ChannelTest{}, err
	}
	p := Payload{Title: "Test", Message: "A test from the homelab.", Severity: Info, SentAt: c.now().Unix()}
	if linkBase != "" {
		p.URL = strings.TrimRight(linkBase, "/") + "/notifications"
	}
	if err := c.sender(row).Send(ctx, p); err != nil {
		return ChannelTest{Error: err.Error()}, nil
	}
	return ChannelTest{OK: true}, nil
}
