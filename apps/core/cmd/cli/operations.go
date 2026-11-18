package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v2"
)

// printJSON writes v as indented JSON on stdout; nil prints {}.
func printJSON(v any) error {
	if v == nil {
		v = struct{}{}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// emit prints a mutation result: the daemon's JSON body under --json (or
// whenever there is no human line), otherwise the human line. A mutation
// whose answer has no body (204) prints {}.
func emit(c *cli.Context, value any, human string) error {
	if jsonFlag(c) || human == "" {
		return printJSON(value)
	}
	fmt.Fprintln(os.Stdout, human)
	return nil
}

func requireArgs(c *cli.Context, count int, usage string) error {
	if c.NArg() < count {
		return fmt.Errorf("usage: %s", usage)
	}
	return nil
}

// splitSecret splits "service/KEY" into the two path parameters of
// /secrets/{service}/{key}.
func splitSecret(name string) (service, key string, err error) {
	service, key, ok := strings.Cut(name, "/")
	if !ok || service == "" || key == "" {
		return "", "", fmt.Errorf("secret name must be service/KEY, got %q", name)
	}
	return service, key, nil
}
