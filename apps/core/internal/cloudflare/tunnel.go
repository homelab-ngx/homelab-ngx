package cloudflare

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"strconv"
)

const maxMetricsBody = 4 << 20

// TunnelStats is what cloudflared reports about itself locally. No Cloudflare
// credential is involved: this is the tunnel describing its own behaviour.
//
// Deliberately tunnel-wide, because that is all cloudflared offers — its
// metrics carry no hostname label anywhere (verified 2026-09-05: the only
// labels in its entire output are status_code, connection_id, edge_location,
// conn_index and RPC internals). Per-hostname traffic exists only at the edge.
type TunnelStats struct {
	Ready              bool             `json:"ready"`
	ConnectorID        string           `json:"connector_id,omitempty"`
	ReadyConnections   int              `json:"ready_connections"`
	TotalRequests      int64            `json:"total_requests"`
	RequestErrors      int64            `json:"request_errors"`
	ConcurrentRequests int64            `json:"concurrent_requests"`
	BytesSent          int64            `json:"bytes_sent"`
	EdgeLocations      []string         `json:"edge_locations"`
	ResponsesByCode    map[string]int64 `json:"responses_by_code"`
}

// sample is one parsed Prometheus line: a metric name, its labels, its value.
type sample struct {
	labels map[string]string
	value  float64
}

// parseMetrics reads the Prometheus text exposition subset cloudflared emits:
// comment lines, then `name` or `name{k="v",...}` followed by a float.
//
// Hand-rolled rather than using prometheus/common/expfmt deliberately. That
// package's TextParser panics with "Invalid name validation scheme requested:
// unset" unless a PACKAGE-LEVEL global is set first, and setting process-wide
// global state in a shared library to parse one loopback endpoint would also
// reach the daemon's own promhttp metrics. Using it would additionally have
// forced a prometheus/common upgrade across the whole daemon.
//
// Values are parsed as float64, never as integers: Prometheus renders large
// numbers in scientific notation (quic_client_sent_bytes reports 7.3011349e+07)
// and an integer parse of that silently yields zero rather than failing.
func parseMetrics(r io.Reader) (map[string][]sample, error) {
	out := map[string][]sample{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lines := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines++
		name, rest, ok := cutMetricName(line)
		if !ok {
			return nil, fmt.Errorf("unparseable metric line %q", truncate(line))
		}
		labels := map[string]string{}
		if strings.HasPrefix(rest, "{") {
			end := strings.Index(rest, "}")
			if end < 0 {
				return nil, fmt.Errorf("unterminated label set in %q", truncate(line))
			}
			for _, pair := range splitLabels(rest[1:end]) {
				k, v, found := strings.Cut(pair, "=")
				if !found {
					continue
				}
				labels[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
			}
			rest = rest[end+1:]
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return nil, fmt.Errorf("metric %q has no value", name)
		}
		v, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			return nil, fmt.Errorf("metric %q has a non-numeric value %q", name, fields[0])
		}
		out[name] = append(out[name], sample{labels: labels, value: v})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if lines == 0 {
		return nil, fmt.Errorf("no metric samples found")
	}
	return out, nil
}

// cutMetricName splits a leading metric name off a line. A valid name is
// [a-zA-Z_:][a-zA-Z0-9_:]* — anything else means this is not exposition text.
func cutMetricName(line string) (string, string, bool) {
	i := 0
	for i < len(line) {
		c := line[i]
		valid := c == '_' || c == ':' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(i > 0 && c >= '0' && c <= '9')
		if !valid {
			break
		}
		i++
	}
	if i == 0 {
		return "", "", false
	}
	return line[:i], line[i:], true
}

// splitLabels splits on commas that are not inside a quoted value.
func splitLabels(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' && (i == 0 || s[i-1] != '\\'):
			inQuote = !inQuote
			cur.WriteByte(c)
		case c == ',' && !inQuote:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func truncate(s string) string {
	if len(s) > 60 {
		return s[:60] + "..."
	}
	return s
}

// FetchTunnelStats reads /metrics and /ready.
//
// A failing /ready degrades to Ready=false rather than failing the call: a
// tunnel that is down and a tunnel with no traffic must not look alike, and the
// metrics remain informative either way.
func FetchTunnelStats(ctx context.Context, client *http.Client, baseURL string) (TunnelStats, error) {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	base := strings.TrimRight(baseURL, "/")
	out := TunnelStats{ResponsesByCode: map[string]int64{}, EdgeLocations: []string{}}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/metrics", nil)
	if err != nil {
		return out, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return out, fmt.Errorf("read cloudflared metrics: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("cloudflared metrics returned HTTP %d", resp.StatusCode)
	}

	families, err := parseMetrics(io.LimitReader(resp.Body, maxMetricsBody))
	if err != nil {
		return out, fmt.Errorf("parse cloudflared metrics: %w", err)
	}

	first := func(name string) float64 {
		if fam, ok := families[name]; ok && len(fam) > 0 {
			return fam[0].value
		}
		return 0
	}
	out.TotalRequests = int64(first("cloudflared_tunnel_total_requests"))
	out.RequestErrors = int64(first("cloudflared_tunnel_request_errors"))
	out.ConcurrentRequests = int64(first("cloudflared_tunnel_concurrent_requests_per_tunnel"))

	for _, m := range families["quic_client_sent_bytes"] {
		out.BytesSent += int64(m.value)
	}
	for _, m := range families["cloudflared_tunnel_response_by_code"] {
		if code := m.labels["status_code"]; code != "" {
			out.ResponsesByCode[code] += int64(m.value)
		}
	}

	// server_locations emits one series per connection, with 0 for locations
	// the tunnel has LEFT. Reporting those would name PoPs it no longer uses.
	seen := map[string]bool{}
	for _, m := range families["cloudflared_tunnel_server_locations"] {
		if m.value == 0 {
			continue
		}
		if loc := m.labels["edge_location"]; loc != "" {
			seen[loc] = true
		}
	}
	for loc := range seen {
		out.EdgeLocations = append(out.EdgeLocations, loc)
	}
	sort.Strings(out.EdgeLocations)

	out.Ready, out.ReadyConnections, out.ConnectorID = fetchReady(ctx, client, base)
	return out, nil
}

// fetchReady is best-effort by design; see FetchTunnelStats' doc comment.
func fetchReady(ctx context.Context, client *http.Client, base string) (bool, int, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/ready", nil)
	if err != nil {
		return false, 0, ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, 0, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, 0, ""
	}
	var ready struct {
		Status           int    `json:"status"`
		ReadyConnections int    `json:"readyConnections"`
		ConnectorID      string `json:"connectorId"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&ready) != nil {
		return false, 0, ""
	}
	return ready.Status == http.StatusOK, ready.ReadyConnections, ready.ConnectorID
}
