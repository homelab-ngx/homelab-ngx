package powermeter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// The part of Home Assistant's WebSocket API the meter uses: authenticate,
// then subscribe_entities for one entity. The first event carries the
// entity's whole state ("a"); each later one carries what changed ("c",
// under "+"). See https://developers.home-assistant.io/docs/api/websocket.

var (
	// errAuth is Home Assistant refusing the token.
	errAuth = errors.New("the access token was refused")
	// errNoEntity is an entity Home Assistant does not have.
	errNoEntity = errors.New("no such entity")
)

// wsURL is the WebSocket address of the Home Assistant at raw, an http(s)
// address. A path is kept, for a Home Assistant behind a proxy's prefix.
func wsURL(raw string) (string, error) {
	u, err := checkURL(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/websocket"
	return u.String(), nil
}

// checkURL parses an http(s) address with a host and no user info.
func checkURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%q is not an http(s) address such as http://homeassistant.local:8123", raw)
	}
	return u, nil
}

type haMsg struct {
	ID          int             `json:"id,omitempty"`
	Type        string          `json:"type"`
	AccessToken string          `json:"access_token,omitempty"`
	EntityIDs   []string        `json:"entity_ids,omitempty"`
	Message     string          `json:"message,omitempty"`
	Success     *bool           `json:"success,omitempty"`
	Error       *haError        `json:"error,omitempty"`
	Event       json.RawMessage `json:"event,omitempty"`
}

type haError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// haState is an entity's compressed state: "s" the state, "a" the
// attributes. In a change either may be missing.
type haState struct {
	S *string        `json:"s"`
	A map[string]any `json:"a"`
}

type haEvent struct {
	A map[string]haState `json:"a"`
	C map[string]struct {
		Plus *haState `json:"+"`
	} `json:"c"`
	R []string `json:"r"`
}

// entity is what the meter knows of the plug's sensor.
type entity struct {
	state string
	unit  string
	name  string
}

// apply merges a state or a change into e.
func (e *entity) apply(s haState) {
	if s.S != nil {
		e.state = *s.S
	}
	if u, ok := s.A["unit_of_measurement"].(string); ok {
		e.unit = u
	}
	if n, ok := s.A["friendly_name"].(string); ok {
		e.name = n
	}
}

// scale is how many watts one of a unit is.
var scale = map[string]float64{"W": 1, "kW": 1000, "mW": 0.001}

// isPower is whether the entity reports in a unit of power.
func (e entity) isPower() bool { _, ok := scale[e.unit]; return ok }

// watts is the reading in W; false when the state is not a number, such as
// "unavailable" while the plug is offline.
func (e entity) watts() (float64, bool) {
	f, err := strconv.ParseFloat(e.state, 64)
	if err != nil || !e.isPower() {
		return 0, false
	}
	return f * scale[e.unit], true
}

// session is a subscribed connection to one entity.
type session struct {
	c      *websocket.Conn
	entity string
	e      entity
}

// connect authenticates and subscribes to entityID, and reads the first
// event. It returns errAuth when the token is refused and errNoEntity when
// the first event lacks the entity.
func connect(ctx context.Context, rawURL, token, entityID string) (*session, error) {
	addr, err := wsURL(rawURL)
	if err != nil {
		return nil, err
	}
	c, _, err := websocket.Dial(ctx, addr, nil)
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(1 << 20)
	s := &session{c: c, entity: entityID}
	if err := s.start(ctx, token); err != nil {
		c.CloseNow()
		return nil, err
	}
	return s, nil
}

func (s *session) start(ctx context.Context, token string) error {
	var m haMsg
	if err := wsjson.Read(ctx, s.c, &m); err != nil {
		return err
	}
	if m.Type != "auth_required" {
		return fmt.Errorf("not Home Assistant: the first message is %q", m.Type)
	}
	if err := wsjson.Write(ctx, s.c, haMsg{Type: "auth", AccessToken: token}); err != nil {
		return err
	}
	if err := wsjson.Read(ctx, s.c, &m); err != nil {
		return err
	}
	switch m.Type {
	case "auth_ok":
	case "auth_invalid":
		return errAuth
	default:
		return fmt.Errorf("unexpected %q after auth", m.Type)
	}
	if err := wsjson.Write(ctx, s.c, haMsg{ID: 1, Type: "subscribe_entities", EntityIDs: []string{s.entity}}); err != nil {
		return err
	}
	// The result, then the first event with the entity's whole state.
	for {
		var m haMsg
		if err := wsjson.Read(ctx, s.c, &m); err != nil {
			return err
		}
		switch {
		case m.Type == "result" && m.Success != nil && !*m.Success:
			if m.Error != nil {
				return fmt.Errorf("subscribe: %s", m.Error.Message)
			}
			return errors.New("subscribe refused")
		case m.Type == "event":
			var ev haEvent
			if err := json.Unmarshal(m.Event, &ev); err != nil {
				return err
			}
			st, ok := ev.A[s.entity]
			if !ok {
				return errNoEntity
			}
			s.e.apply(st)
			return nil
		}
	}
}

// next waits for the entity's next change and returns its state after it.
func (s *session) next(ctx context.Context) (entity, error) {
	for {
		var m haMsg
		if err := wsjson.Read(ctx, s.c, &m); err != nil {
			return entity{}, err
		}
		if m.Type != "event" {
			continue
		}
		var ev haEvent
		if err := json.Unmarshal(m.Event, &ev); err != nil {
			return entity{}, err
		}
		for _, r := range ev.R {
			if r == s.entity {
				return entity{}, errNoEntity
			}
		}
		if st, ok := ev.A[s.entity]; ok {
			s.e.apply(st)
			return s.e, nil
		}
		if ch, ok := ev.C[s.entity]; ok && ch.Plus != nil {
			s.e.apply(*ch.Plus)
			return s.e, nil
		}
	}
}

// keepAlive pings every period and closes the connection when a ping goes
// unanswered, so a peer that vanished without closing ends the session.
func (s *session) keepAlive(ctx context.Context, period time.Duration) {
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, period/2)
		err := s.c.Ping(pctx)
		cancel()
		if err != nil && ctx.Err() == nil {
			s.c.CloseNow()
			return
		}
	}
}

func (s *session) close() { s.c.Close(websocket.StatusNormalClosure, "") }
