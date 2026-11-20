package hostcfg

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/libdns/libdns"
)

// fakeZone is a DNS provider holding the zones it is given.
type fakeZone struct {
	mu    sync.Mutex
	zones map[string]bool
	set   []string // "<zone> <name> <ip>"
	fail  error
}

func (z *fakeZone) GetRecords(_ context.Context, zone string) ([]libdns.Record, error) {
	if !z.zones[zone] {
		return nil, errors.New("zone not found")
	}
	return nil, nil
}

func (z *fakeZone) SetRecords(_ context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	if z.fail != nil {
		return nil, z.fail
	}
	for _, r := range recs {
		a := r.(libdns.Address)
		z.set = append(z.set, zone+" "+a.Name+" "+a.IP.String())
	}
	return recs, nil
}

// The record is written when the address changes, not on every check, in
// the zone that holds the domain; a failure is kept with the last address.
func TestDDNS(t *testing.T) {
	s, _, _ := newService(t)
	ctx := context.Background()
	z := &fakeZone{zones: map[string]bool{"example.com.": true}}
	ip := "203.0.113.7"
	s.ddns.newZone = func(Snapshot) (dnsZone, error) { return z, nil }
	s.ddns.publicIP = func(_ context.Context, url string) (netip.Addr, error) {
		if url == trace6URL {
			return netip.Addr{}, errors.New("no IPv6")
		}
		return netip.MustParseAddr(ip), nil
	}
	if _, err := s.SetDDNS(ctx, DDNSSet{Enabled: true}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("dynamic DNS on without a DNS provider: %v", err)
	}
	if _, err := s.SetDomain(ctx, DomainSet{Domain: "home.example.com", DNSProvider: DNSCloudflare, DNSCredentials: map[string]string{"api_token": "tok"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDDNS(ctx, DDNSSet{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	s.updateDDNS(ctx)
	s.updateDDNS(ctx)
	if len(z.set) != 1 || z.set[0] != "example.com. vpn.home 203.0.113.7" {
		t.Fatalf("set = %v", z.set)
	}
	got, _ := s.Get(ctx)
	if st := got.DDNS.Status; st == nil || st.Record != "vpn.home.example.com" || st.IPs[0] != ip || st.At == nil || st.Error != "" {
		t.Fatalf("status = %+v", st)
	}
	if e := Endpoint(got); e != "vpn.home.example.com:51820" {
		t.Errorf("endpoint = %q", e)
	}
	ip = "203.0.113.8"
	z.fail = errors.New("rate limited")
	s.updateDDNS(ctx)
	got, _ = s.Get(ctx)
	if st := got.DDNS.Status; !strings.Contains(st.Error, "rate limited") || st.IPs[0] != "203.0.113.7" {
		t.Errorf("status after a failure = %+v", st)
	}
	z.fail = nil
	s.updateDDNS(ctx)
	if len(z.set) != 2 || z.set[1] != "example.com. vpn.home 203.0.113.8" {
		t.Errorf("set = %v", z.set)
	}
	// Off: no status.
	if got, _ := s.SetDDNS(ctx, DDNSSet{}); got.DDNS.Enabled {
		t.Fatal("still on")
	}
	s.updateDDNS(ctx)
	if got, _ := s.Get(ctx); got.DDNS.Status != nil {
		t.Errorf("status while off = %+v", got.DDNS.Status)
	}
}

// Each provider is built from the domain's stored credentials.
func TestNewDNSZone(t *testing.T) {
	t.Parallel()
	for _, p := range []DNSProvider{DNSCloudflare, DNSPorkbun, DNSDuckDNS, DNSRoute53} {
		var s Snapshot
		s.Domain.DNSProvider = p
		s.DNSCredentials = map[string]string{"api_token": "t", "api_key": "k", "api_secret_key": "s", "access_key_id": "a", "secret_access_key": "s"}
		if z, err := newDNSZone(s); err != nil || z == nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if _, err := newDNSZone(Snapshot{}); err == nil {
		t.Error("a zone without a provider")
	}
	var s Snapshot
	s.Domain = Domain{Domain: "myhome.duckdns.org", DNSProvider: DNSDuckDNS}
	s.DDNS.Name = "vpn"
	if n := DDNSName(s.HostSettings); n != "myhome.duckdns.org" {
		t.Errorf("DuckDNS record = %q", n)
	}
}
