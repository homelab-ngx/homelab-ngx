// Package powermeter reads a power meter the host is plugged into, such as
// a smart plug, from Home Assistant. It holds one WebSocket subscription to
// the plug's power entity and keeps the last value it reported.
package powermeter

import "github.com/danielgtaylor/huma/v2"

// PowerMeterState is where the connection to Home Assistant stands.
type PowerMeterState string

const (
	StateOff        PowerMeterState = "off"
	StateConnecting PowerMeterState = "connecting"
	StateConnected  PowerMeterState = "connected"
	StateAuthFailed PowerMeterState = "auth_failed"
	StateError      PowerMeterState = "error"
)

// Schema documents PowerMeterState as its enum.
func (PowerMeterState) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Enum: []any{
		string(StateOff), string(StateConnecting), string(StateConnected), string(StateAuthFailed), string(StateError),
	}}
}

// PowerMeter is the meter's settings and its last reading. The token is
// never returned.
type PowerMeter struct {
	URL      string          `json:"url" doc:"Home Assistant's address, such as http://homeassistant.local:8123; empty when no meter is set up."`
	Entity   string          `json:"entity" doc:"The power sensor's entity id, such as sensor.server_plug_power."`
	HasToken bool            `json:"has_token" doc:"Whether a long-lived access token is stored."`
	IdleOnly bool            `json:"idle_only" doc:"Whether the profile comparison shows only the minutes the host was idle. A display choice: what is stored is the same."`
	State    PowerMeterState `json:"state"`
	Detail   string          `json:"detail" doc:"Why the state is auth_failed or error."`
	Name     string          `json:"name" doc:"The entity's friendly name in Home Assistant."`
	Watts    *float64        `json:"watts" nullable:"true" doc:"The last reading in W; null while not connected or when the plug reports none."`
	At       int64           `json:"at" doc:"When the plug last reported, in unix seconds; 0 when never."`
}

// PowerMeterSet changes the meter's settings. An empty url and entity turn
// the meter off and remove the token.
type PowerMeterSet struct {
	URL      string  `json:"url" maxLength:"512"`
	Entity   string  `json:"entity" maxLength:"255"`
	Token    *string `json:"token,omitempty" maxLength:"4096" doc:"Absent keeps the stored token; empty removes it."`
	IdleOnly bool    `json:"idle_only"`
}

// PowerMeterTest is the answer to a connection test.
type PowerMeterTest struct {
	OK    bool     `json:"ok"`
	Code  string   `json:"code,omitempty" enum:"unreachable,auth_invalid,entity_not_found,not_power" doc:"Why the test failed."`
	Error string   `json:"error,omitempty"`
	Name  string   `json:"name,omitempty" doc:"The entity's friendly name."`
	Watts *float64 `json:"watts" nullable:"true" doc:"The reading in W; null when the plug reports none."`
}
