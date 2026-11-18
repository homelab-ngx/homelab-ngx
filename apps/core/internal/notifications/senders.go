package notifications

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Payload is one notification as a channel gets it. As JSON it is the
// webhook channel's body, the contract any receiver implements.
type Payload struct {
	// Title and Message: one of them is non-empty.
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Severity Severity `json:"severity"`
	// Key is a stable problem key; a receiver may replace an earlier
	// notification with the same key. Omitted when there is none.
	Key string `json:"key,omitempty"`
	// URL is the dashboard page the notification is about.
	URL string `json:"url,omitempty"`
	// SentAt is unix seconds.
	SentAt int64 `json:"sent_at"`
}

// Sender delivers one payload.
type Sender interface {
	Send(ctx context.Context, p Payload) error
}

// Target is one channel a Notifier delivers to: its name in the log and
// its sender.
type Target struct {
	Name   string
	Sender Sender
}

// Targets lists the channels a message goes to now.
type Targets interface {
	Targets(ctx context.Context) ([]Target, error)
}

const (
	// sendTimeout bounds one delivery.
	sendTimeout = 10 * time.Second
	// maxAnswer bounds what is read of an answer.
	maxAnswer = 64 << 10
)

// Webhook POSTs the payload as JSON to URL.
type Webhook struct {
	URL string
	// Token is sent as a bearer token; "" sends none.
	Token string
	// HTTP is the client; nil means a plain one (see PinnedHTTP).
	HTTP *http.Client
}

// Send posts p.
func (w Webhook) Send(ctx context.Context, p Payload) error {
	headers := map[string]string{}
	if w.Token != "" {
		headers["Authorization"] = "Bearer " + w.Token
	}
	return post(ctx, w.HTTP, w.URL, headers, p)
}

// Ntfy publishes to a topic on an ntfy server (JSON publishing).
type Ntfy struct {
	// Server is the server's origin, e.g. https://ntfy.sh.
	Server string
	Topic  string
	// Token is an access token; "" publishes anonymously.
	Token string
	HTTP  *http.Client
}

// Send publishes p: an alert at priority 4 with a warning tag, info at the
// default 3, the dashboard link as the click action. ntfy needs a message,
// so a title alone goes out as the message.
func (n Ntfy) Send(ctx context.Context, p Payload) error {
	body := map[string]any{"topic": n.Topic, "title": p.Title, "message": p.Message, "priority": 3}
	if strings.TrimSpace(p.Message) == "" {
		body["message"] = p.Title
		delete(body, "title")
	}
	if p.Severity == Alert {
		body["priority"] = 4
		body["tags"] = []string{"warning"}
	}
	if p.URL != "" {
		body["click"] = p.URL
	}
	headers := map[string]string{}
	if n.Token != "" {
		headers["Authorization"] = "Bearer " + n.Token
	}
	return post(ctx, n.HTTP, strings.TrimRight(n.Server, "/")+"/", headers, body)
}

// post sends body as JSON and wants a 2xx answer. A refusal carries the
// answer's "error" field when it has one.
func post(ctx context.Context, hc *http.Client, url string, headers map[string]string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "homelab")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if hc == nil {
		hc = plainHTTP
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return nil
	}
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		return fmt.Errorf("answered %d: %s", resp.StatusCode, e.Error)
	}
	return fmt.Errorf("answered %d", resp.StatusCode)
}

// plainHTTP follows no redirects: a channel is one address, and a redirect
// would carry the token somewhere else.
var plainHTTP = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// PinnedHTTP is a client that accepts only a TLS server whose leaf
// certificate's DER encoding hashes (SHA-256) to pin, lowercase hex. The
// chain and host name are not checked: the pin replaces them, which is what
// makes a self-signed certificate usable. A malformed pin matches nothing.
func PinnedHTTP(pin string) *http.Client {
	want, _ := hex.DecodeString(pin)
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.TLSClientConfig = &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // replaced by VerifyPeerCertificate below
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("the server sent no certificate")
			}
			got := sha256.Sum256(rawCerts[0])
			if len(want) != sha256.Size || subtle.ConstantTimeCompare(got[:], want) != 1 {
				return errors.New("the server's certificate does not match the pin")
			}
			return nil
		},
	}
	return &http.Client{Transport: t, CheckRedirect: plainHTTP.CheckRedirect}
}

// failing is a target whose sender could not be built (its credentials do
// not open): every delivery fails with why, so the page shows it.
type failing struct{ why error }

func (f failing) Send(context.Context, Payload) error { return f.why }
