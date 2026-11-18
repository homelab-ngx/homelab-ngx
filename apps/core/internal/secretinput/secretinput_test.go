package secretinput

import (
	"strings"
	"testing"
)

func TestReadFromStdinPreservesContent(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no newline", "secret", "secret"},
		{"one LF", "secret\n", "secret"},
		{"one CRLF", "secret\r\n", "secret"},
		{"spaces", "  secret  \n", "  secret  "},
		{"multiline", "line one\nline two\n", "line one\nline two"},
		{"keeps extra newline", "secret\n\n", "secret\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Read(strings.NewReader(tt.in))
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestReadRejectsEmptyInput(t *testing.T) {
	if _, err := Read(strings.NewReader("")); err == nil {
		t.Fatal("expected empty stdin to fail")
	}
}
