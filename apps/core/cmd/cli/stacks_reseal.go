package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/urfave/cli/v2"

	"homelab-ngx.com/core/internal/stacks"
)

// readAgeKeyFlag parses --age-key-file and --age-key-stdin even when given after
// positional arguments, returning the remaining arguments and the key material.
func readAgeKeyFlag(c *cli.Context, required bool) ([]string, string, error) {
	filePath := c.String("age-key-file")
	fromStdin := c.Bool("age-key-stdin")
	raw := c.Args().Slice()
	args := make([]string, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		a := raw[i]
		switch {
		case a == "--age-key-stdin":
			fromStdin = true
		case a == "--age-key-file":
			if i+1 >= len(raw) {
				return nil, "", errors.New("--age-key-file requires a path")
			}
			i++
			filePath = raw[i]
		case strings.HasPrefix(a, "--age-key-file="):
			filePath = strings.TrimPrefix(a, "--age-key-file=")
		default:
			args = append(args, a)
		}
	}
	if filePath != "" && fromStdin {
		return nil, "", errors.New("use only one of --age-key-file or --age-key-stdin")
	}
	if filePath != "" {
		b, err := os.ReadFile(filePath)
		if err != nil {
			return nil, "", err
		}
		if strings.TrimSpace(string(b)) == "" {
			return nil, "", fmt.Errorf("%s: age key file is empty", filePath)
		}
		return args, string(b), nil
	}
	if fromStdin {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, "", fmt.Errorf("reading age key from stdin: %w", err)
		}
		if strings.TrimSpace(string(b)) == "" {
			return nil, "", errors.New("age key on stdin is empty")
		}
		return args, string(b), nil
	}
	if required {
		return nil, "", errors.New("pass --age-key-file <path> or --age-key-stdin")
	}
	return args, "", nil
}

// printVarsError explains why a stack's variables could not be decrypted.
func printVarsError(s stacks.Stack) {
	if s.VariablesKeyMismatch {
		fmt.Println("✗ Variables are sealed to another host's age key.")
		fmt.Printf("Re-seal with: homelab stacks var reseal %s --age-key-file <keys.txt>\n", s.Name)
		return
	}
	fmt.Printf("✗ Variables could not be read: %s\n", s.VariablesError)
}
