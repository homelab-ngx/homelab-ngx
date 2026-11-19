package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v2"

	"homelab-ngx.com/core/internal/secretinput"
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

// readSecretFor reads the new value from stdin or the terminal prompt.
func readSecretFor(name string, fromStdin bool) (string, error) {
	var val string
	var err error
	if fromStdin {
		val, err = secretinput.Read(os.Stdin)
	} else {
		val, err = readPassword(fmt.Sprintf("Value for %s: ", name))
	}
	if err != nil {
		return "", fmt.Errorf("reading value: %w", err)
	}
	if val == "" {
		return "", fmt.Errorf("value must not be empty")
	}
	return val, nil
}

// argsAndBools splits c's arguments from the named boolean flags given after
// them. urfave/cli v2 stops parsing flags at the first argument, and the
// documented form puts --value-stdin last (`… set <name> --value-stdin`).
// A flag given before the arguments is read as usual.
func argsAndBools(c *cli.Context, names ...string) ([]string, map[string]bool) {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = c.Bool(n)
	}
	args := []string{}
	for _, a := range c.Args().Slice() {
		if n, ok := strings.CutPrefix(a, "--"); ok {
			if _, known := set[n]; known {
				set[n] = true
				continue
			}
		}
		args = append(args, a)
	}
	return args, set
}
