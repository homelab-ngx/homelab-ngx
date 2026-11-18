package secretinput

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// Read reads a --value-stdin secret: all of r with at most one
// trailing LF or CRLF dropped. It is the only place that trim happens: the
// value it returns goes on the wire and is stored exactly (secrets.Set trims
// nothing).
func Read(r io.Reader) (string, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("reading secret from stdin: %w", err)
	}
	value := string(raw)
	if v, ok := strings.CutSuffix(value, "\r\n"); ok {
		value = v
	} else {
		value = strings.TrimSuffix(value, "\n")
	}
	if value == "" {
		return "", errors.New("secret value from stdin must not be empty")
	}
	return value, nil
}
