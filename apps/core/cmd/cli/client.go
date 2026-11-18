package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const socketPath = "/run/homelab-daemon/daemon.sock"

// daemonSocket is the unix socket the CLI talks to; tests point it at a
// temporary listener.
var daemonSocket = socketPath

// httpClient talks to the daemon over its unix socket. The "unix" host in
// request URLs is a placeholder: the transport always dials daemonSocket.
func httpClient() *http.Client {
	sock := daemonSocket
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
		},
		Timeout: 60 * time.Second,
	}
}

// call sends one request and decodes a 2xx JSON answer into O. The input and
// output types are the daemon's own handler types. Path segments taken from
// user input must be escaped with url.PathEscape by the caller; in, when not
// nil, is sent as the JSON body. Transport errors become "contacting daemon",
// non-2xx answers an *APIError.
func call[O any](ctx context.Context, method, path string, in any) (*O, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, fmt.Errorf("encoding request: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://unix/api"+path, body)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("contacting daemon: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading daemon answer: %w", err)
	}
	if err := check(resp.StatusCode, data); err != nil {
		return nil, err
	}
	out := new(O)
	if len(data) > 0 && resp.StatusCode != http.StatusNoContent {
		if err := json.Unmarshal(data, out); err != nil {
			return nil, fmt.Errorf("decoding daemon answer: %w", err)
		}
	}
	return out, nil
}

// send is call for operations whose answer has no body the caller needs.
func send(ctx context.Context, method, path string, in any) error {
	_, err := call[json.RawMessage](ctx, method, path, in)
	return err
}

// APIError is a non-2xx daemon answer, decoded from application/problem+json
// when the body is one.
type APIError struct {
	Status int
	Code   string
	Title  string
	Detail string
}

func (e *APIError) Error() string {
	msg := e.Detail
	if msg == "" {
		msg = e.Title
	}
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	out := fmt.Sprintf("daemon returned status %d: %s", e.Status, msg)
	if e.Code != "" {
		out += " [" + e.Code + "]"
	}
	return out
}

// check turns a non-2xx answer into an *APIError, keeping the daemon's
// problem detail instead of only the status number.
func check(status int, body []byte) error {
	if status >= 200 && status < 300 {
		return nil
	}
	e := &APIError{Status: status}
	var p struct{ Code, Title, Detail string }
	if json.Unmarshal(body, &p) == nil && (p.Title != "" || p.Detail != "") {
		e.Code, e.Title, e.Detail = p.Code, p.Title, p.Detail
		return e
	}
	e.Detail = strings.TrimSpace(string(body))
	if len(e.Detail) > 512 {
		e.Detail = e.Detail[:512] + "…"
	}
	return e
}

// isUnreachable reports whether err means the daemon socket could not be
// dialed (daemon down, socket missing or not permitted), as opposed to the
// daemon answering with an error.
func isUnreachable(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

func renderError(w io.Writer, asJSON bool, err error) {
	if asJSON {
		out := map[string]any{"success": false, "error": err.Error()}
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			out["status"] = apiErr.Status
			if apiErr.Code != "" {
				out["code"] = apiErr.Code
			}
			if apiErr.Detail != "" {
				out["detail"] = apiErr.Detail
			}
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	fmt.Fprintf(w, "Error: %v\n", err)
}
