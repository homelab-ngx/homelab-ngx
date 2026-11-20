package hostcfg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/libdns/cloudflare"
	"github.com/libdns/duckdns"
	"github.com/libdns/libdns"
	"github.com/libdns/porkbun"
	"github.com/libdns/route53"
)

// ── dynamic DNS ──────────────────────────────────────────────────────────
//
// A name under the domain kept pointed at the home's public address, through
// the domain's own DNS provider and credentials, so devices away from home
// reach the VPN at a name that follows the address.

const (
	// ddnsEvery is how often the public address is checked.
	ddnsEvery = 5 * time.Minute
	// ddnsTTL is the record's TTL: a changed address is seen within it.
	ddnsTTL = 5 * time.Minute
	// defaultDDNSName is the label under the domain when none is set.
	defaultDDNSName = "vpn"
	// trace6URL is traceURL on IPv6.
	trace6URL = "https://[2606:4700:4700::1111]/cdn-cgi/trace"
)

// DDNS keeps a name pointed at the home's public address.
type DDNS struct {
	Enabled bool        `json:"enabled"`
	Name    string      `required:"false" json:"name" doc:"The label under the domain, such as vpn (the default). With DuckDNS the record is the DuckDNS name itself, and this is not used."`
	Status  *DDNSStatus `json:"status,omitempty" readOnly:"true" doc:"The last check, while dynamic DNS is on. Kept in memory: empty until the first check after the daemon starts."`
}

// DDNSSet changes the dynamic DNS section.
type DDNSSet struct {
	Enabled bool   `json:"enabled"`
	Name    string `json:"name,omitempty" maxLength:"63"`
}

// DDNSStatus is the last check of the public address.
type DDNSStatus struct {
	Record string     `json:"record" doc:"The name kept up to date, such as vpn.home.example.com."`
	IPs    []string   `json:"ips" doc:"The public addresses it points at: IPv4, and IPv6 when the host has one."`
	At     *time.Time `json:"at,omitempty" doc:"When the record was last found or made up to date."`
	Error  string     `json:"error,omitempty" doc:"Why the last check or update failed."`
}

// DDNSName is the record dynamic DNS keeps, or "" without a domain.
func DDNSName(st HostSettings) string {
	d := st.Domain.Domain
	if d == "" {
		return ""
	}
	if st.Domain.DNSProvider == DNSDuckDNS {
		return d
	}
	n := st.DDNS.Name
	if n == "" {
		n = defaultDDNSName
	}
	return n + "." + d
}

// dnsZone reads and writes a zone's records (a libdns provider).
type dnsZone interface {
	libdns.RecordGetter
	libdns.RecordSetter
}

// newDNSZone is the provider for the domain's DNS, from its credentials.
func newDNSZone(s Snapshot) (dnsZone, error) {
	c := s.DNSCredentials
	switch s.Domain.DNSProvider {
	case DNSCloudflare:
		return &cloudflare.Provider{APIToken: c["api_token"]}, nil
	case DNSPorkbun:
		return &porkbun.Provider{APIKey: c["api_key"], APISecretKey: c["api_secret_key"]}, nil
	case DNSDuckDNS:
		return &duckdns.Provider{APIToken: c["api_token"]}, nil
	case DNSRoute53:
		return &route53.Provider{AccessKeyId: c["access_key_id"], SecretAccessKey: c["secret_access_key"], Region: s.Domain.Region}, nil
	}
	return nil, fmt.Errorf("the domain has no DNS provider")
}

// ddnsLoop is the updater's memory.
type ddnsLoop struct {
	mu      sync.Mutex
	status  *DDNSStatus
	written string // "<record> <ips>" last written
	zone    string // the zone found for written's record
	kick    chan struct{}

	// Seams for tests.
	newZone  func(Snapshot) (dnsZone, error)
	publicIP func(ctx context.Context, url string) (netip.Addr, error)
}

func newDDNSLoop() *ddnsLoop {
	return &ddnsLoop{
		kick:    make(chan struct{}, 1),
		newZone: newDNSZone,
		publicIP: func(ctx context.Context, url string) (netip.Addr, error) {
			return PublicIP(ctx, http.DefaultClient, url)
		},
	}
}

// RunDDNS keeps the record up to date until ctx ends: every ddnsEvery and
// after each change of the settings.
func (s *Service) RunDDNS(ctx context.Context) {
	if s.managed != ManagedImage {
		return
	}
	t := time.NewTicker(ddnsEvery)
	defer t.Stop()
	for {
		s.updateDDNS(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.ddns.kick:
		}
	}
}

// kickDDNS has the loop check again now.
func (s *Service) kickDDNS() {
	select {
	case s.ddns.kick <- struct{}{}:
	default:
	}
}

func (s *Service) ddnsStatus() *DDNSStatus {
	s.ddns.mu.Lock()
	defer s.ddns.mu.Unlock()
	if s.ddns.status == nil {
		return nil
	}
	c := *s.ddns.status
	return &c
}

// updateDDNS checks the public address once and writes the record when it
// or the record's name changed since the last write.
func (s *Service) updateDDNS(ctx context.Context) {
	d := s.ddns
	snap, err := s.snapshot(ctx)
	if err != nil {
		s.log.Error("dynamic DNS: settings unreadable", "error", err)
		return
	}
	if !snap.DDNS.Enabled {
		d.mu.Lock()
		d.status, d.written = nil, ""
		d.mu.Unlock()
		return
	}
	record := DDNSName(snap.HostSettings)
	st := &DDNSStatus{Record: record, IPs: []string{}}
	fail := func(err error) {
		st.Error = err.Error()
		d.mu.Lock()
		if d.status != nil && d.status.Record == record {
			st.IPs, st.At = d.status.IPs, d.status.At
		}
		d.status = st
		d.mu.Unlock()
		s.log.Warn("dynamic DNS not updated", "record", record, "error", err)
	}
	v4, err := d.publicIP(ctx, traceURL)
	if err != nil {
		fail(err)
		return
	}
	ips := []netip.Addr{v4}
	if v6, err := d.publicIP(ctx, trace6URL); err == nil && v6.Is6() && v6.IsGlobalUnicast() {
		ips = append(ips, v6)
	}
	for _, ip := range ips {
		st.IPs = append(st.IPs, ip.String())
	}
	key := record + " " + strings.Join(st.IPs, " ")
	d.mu.Lock()
	same := d.written == key
	d.mu.Unlock()
	if !same {
		zone, err := s.writeDDNS(ctx, snap, record, ips)
		if err != nil {
			fail(err)
			return
		}
		d.mu.Lock()
		d.written, d.zone = key, zone
		d.mu.Unlock()
		s.log.Info("dynamic DNS updated", "record", record, "ips", st.IPs)
	}
	now := s.now()
	st.At = &now
	d.mu.Lock()
	d.status = st
	d.mu.Unlock()
}

func (s *Service) writeDDNS(ctx context.Context, snap Snapshot, record string, ips []netip.Addr) (string, error) {
	z, err := s.ddns.newZone(snap)
	if err != nil {
		return "", err
	}
	zone, err := s.findZone(ctx, z, snap)
	if err != nil {
		return "", err
	}
	name := libdns.RelativeName(record+".", zone)
	recs := make([]libdns.Record, 0, len(ips))
	for _, ip := range ips {
		recs = append(recs, libdns.Address{Name: name, TTL: ddnsTTL, IP: ip})
	}
	if _, err := z.SetRecords(ctx, zone, recs); err != nil {
		return "", fmt.Errorf("writing %s at %s: %w", record, snap.Domain.DNSProvider, err)
	}
	return zone, nil
}

// findZone is the zone at the provider that holds the domain: the domain
// itself or one of its parents, the first the provider answers for.
func (s *Service) findZone(ctx context.Context, z dnsZone, snap Snapshot) (string, error) {
	d := snap.Domain.Domain
	if snap.Domain.DNSProvider == DNSDuckDNS {
		return d + ".", nil
	}
	s.ddns.mu.Lock()
	cached := s.ddns.zone
	s.ddns.mu.Unlock()
	if cached != "" && (d+"." == cached || strings.HasSuffix(d+".", "."+cached)) {
		return cached, nil
	}
	labels := strings.Split(d, ".")
	var errs []error
	for i := 0; i < len(labels)-1; i++ {
		zone := strings.Join(labels[i:], ".") + "."
		_, err := z.GetRecords(ctx, zone)
		if err == nil {
			return zone, nil
		}
		errs = append(errs, err)
	}
	return "", fmt.Errorf("no zone for %s at %s (do the credentials reach it?): %w", d, snap.Domain.DNSProvider, errors.Join(errs...))
}

// SetDDNS changes dynamic DNS. It uses the domain's DNS provider and
// credentials, so those are set first.
func (s *Service) SetDDNS(ctx context.Context, in DDNSSet) (HostSettings, error) {
	in.Name = strings.ToLower(strings.TrimSpace(in.Name))
	if in.Name != "" && !labelRE.MatchString(in.Name) {
		return HostSettings{}, invalid("name: one label of letters, digits and -, such as vpn")
	}
	st, err := s.change(ctx, func(rs map[string]row) error {
		if in.Enabled {
			var d Domain
			if r, ok := rs["domain"]; ok {
				_ = json.Unmarshal(r.value, &d)
			}
			var ds domainSecret
			if err := s.open(rs["domain"], &ds); err != nil {
				return err
			}
			if d.Domain == "" || d.DNSProvider == DNSNone || len(ds.Credentials) == 0 {
				return invalid("dynamic DNS writes through the domain's DNS provider: set the domain, its provider and its credentials first")
			}
		}
		return s.save(ctx, "ddns", DDNS{Enabled: in.Enabled, Name: in.Name}, nil)
	})
	if err == nil {
		s.kickDDNS()
	}
	return st, err
}

// PublicAddress asks Cloudflare for the home's public IPv4 address. It is
// asked only when called: the dashboard calls it on the operator's click.
func (s *Service) PublicAddress(ctx context.Context) (PublicAddress, error) {
	ip, err := s.ddns.publicIP(ctx, traceURL)
	if err != nil {
		return PublicAddress{}, fmt.Errorf("%w: %s", ErrUpstream, err)
	}
	return PublicAddress{IP: ip.String()}, nil
}
