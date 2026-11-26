package hostcfg

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
)

func TestSMTPSettingsSealedAndKept(t *testing.T) {
	t.Parallel()
	s, st, rec := newService(t)
	ctx := context.Background()

	got, err := s.SetSMTP(ctx, SMTPSet{
		Enabled:  true,
		Host:     " smtp.example.com ",
		Username: " apikey ",
		Password: ptr("smtp-secret-token"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.SMTP.Enabled || got.SMTP.Host != "smtp.example.com" || got.SMTP.Port != 587 || got.SMTP.TLS != SMTPTLSStartTLS || got.SMTP.Username != "apikey" || !got.SMTP.PasswordSet {
		t.Fatalf("smtp = %+v", got.SMTP)
	}
	rows, _ := st.Queries().ListHostSettings(ctx)
	for _, r := range rows {
		if strings.Contains(r.Value, "smtp-secret-token") || strings.Contains(string(r.Secret), "smtp-secret-token") {
			t.Fatal("the SMTP password is stored in the clear")
		}
	}
	if b, _ := json.Marshal(got); strings.Contains(string(b), "smtp-secret-token") {
		t.Fatal("the SMTP password is in the API answer")
	}
	if last := rec.got[len(rec.got)-1]; last.SMTPPassword != "smtp-secret-token" {
		t.Fatalf("applier did not receive password: %+v", last)
	}

	// Absent password keeps the stored secret.
	got, err = s.SetSMTP(ctx, SMTPSet{
		Enabled:  true,
		Host:     "smtp.example.com",
		Port:     465,
		TLS:      SMTPTLSImplicit,
		Username: "apikey",
	})
	if err != nil || !got.SMTP.PasswordSet || got.SMTP.Port != 465 || got.SMTP.TLS != SMTPTLSImplicit {
		t.Fatalf("update keeping password = %+v, %v", got.SMTP, err)
	}
	if last := rec.got[len(rec.got)-1]; last.SMTPPassword != "smtp-secret-token" {
		t.Fatalf("stored password lost on update: %+v", last)
	}

	// Clearing the username clears the stored password.
	got, err = s.SetSMTP(ctx, SMTPSet{
		Enabled: true,
		Host:    "127.0.0.1",
		Port:    2525,
		TLS:     SMTPTLSNone,
	})
	if err != nil || got.SMTP.PasswordSet || got.SMTP.Username != "" {
		t.Fatalf("unauthenticated update = %+v, %v", got.SMTP, err)
	}
	if last := rec.got[len(rec.got)-1]; last.SMTPPassword != "" {
		t.Fatalf("password remained after clearing username: %q", last.SMTPPassword)
	}
}

func TestSMTPValidation(t *testing.T) {
	t.Parallel()
	s, _, _ := newService(t)
	ctx := context.Background()

	for name, in := range map[string]SMTPSet{
		"missing host":     {Enabled: true},
		"bad host":         {Enabled: true, Host: "bad host name"},
		"dotdot host":      {Enabled: true, Host: "a..b"},
		"newline host":     {Enabled: true, Host: "smtp.example.com\nmatch from any"},
		"bad tls":          {Enabled: true, Host: "smtp.example.com", TLS: "ssl"},
		"bad port":         {Enabled: true, Host: "smtp.example.com", Port: 70000},
		"auth without tls": {Enabled: true, Host: "smtp.example.com", TLS: SMTPTLSNone, Username: "u", Password: ptr("p")},
		"auth no pass":     {Enabled: true, Host: "smtp.example.com", TLS: SMTPTLSStartTLS, Username: "u"},
		"user colon":       {Enabled: true, Host: "smtp.example.com", Username: "u:1", Password: ptr("p")},
		"user space":       {Enabled: true, Host: "smtp.example.com", Username: "u 1", Password: ptr("p")},
		"pass newline":     {Enabled: true, Host: "smtp.example.com", Username: "u", Password: ptr("p\nline")},
	} {
		if _, err := s.SetSMTP(ctx, in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestSMTPConfigAndApplier(t *testing.T) {
	t.Parallel()

	// Disabled or missing host produces no config.
	if c, sec := SMTPConfig(Snapshot{}); c != "" || sec != "" {
		t.Fatalf("empty snapshot = %q / %q", c, sec)
	}

	// STARTTLS with auth.
	s := Snapshot{SMTPPassword: "secret"}
	s.SMTP = SMTP{Enabled: true, Host: "smtp.example.com", Port: 587, TLS: SMTPTLSStartTLS, Username: "apikey"}
	conf, secrets := SMTPConfig(s)
	for _, want := range []string{
		"table secrets file:/var/lib/homelab/smtp/secrets",
		"listen on 0.0.0.0 port 25",
		`action "relay" relay host smtp+tls://relay@smtp.example.com:587 auth <secrets>`,
		`match from local for any action "relay"`,
		`match from src 127.0.0.0/8 for any action "relay"`,
		`match from src 10.99.0.0/16 for any action "relay"`,
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("conf missing %q:\n%s", want, conf)
		}
	}
	if secrets != "relay apikey:secret\n" {
		t.Errorf("secrets = %q", secrets)
	}

	// Implicit TLS with IPv6 host.
	s.SMTP = SMTP{Enabled: true, Host: "::1", TLS: SMTPTLSImplicit, Username: "u"}
	conf, _ = SMTPConfig(s)
	if !strings.Contains(conf, `host smtps://relay@[::1]:465 auth <secrets>`) {
		t.Errorf("ipv6 implicit tls conf:\n%s", conf)
	}

	// Plaintext unauthenticated.
	s.SMTP = SMTP{Enabled: true, Host: "127.0.0.1", Port: 2525, TLS: SMTPTLSNone}
	s.SMTPPassword = ""
	conf, secrets = SMTPConfig(s)
	if secrets != "" || strings.Contains(conf, "auth <secrets>") || !strings.Contains(conf, `host smtp://127.0.0.1:2525`) {
		t.Errorf("plaintext conf:\n%s\nsecrets:%q", conf, secrets)
	}

	// ImageApplier writes files with safe modes, restarts on change, and removes when disabled.
	old := syscall.Umask(0o027)
	defer syscall.Umask(old)
	root := t.TempDir()
	f := cmdrunnertest.NewFake()
	f.On("systemctl").Reply("")
	a := &ImageApplier{Runner: f, Root: root}
	ctx := context.Background()

	s.SMTP = SMTP{Enabled: true, Host: "smtp.example.com", Port: 587, TLS: SMTPTLSStartTLS, Username: "apikey"}
	s.SMTPPassword = "secret"
	if err := a.smtp(ctx, s); err != nil {
		t.Fatal(err)
	}
	secPath := filepath.Join(root, smtpDir, "secrets")
	confPath := filepath.Join(root, smtpDir, "smtpd.conf")
	if st, err := os.Stat(secPath); err != nil || st.Mode().Perm() != 0o640 {
		t.Fatalf("secrets mode = %v, err = %v", st, err)
	}
	if _, err := os.Stat(confPath); err != nil {
		t.Fatal(err)
	}
	if len(f.CallsMatching("systemctl", "enable", "--runtime", smtpUnit)) != 1 ||
		len(f.CallsMatching("systemctl", "restart", smtpUnit)) != 1 {
		t.Fatalf("calls after first apply = %+v", f.Calls())
	}

	// Unchanged apply uses start instead of restart.
	if err := a.smtp(ctx, s); err != nil {
		t.Fatal(err)
	}
	if len(f.CallsMatching("systemctl", "start", smtpUnit)) != 1 ||
		len(f.CallsMatching("systemctl", "restart", smtpUnit)) != 1 {
		t.Fatalf("calls after unchanged apply = %+v", f.Calls())
	}

	// Disabling removes files and disables the unit.
	s.SMTP.Enabled = false
	if err := a.smtp(ctx, s); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{secPath, confPath} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists after disable", p)
		}
	}
	if len(f.CallsMatching("systemctl", "disable", "--runtime", "--now", smtpUnit)) != 1 {
		t.Fatalf("disable calls = %+v", f.Calls())
	}
}
