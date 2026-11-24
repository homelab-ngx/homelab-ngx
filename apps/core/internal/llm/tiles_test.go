package llm

import (
	"context"
	"testing"

	"homelab-ngx.com/core/internal/tilestatus"
)

func TestTiles(t *testing.T) {
	s := newSettings(t)
	ctx := context.Background()
	if got, err := s.Tiles(ctx); err != nil || got["assistant"].State != tilestatus.Off {
		t.Fatalf("no provider: %+v, %v", got, err)
	}
	key := "sk-ant-secret"
	if _, err := s.Set(ctx, AssistantSettingsSet{Provider: KindAnthropic, APIKey: &key}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Tiles(ctx); err != nil || got["assistant"].State != tilestatus.OK {
		t.Fatalf("anthropic: %+v, %v", got, err)
	}
}
