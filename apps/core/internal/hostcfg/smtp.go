package hostcfg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/danielgtaylor/huma/v2"
)

const (
	smtpUnit = "homelab-smtpd.service"
	smtpDir  = "/var/lib/homelab/smtp"
)

// SMTPTLS is the transport security between the host relay and the upstream
// SMTP server.
type SMTPTLS string

const (
	SMTPTLSAuto     SMTPTLS = ""
	SMTPTLSStartTLS SMTPTLS = "starttls"
	SMTPTLSImplicit SMTPTLS = "tls"
	SMTPTLSNone     SMTPTLS = "none"
)

// Schema documents SMTPTLS as its enum.
func (SMTPTLS) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Enum: []any{"", "starttls", "tls", "none"}}
}

// SMTP is the host's outbound mail relay for stacks on port 25.
type SMTP struct {
	Enabled     bool    `json:"enabled"`
	Host        string  `required:"false" json:"host" doc:"Upstream SMTP server host name or IP address."`
	Port        int     `required:"false" json:"port" doc:"Upstream TCP port; 0 is 587 (or 465 for implicit TLS)."`
	TLS         SMTPTLS `required:"false" json:"tls" doc:"Transport security to the upstream: starttls (default, port 587), tls (implicit TLS, port 465), or none (plaintext, no authentication)."`
	Username    string  `required:"false" json:"username" doc:"SMTP authentication user name; leave empty for an unauthenticated upstream."`
	PasswordSet bool    `json:"password_set" doc:"An upstream SMTP password is stored; it is never shown."`
}

// SMTPSet changes the host SMTP relay section.
type SMTPSet struct {
	Enabled  bool    `json:"enabled"`
	Host     string  `json:"host,omitempty" maxLength:"253"`
	Port     int     `json:"port,omitempty" minimum:"0" maximum:"65535"`
	TLS      SMTPTLS `json:"tls,omitempty"`
	Username string  `json:"username,omitempty" maxLength:"254"`
	// Password: absent keeps the stored one (dropped when username is cleared); empty clears it.
	Password *string `json:"password,omitempty" maxLength:"1024" doc:"Upstream SMTP password. Absent keeps the stored one; empty clears it."`
}

type smtpSecret struct {
	Password string `json:"password,omitempty"`
}

var smtpHostRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

func validSMTPHost(host string) bool {
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	return !strings.Contains(host, "..") && smtpHostRE.MatchString(host)
}

func hasControlOrNewline(s string) bool {
	return strings.ContainsAny(s, "\r\n\x00")
}

// SetSMTP stores and applies the host SMTP relay settings.
func (s *Service) SetSMTP(ctx context.Context, in SMTPSet) (HostSettings, error) {
	in.Host = strings.TrimSpace(in.Host)
	in.Username = strings.TrimSpace(in.Username)
	if hasControlOrNewline(in.Host) || hasControlOrNewline(in.Username) || (in.Password != nil && hasControlOrNewline(*in.Password)) {
		return HostSettings{}, invalid("smtp: control characters are not allowed")
	}
	switch in.TLS {
	case SMTPTLSAuto, SMTPTLSStartTLS, SMTPTLSImplicit, SMTPTLSNone:
	default:
		return HostSettings{}, invalid("smtp tls: starttls, tls, none or empty")
	}
	if in.Port < 0 || in.Port > 65535 {
		return HostSettings{}, invalid("smtp port: 0 to 65535")
	}
	if in.Host != "" && !validSMTPHost(in.Host) {
		return HostSettings{}, invalid("smtp host: %q is not a host name or an address", in.Host)
	}
	if in.Username != "" && (strings.Contains(in.Username, ":") || strings.ContainsFunc(in.Username, unicode.IsSpace)) {
		return HostSettings{}, invalid("smtp username: spaces and colons are not allowed")
	}

	tlsMode := in.TLS
	if tlsMode == SMTPTLSAuto {
		if in.Port == 465 {
			tlsMode = SMTPTLSImplicit
		} else {
			tlsMode = SMTPTLSStartTLS
		}
	}
	port := in.Port
	if port == 0 {
		if tlsMode == SMTPTLSImplicit {
			port = 465
		} else {
			port = 587
		}
	}

	return s.change(ctx, func(rs map[string]row) error {
		var cur smtpSecret
		if err := s.open(rs["smtp"], &cur); err != nil {
			return err
		}
		pass := cur.Password
		if in.Password != nil {
			pass = *in.Password
		}
		if in.Username == "" {
			pass = ""
		}
		if in.Enabled {
			if in.Host == "" {
				return invalid("smtp host: required when enabled")
			}
			if in.Username != "" {
				if tlsMode == SMTPTLSNone {
					return invalid("smtp: authentication requires TLS or STARTTLS")
				}
				if pass == "" {
					return invalid("smtp password: required when a username is set")
				}
			}
		}
		var sec any
		if pass != "" {
			sec = smtpSecret{Password: pass}
		}
		val := SMTP{
			Enabled:  in.Enabled,
			Host:     in.Host,
			Port:     port,
			TLS:      tlsMode,
			Username: in.Username,
		}
		return s.save(ctx, "smtp", val, sec)
	})
}

// SMTPConfig renders OpenSMTPD's smtpd.conf and optional secrets table for s.
// Empty when the relay is disabled or has no upstream host.
func SMTPConfig(s Snapshot) (conf string, secrets string) {
	if !s.SMTP.Enabled || s.SMTP.Host == "" {
		return "", ""
	}
	tlsMode := s.SMTP.TLS
	if tlsMode == SMTPTLSAuto {
		if s.SMTP.Port == 465 {
			tlsMode = SMTPTLSImplicit
		} else {
			tlsMode = SMTPTLSStartTLS
		}
	}
	port := s.SMTP.Port
	if port == 0 {
		if tlsMode == SMTPTLSImplicit {
			port = 465
		} else {
			port = 587
		}
	}
	scheme := "smtp+tls"
	switch tlsMode {
	case SMTPTLSImplicit:
		scheme = "smtps"
	case SMTPTLSNone:
		scheme = "smtp"
	}
	target := net.JoinHostPort(s.SMTP.Host, strconv.Itoa(port))

	var b strings.Builder
	b.WriteString("# Written by homelab-daemon from the host settings; changes here are lost.\n")
	if s.SMTP.Username != "" && s.SMTPPassword != "" {
		secrets = fmt.Sprintf("relay %s:%s\n", s.SMTP.Username, s.SMTPPassword)
		fmt.Fprintf(&b, "table secrets file:%s/secrets\n\n", smtpDir)
	}
	b.WriteString("listen on 0.0.0.0 port 25\n\n")
	if secrets != "" {
		fmt.Fprintf(&b, "action \"relay\" relay host %s://relay@%s auth <secrets>\n\n", scheme, target)
	} else {
		fmt.Fprintf(&b, "action \"relay\" relay host %s://%s\n\n", scheme, target)
	}
	// Accept mail only from the host itself and stack subnets (10.99.0.0/16).
	b.WriteString("match from local for any action \"relay\"\n")
	b.WriteString("match from src 127.0.0.0/8 for any action \"relay\"\n")
	b.WriteString("match from src 10.99.0.0/16 for any action \"relay\"\n")
	return b.String(), secrets
}

func (a *ImageApplier) smtp(ctx context.Context, s Snapshot) error {
	dir := a.path(smtpDir)
	confPath := filepath.Join(dir, "smtpd.conf")
	secretsPath := filepath.Join(dir, "secrets")
	conf, secrets := SMTPConfig(s)
	if conf == "" {
		_, err1 := remove(confPath)
		_, err2 := remove(secretsPath)
		_ = a.run(ctx, "systemctl", "disable", "--runtime", "--now", smtpUnit)
		return errors.Join(err1, err2)
	}
	var schanged bool
	var err error
	if secrets != "" {
		schanged, err = a.write(secretsPath, secrets, 0o640, "smtpd")
	} else {
		schanged, err = remove(secretsPath)
	}
	if err != nil {
		return err
	}
	cchanged, err := a.write(confPath, conf, 0o644, "")
	if err != nil {
		return err
	}
	if err := a.run(ctx, "systemctl", "enable", "--runtime", smtpUnit); err != nil {
		return err
	}
	action := "start"
	if cchanged || schanged {
		action = "restart"
	}
	return a.run(ctx, "systemctl", action, smtpUnit)
}
