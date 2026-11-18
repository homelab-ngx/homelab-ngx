package cloudflare_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"homelab-ngx.com/core/internal/cloudflare"
)

// Trimmed from this host's real /metrics output on 2026-09-05.
const sampleMetrics = `
# HELP cloudflared_tunnel_total_requests Amount of requests proxied through all the tunnels
# TYPE cloudflared_tunnel_total_requests counter
cloudflared_tunnel_total_requests 1771
# HELP cloudflared_tunnel_ha_connections Number of active ha connections
# TYPE cloudflared_tunnel_ha_connections gauge
cloudflared_tunnel_ha_connections 4
# HELP cloudflared_tunnel_request_errors Count of error proxying to origin
# TYPE cloudflared_tunnel_request_errors counter
cloudflared_tunnel_request_errors 3
# HELP cloudflared_tunnel_concurrent_requests_per_tunnel Concurrent requests proxied through each tunnel
# TYPE cloudflared_tunnel_concurrent_requests_per_tunnel gauge
cloudflared_tunnel_concurrent_requests_per_tunnel 2
# HELP cloudflared_tunnel_server_locations Where each tunnel is connected to.
# TYPE cloudflared_tunnel_server_locations gauge
cloudflared_tunnel_server_locations{connection_id="0",edge_location="arn04"} 1
cloudflared_tunnel_server_locations{connection_id="1",edge_location="arn06"} 1
cloudflared_tunnel_server_locations{connection_id="2",edge_location="arn06"} 1
cloudflared_tunnel_server_locations{connection_id="3",edge_location="arn02"} 0
# HELP cloudflared_tunnel_response_by_code Count of responses by HTTP status code
# TYPE cloudflared_tunnel_response_by_code counter
cloudflared_tunnel_response_by_code{status_code="200"} 1700
cloudflared_tunnel_response_by_code{status_code="404"} 71
# HELP quic_client_sent_bytes Bytes sent
# TYPE quic_client_sent_bytes gauge
quic_client_sent_bytes{conn_index="0"} 7.3011349e+07
quic_client_sent_bytes{conn_index="1"} 9.4100563e+07
`

func metricsServer(t *testing.T, metrics, ready string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/metrics":
			w.Header().Set("Content-Type", "text/plain; version=0.0.4")
			_, _ = w.Write([]byte(metrics))
		case "/ready":
			if ready == "" {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(ready))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestFetchTunnelStatsParsesRealCloudflaredOutput(t *testing.T) {
	srv := metricsServer(t, sampleMetrics, `{"status":200,"readyConnections":4,"connectorId":"0f602283-c71d"}`)
	defer srv.Close()

	got, err := cloudflare.FetchTunnelStats(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("FetchTunnelStats: %v", err)
	}
	if got.TotalRequests != 1771 || got.RequestErrors != 3 || got.ConcurrentRequests != 2 {
		t.Errorf("counters = %+v", got)
	}
	if !got.Ready || got.ReadyConnections != 4 || got.ConnectorID != "0f602283-c71d" {
		t.Errorf("ready info = %+v", got)
	}
	// 73011349 + 94100563. Prometheus renders these in scientific notation, so
	// a naive integer parse silently yields zero.
	if got.BytesSent != 167111912 {
		t.Errorf("BytesSent = %d, want 167111912", got.BytesSent)
	}
	if got.ResponsesByCode["200"] != 1700 || got.ResponsesByCode["404"] != 71 {
		t.Errorf("responses = %+v", got.ResponsesByCode)
	}
}

// server_locations carries one series per connection with 0 for locations the
// tunnel has LEFT. Reporting those would name PoPs it is no longer using.
func TestEdgeLocationsIgnoresStaleSeriesAndDeduplicates(t *testing.T) {
	srv := metricsServer(t, sampleMetrics, `{"status":200,"readyConnections":4,"connectorId":"x"}`)
	defer srv.Close()

	got, err := cloudflare.FetchTunnelStats(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("FetchTunnelStats: %v", err)
	}
	joined := strings.Join(got.EdgeLocations, ",")
	if strings.Contains(joined, "arn02") {
		t.Errorf("reported a location the tunnel has left: %v", got.EdgeLocations)
	}
	if joined != "arn04,arn06" {
		t.Errorf("EdgeLocations = %v, want sorted unique [arn04 arn06]", got.EdgeLocations)
	}
}

// A tunnel that is down must not read as a tunnel with no traffic.
func TestFetchTunnelStatsReportsAnUnreadyTunnel(t *testing.T) {
	srv := metricsServer(t, sampleMetrics, "")
	defer srv.Close()

	got, err := cloudflare.FetchTunnelStats(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("a failing /ready must not fail the whole fetch: %v", err)
	}
	if got.Ready {
		t.Error("Ready is true despite /ready returning 503")
	}
	if got.TotalRequests != 1771 {
		t.Error("metrics should still be parsed when only /ready is down")
	}
}

func TestFetchTunnelStatsErrorsWhenMetricsAreUnreachable(t *testing.T) {
	srv := metricsServer(t, sampleMetrics, "{}")
	client := srv.Client()
	url := srv.URL
	srv.Close() // nothing listening
	if _, err := cloudflare.FetchTunnelStats(context.Background(), client, url); err == nil {
		t.Fatal("unreachable metrics endpoint reported success")
	}
}

func TestFetchTunnelStatsRejectsGarbage(t *testing.T) {
	srv := metricsServer(t, "this is not prometheus text\x00\x01", `{"status":200}`)
	defer srv.Close()
	if _, err := cloudflare.FetchTunnelStats(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("garbage metrics parsed without error")
	}
}
