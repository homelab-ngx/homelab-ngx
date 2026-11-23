package powermeter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"homelab-ngx.com/core/internal/agefile"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/store"
)

const (
	section = "power_meter"
	// The wait before a reconnect doubles from firstWait up to lastWait; a
	// connection that held for stableFor starts the count again.
	firstWait = 5 * time.Second
	lastWait  = 5 * time.Minute
	stableFor = time.Minute
	// pingEvery finds a Home Assistant that went away without closing.
	pingEvery = 30 * time.Second
	// testTimeout bounds a connection test.
	testTimeout = 10 * time.Second
)

var entityID = regexp.MustCompile(`^[a-z0-9_]+\.[a-z0-9_]+$`)

// settings are what is stored in the clear; the token is sealed beside them.
type settings struct {
	URL      string `json:"url"`
	Entity   string `json:"entity"`
	IdleOnly bool   `json:"idle_only"`
}

func (c settings) on(token string) bool { return c.URL != "" && c.Entity != "" && token != "" }

// Service keeps a subscription to the meter's entity while one is set up.
type Service struct {
	st      *store.Store
	keyFile string
	log     *slog.Logger
	now     func() time.Time
	// first and last bound the reconnect wait; tests shorten them.
	first, last time.Duration

	mu      sync.Mutex
	loaded  bool
	cfg     settings
	token   string
	state   PowerMeterState
	detail  string
	e       entity
	at      time.Time
	cancel  context.CancelFunc // ends the current session
	restart chan struct{}
}

// New builds the service. keyFile is the host's age key, which seals the
// token.
func New(st *store.Store, keyFile string, log *slog.Logger) *Service {
	return &Service{
		st: st, keyFile: keyFile, log: log, now: time.Now, first: firstWait, last: lastWait,
		state: StateOff, restart: make(chan struct{}, 1),
	}
}

// Wall is the meter's last reading in W; false while not connected or when
// the plug reports none.
func (s *Service) Wall() (float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateConnected {
		return 0, false
	}
	return s.e.watts()
}

// Run holds the subscription until ctx ends, reconnecting after a failure
// and whenever the settings change.
func (s *Service) Run(ctx context.Context) {
	wait := s.first
	for ctx.Err() == nil {
		s.mu.Lock()
		if err := s.load(ctx); err != nil {
			s.log.Warn("power meter settings unreadable", "error", err)
		}
		cfg, token := s.cfg, s.token
		select { // these settings include any change signalled so far
		case <-s.restart:
		default:
		}
		if !cfg.on(token) {
			s.setState(StateOff, "")
		} else {
			s.setState(StateConnecting, "")
		}
		sctx, cancel := context.WithCancel(ctx)
		s.cancel = cancel
		s.mu.Unlock()

		if !cfg.on(token) {
			s.idle(ctx, 0)
			cancel()
			continue
		}
		began := s.now()
		err := s.session(sctx, cfg, token)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if s.now().Sub(began) >= stableFor {
			wait = s.first
		}
		s.mu.Lock()
		changed := s.cfg != cfg || s.token != token
		switch {
		case changed:
		case errors.Is(err, errAuth):
			s.setState(StateAuthFailed, "Home Assistant refused the token")
		case errors.Is(err, errNoEntity):
			s.setState(StateError, fmt.Sprintf("Home Assistant has no %s", cfg.Entity))
		case err != nil:
			s.setState(StateError, err.Error())
		}
		s.mu.Unlock()
		if changed {
			wait = s.first
			continue
		}
		s.log.Warn("power meter disconnected", "error", err, "retry_in", wait)
		if errors.Is(err, errAuth) {
			s.idle(ctx, 0) // a refused token stays refused until it changes
			continue
		}
		if s.idle(ctx, wait) {
			wait = s.first
		} else {
			wait = min(wait*2, s.last)
		}
	}
}

// idle waits for d (forever when 0), a settings change or the end of ctx;
// true when the settings changed.
func (s *Service) idle(ctx context.Context, d time.Duration) bool {
	var t <-chan time.Time
	if d > 0 {
		timer := time.NewTimer(d)
		defer timer.Stop()
		t = timer.C
	}
	select {
	case <-ctx.Done():
	case <-t:
	case <-s.restart:
		return true
	}
	return false
}

// session connects and records each reading until the connection ends.
func (s *Service) session(ctx context.Context, cfg settings, token string) error {
	dctx, cancel := context.WithTimeout(ctx, testTimeout)
	ss, err := connect(dctx, cfg.URL, token, cfg.Entity)
	cancel()
	if err != nil {
		return err
	}
	defer ss.close()
	go ss.keepAlive(ctx, pingEvery)
	s.mu.Lock()
	s.e, s.at = ss.e, s.now()
	s.setState(StateConnected, "")
	s.mu.Unlock()
	s.log.Info("power meter connected", "entity", cfg.Entity, "unit", ss.e.unit)
	for {
		e, err := ss.next(ctx)
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.e, s.at = e, s.now()
		s.mu.Unlock()
	}
}

// setState records the state; the caller holds mu.
func (s *Service) setState(st PowerMeterState, detail string) {
	s.state, s.detail = st, detail
	if st != StateConnected {
		s.e = entity{name: s.e.name}
	}
}

// load reads the stored settings and opens the token, once.
func (s *Service) load(ctx context.Context) error {
	if s.loaded {
		return nil
	}
	rs, err := s.st.Queries().ListHostSettings(ctx)
	if err != nil {
		return err
	}
	s.loaded = true
	for _, r := range rs {
		if r.Section != section {
			continue
		}
		if err := json.Unmarshal([]byte(r.Value), &s.cfg); err != nil {
			return err
		}
		if len(r.Secret) > 0 {
			b, err := agefile.Open(s.keyFile, r.Secret)
			if err != nil {
				return fmt.Errorf("the stored token does not open: %w", err)
			}
			s.token = string(b)
		}
	}
	return nil
}

// Status is the settings, the connection's state and the last reading.
func (s *Service) Status(ctx context.Context) (PowerMeter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(ctx); err != nil {
		return PowerMeter{}, err
	}
	out := PowerMeter{
		URL: s.cfg.URL, Entity: s.cfg.Entity, HasToken: s.token != "", IdleOnly: s.cfg.IdleOnly,
		State: s.state, Detail: s.detail, Name: s.e.name,
	}
	if w, ok := s.e.watts(); ok && s.state == StateConnected {
		out.Watts = &w
	}
	if !s.at.IsZero() {
		out.At = s.at.Unix()
	}
	return out, nil
}

// IdleOnly is whether the profile comparison shows only idle minutes.
func (s *Service) IdleOnly(ctx context.Context) bool {
	st, err := s.Status(ctx)
	return err == nil && st.IdleOnly
}

// valid checks and tidies the settings in in.
func valid(in PowerMeterSet) (settings, error) {
	c := settings{URL: strings.TrimRight(strings.TrimSpace(in.URL), "/"), Entity: strings.TrimSpace(in.Entity), IdleOnly: in.IdleOnly}
	if c.URL == "" && c.Entity == "" {
		return c, nil
	}
	if _, err := checkURL(c.URL); err != nil {
		return c, httpx.Unprocessable("invalid_url", "%s", err)
	}
	if !entityID.MatchString(c.Entity) {
		return c, httpx.Unprocessable("invalid_entity", "%q is not an entity id such as sensor.server_plug_power", in.Entity)
	}
	return c, nil
}

// Set stores the settings and reconnects when the connection changed. An
// empty address and entity turn the meter off and remove the token.
func (s *Service) Set(ctx context.Context, in PowerMeterSet) (PowerMeter, error) {
	c, err := valid(in)
	if err != nil {
		return PowerMeter{}, err
	}
	s.mu.Lock()
	if err := s.load(ctx); err != nil {
		s.mu.Unlock()
		return PowerMeter{}, err
	}
	token := s.token
	if in.Token != nil {
		token = strings.TrimSpace(*in.Token)
	}
	if c.URL == "" {
		token = ""
	} else if token == "" {
		s.mu.Unlock()
		return PowerMeter{}, httpx.Unprocessable("token_required", "a long-lived access token from Home Assistant is needed")
	}
	var sealed []byte
	if token != "" {
		if sealed, err = agefile.Seal(s.keyFile, []byte(token)); err != nil {
			s.mu.Unlock()
			return PowerMeter{}, httpx.Internal("token_not_sealed", "the token could not be stored: %v", err)
		}
	}
	v, _ := json.Marshal(c)
	err = s.st.Queries().UpsertHostSetting(ctx, store.UpsertHostSettingParams{
		Section: section, Value: string(v), Secret: sealed, UpdatedAt: s.now().UnixMilli(),
	})
	changed := err == nil && (c.URL != s.cfg.URL || c.Entity != s.cfg.Entity || token != s.token)
	if err == nil {
		s.cfg, s.token = c, token
	}
	if changed {
		if s.cancel != nil {
			s.cancel()
		}
		select {
		case s.restart <- struct{}{}:
		default:
		}
		s.setState(StateConnecting, "")
		if !c.on(token) {
			s.setState(StateOff, "")
		}
		s.e, s.at = entity{}, time.Time{}
	}
	s.mu.Unlock()
	if err != nil {
		return PowerMeter{}, err
	}
	s.log.Info("power meter set", "url", c.URL, "entity", c.Entity, "token_set", token != "", "idle_only", c.IdleOnly)
	return s.Status(ctx)
}

// Test connects once with in, or with the stored token when in has none,
// and reads the entity.
func (s *Service) Test(ctx context.Context, in PowerMeterSet) (PowerMeterTest, error) {
	c, err := valid(in)
	if err != nil {
		return PowerMeterTest{}, err
	}
	if c.URL == "" {
		return PowerMeterTest{}, httpx.Unprocessable("invalid_url", "enter Home Assistant's address")
	}
	s.mu.Lock()
	err = s.load(ctx)
	token := s.token
	s.mu.Unlock()
	if err != nil {
		return PowerMeterTest{}, err
	}
	if in.Token != nil {
		token = strings.TrimSpace(*in.Token)
	}
	if token == "" {
		return PowerMeterTest{}, httpx.Unprocessable("token_required", "a long-lived access token from Home Assistant is needed")
	}
	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	ss, err := connect(ctx, c.URL, token, c.Entity)
	switch {
	case errors.Is(err, errAuth):
		return PowerMeterTest{Code: "auth_invalid", Error: "Home Assistant refused the token"}, nil
	case errors.Is(err, errNoEntity):
		return PowerMeterTest{Code: "entity_not_found", Error: fmt.Sprintf("Home Assistant has no %s", c.Entity)}, nil
	case err != nil:
		return PowerMeterTest{Code: "unreachable", Error: err.Error()}, nil
	}
	ss.close()
	out := PowerMeterTest{Name: ss.e.name}
	if !ss.e.isPower() && ss.e.state != "unavailable" && ss.e.state != "unknown" {
		out.Code, out.Error = "not_power", fmt.Sprintf("%s reports %q, not watts", c.Entity, ss.e.unit)
		return out, nil
	}
	out.OK = true
	if w, ok := ss.e.watts(); ok {
		out.Watts = &w
	}
	return out, nil
}
