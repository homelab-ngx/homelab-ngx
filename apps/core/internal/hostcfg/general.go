package hostcfg

import (
	"bufio"
	"context"
	"net/netip"
	"os"
	"slices"
	"strings"

	"homelab-ngx.com/core/internal/cmdrunner"
)

// The general settings: the clock, its time servers and the console's
// keyboard.

const (
	// xkbRules lists the XKB layouts (xkeyboard-config, which kmscon's
	// libxkbcommon reads).
	xkbRules = "/usr/share/X11/xkb/rules/base.lst"
	// kbdModelMap pairs XKB layouts with kbd keymaps; localectl reads it too.
	kbdModelMap = "/usr/share/systemd/kbd-model-map"

	timesyncdFile = "etc/systemd/timesyncd.conf.d/10-homelab.conf"
	kmsconFile    = "etc/kmscon/kmscon.conf"
	vconsoleFile  = "etc/vconsole.conf"
	consoleUnit   = "kmsconvt@tty1.service"
)

// kmsconBase and vconsoleBase are the image's etc/kmscon/kmscon.conf and
// etc/vconsole.conf (image/base/mkosi.extra), word for word. The settings'
// copies replace the whole file, so they start from these; a test fails
// when the image's change.
const kmsconBase = `# The console (kmscon on tty1, kmsconvt@.service): JetBrains Mono Nerd Font
# sized to the screen (its icons are in the prompt, usr/share/homelab), in the dashboard's colours (background #101317, text #d8dde3,
# blue #5aa9e6). Where kmscon cannot start, getty@tty1 takes over with the
# kernel console (vt.default_* on the kernel command line, Terminus).
font-engine=freetype
font-name=JetBrainsMono Nerd Font Mono
palette=custom
palette-black=16,19,23
palette-red=229,112,122
palette-green=124,199,154
palette-yellow=224,184,102
palette-blue=90,169,230
palette-magenta=180,140,230
palette-cyan=92,198,201
palette-light-grey=216,221,227
palette-dark-grey=92,102,114
palette-light-red=240,149,156
palette-light-green=155,220,180
palette-light-yellow=240,208,138
palette-light-blue=140,200,245
palette-light-magenta=205,176,240
palette-light-cyan=138,223,224
palette-white=244,246,248
palette-foreground=216,221,227
palette-background=16,19,23
`

const vconsoleBase = `# Terminus, bold: sharper than the kernel's VGA font on a large screen. The
# IBM-437 set (ter-i) has the login banner's box drawing in 256 glyphs; the
# 512-glyph sets (ter-v) take the console's bright-colour bit and garble
# coloured text.
FONT=ter-i22b
`

// storedConsole is the console section as stored: the layout and the kbd
// keymap found for it when it was set.
type storedConsole struct {
	Console
	Keymap string `json:"keymap,omitempty"`
}

// generalEtc adds the time servers and the console's keyboard to the
// configuration extension's files.
func generalEtc(s Snapshot, files map[string]string) {
	if len(s.Time.NTP) > 0 {
		files[timesyncdFile] = "[Time]\nNTP=" + strings.Join(s.Time.NTP, " ") + "\n"
	}
	if k := s.Console.Keyboard; k != "" {
		files[kmsconFile] = kmsconBase + "xkb-layout=" + k + "\n"
		// getty@tty1, where kmscon cannot start, takes a kbd keymap.
		if s.Keymap != "" {
			files[vconsoleFile] = vconsoleBase + "KEYMAP=" + s.Keymap + "\n"
		}
	}
}

// generalReload restarts what reads the files that changed.
func (a *ImageApplier) generalReload(ctx context.Context, changed map[string]bool) []error {
	var errs []error
	if changed[timesyncdFile] {
		if err := a.run(ctx, "systemctl", "try-restart", "systemd-timesyncd.service"); err != nil {
			errs = append(errs, err)
		}
	}
	if changed[kmsconFile] || changed[vconsoleFile] {
		// The console logs root in again on its own (console-login).
		for _, u := range []string{"systemd-vconsole-setup.service", consoleUnit} {
			if err := a.run(ctx, "systemctl", "try-restart", u); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errs
}

// SetTime changes the clock's format and its time servers.
func (s *Service) SetTime(ctx context.Context, in Time) (HostSettings, error) {
	switch in.Clock {
	case ClockAuto, Clock12h, Clock24h:
	default:
		return HostSettings{}, invalid("clock: 12h, 24h or empty")
	}
	var ntp []string
	for _, n := range in.NTP {
		n = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(n)), ".")
		if n == "" {
			continue
		}
		if _, err := netip.ParseAddr(n); err != nil && !labelRE.MatchString(n) && !domainRE.MatchString(n) {
			return HostSettings{}, invalid("ntp: %q is not a host name or an address", n)
		}
		if !slices.Contains(ntp, n) {
			ntp = append(ntp, n)
		}
	}
	if len(ntp) > 4 {
		return HostSettings{}, invalid("ntp: at most four servers")
	}
	t := Time{Clock: in.Clock, NTP: nonNil(ntp)}
	return s.change(ctx, func(map[string]row) error { return s.save(ctx, "time", t, nil) })
}

// SetConsole changes the console's keyboard layout.
func (s *Service) SetConsole(ctx context.Context, in Console) (HostSettings, error) {
	k := strings.TrimSpace(in.Keyboard)
	c := storedConsole{Console: Console{Keyboard: k}}
	if k != "" {
		if !slices.ContainsFunc(s.KeyboardLayouts().Layouts, func(l KeyboardLayout) bool { return l.Name == k }) {
			return HostSettings{}, invalid("keyboard: %q is not a layout this host knows", k)
		}
		c.Keymap = kbdKeymap(s.kbdMap, k)
	}
	return s.change(ctx, func(map[string]row) error { return s.save(ctx, "console", c, nil) })
}

// KeyboardLayouts reads the layouts from the host's XKB rules.
func (s *Service) KeyboardLayouts() KeyboardLayouts {
	out := KeyboardLayouts{Layouts: []KeyboardLayout{}}
	f, err := os.Open(s.xkbRules)
	if err != nil {
		return out
	}
	defer f.Close()
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "!") {
			in = strings.TrimSpace(strings.TrimPrefix(line, "!")) == "layout"
			continue
		}
		name, desc, ok := strings.Cut(strings.TrimSpace(line), " ")
		if in && ok {
			out.Layouts = append(out.Layouts, KeyboardLayout{Name: name, Description: strings.TrimSpace(desc)})
		}
	}
	slices.SortFunc(out.Layouts, func(a, b KeyboardLayout) int { return strings.Compare(a.Description, b.Description) })
	return out
}

// kbdKeymap is the kbd keymap systemd pairs with an XKB layout (no
// variant), or "" when it pairs none.
func kbdKeymap(path, layout string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// keymap  x11-layout  x11-model  x11-variant  x11-options
		c := strings.Fields(sc.Text())
		if len(c) >= 4 && !strings.HasPrefix(c[0], "#") && c[1] == layout && c[3] == "-" {
			return c[0]
		}
	}
	return ""
}

// readTimeSync asks timedated and timesyncd whether the clock is synced
// and from where.
func readTimeSync(ctx context.Context, r cmdrunner.Runner) TimeSync {
	var t TimeSync
	if r == nil {
		return t
	}
	show := func(args ...string) string {
		res, err := r.Run(ctx, cmdrunner.Cmd{Module: "hostcfg", Name: "timedatectl", Args: append(args, "--value")})
		if err != nil {
			return ""
		}
		return strings.TrimSpace(res.Stdout)
	}
	t.Synced = show("show", "--property=NTPSynchronized") == "yes"
	if t.Synced {
		t.Server = show("show-timesync", "--property=ServerName")
	}
	return t
}
