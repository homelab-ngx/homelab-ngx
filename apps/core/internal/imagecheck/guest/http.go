package guest

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// DaemonSocket is the daemon's API socket in the guest.
const DaemonSocket = "/run/homelab-daemon/daemon.sock"

// Response is an HTTP answer, read whole.
type Response struct {
	Status int
	Body   []byte
}

// Problem is the code of a problem answer (the daemon's errors), or "".
func (r Response) Problem() string {
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(r.Body, &p)
	return p.Code
}

func (r Response) String() string {
	b := strings.TrimSpace(string(r.Body))
	if len(b) > 600 {
		b = b[:600] + "..."
	}
	return fmt.Sprintf("HTTP %d: %s", r.Status, b)
}

// API sends a request to the daemon's /api/PATH over its socket. A body that
// is not nil goes as JSON (a string or []byte as it is). The error is for a
// request that got no answer; any status is an answer.
func (g *Guest) API(ctx context.Context, method, path string, body any, header ...string) (Response, error) {
	t := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return g.Dial(ctx, "unix", DaemonSocket)
		},
		DisableKeepAlives: true,
	}
	c := &http.Client{Transport: t, Timeout: g.Timeout}
	return do(ctx, c, method, "http://daemon/api/"+strings.TrimPrefix(path, "/"), body, header)
}

// WebSocket opens a WebSocket to the daemon's /api/PATH over its socket.
// The response is the refusal when the upgrade was refused.
func (g *Guest) WebSocket(ctx context.Context, path string) (*websocket.Conn, *http.Response, error) {
	t := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return g.Dial(ctx, "unix", DaemonSocket)
		},
	}
	return websocket.Dial(ctx, "ws://daemon/api/"+strings.TrimPrefix(path, "/"), &websocket.DialOptions{HTTPClient: &http.Client{Transport: t}})
}

func do(ctx context.Context, c *http.Client, method, url string, body any, header []string) (Response, error) {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		j, err := json.Marshal(b)
		if err != nil {
			return Response{}, err
		}
		rd = bytes.NewReader(j)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %s %s: %v", ErrTransport, method, url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %s %s: %v", ErrTransport, method, url, err)
	}
	return Response{Status: resp.StatusCode, Body: b}, nil
}

// Web is a browser of the guest's own HTTPS (Caddy on 127.0.0.1:443), with
// cookies, for names under its domain. The certificate is not checked.
type Web struct {
	g      *Guest
	client *http.Client
}

// Web is a new browser with no cookies.
func (g *Guest) Web() *Web {
	jar, _ := cookiejar.New(nil)
	t := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return g.Dial(ctx, "tcp", "127.0.0.1:443")
		},
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives: true,
	}
	return &Web{g: g, client: &http.Client{
		Transport: t, Jar: jar, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Do sends a request to https://HOST/PATH.
func (w *Web) Do(ctx context.Context, method, host, path string, body any, header ...string) (Response, error) {
	return do(ctx, w.client, method, "https://"+host+path, body, header)
}

// Get is a GET of https://HOST/PATH.
func (w *Web) Get(ctx context.Context, host, path string) (Response, error) {
	return w.Do(ctx, http.MethodGet, host, path, nil)
}
