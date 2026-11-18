package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/urfave/cli/v2"
	"homelab-ngx.com/core/internal/secretinput"
	"homelab-ngx.com/core/internal/secrets"
)

func secretCmd() *cli.Command {
	return &cli.Command{
		Name:  "secret",
		Usage: "Manage homelab secrets",
		Subcommands: []*cli.Command{
			{Name: "show", Usage: "Show secret metadata", ArgsUsage: "<name>", Action: secretShowAction},
			{Name: "delete", Usage: "Wipe a secret value", ArgsUsage: "<name>", Action: secretDeleteAction},
			{
				Name:  "list",
				Usage: "List all declared secrets and their status",
				Action: func(c *cli.Context) error {
					return secretList(c)
				},
			},
			{
				Name:      "add",
				Usage:     "Declare and set a new secret",
				ArgsUsage: "<name>",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "value-stdin", Usage: "read the secret value from stdin"},
					&cli.StringFlag{Name: "description", Usage: "secret description (avoids an interactive prompt)"},
				},
				Action: func(c *cli.Context) error {
					if c.NArg() < 1 {
						return fmt.Errorf("usage: homelab secret add <name>")
					}
					if os.Geteuid() != 0 {
						return fmt.Errorf("homelab secret add requires sudo")
					}
					args, flags := argsAndBools(c, "value-stdin")
					first := ""
					if len(args) > 0 {
						first = args[0]
					}
					return handleSecretAdd(c, first, c.String("description"), flags["value-stdin"])
				},
			},
			{
				Name:      "set",
				Usage:     "Rotate the value of an existing secret",
				ArgsUsage: "[name]",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "value-stdin", Usage: "read the secret value from stdin"},
				},
				Action: func(c *cli.Context) error {
					if os.Geteuid() != 0 {
						return fmt.Errorf("homelab secret set requires sudo")
					}
					args, flags := argsAndBools(c, "value-stdin")
					return handleSecretSet(c, args, flags["value-stdin"])
				},
			},
		},
	}
}

func secretShowAction(c *cli.Context) error {
	if err := requireArgs(c, 1, "homelab secret show <name>"); err != nil {
		return err
	}
	svc, key, err := splitSecret(c.Args().First())
	if err != nil {
		return err
	}
	st, err := call[secrets.SecretStatus](c.Context, http.MethodGet, secretPath(svc, key), nil)
	if err != nil {
		return err
	}
	return printJSON(st)
}

func secretDeleteAction(c *cli.Context) error {
	if err := requireArgs(c, 1, "homelab secret delete <name>"); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("homelab secret delete requires sudo")
	}
	name := c.Args().First()
	svc, key, err := splitSecret(name)
	if err != nil {
		return err
	}
	m, err := call[secrets.SecretMutation](c.Context, http.MethodDelete, secretPath(svc, key), nil)
	if err != nil {
		return err
	}
	printWarnings(m)
	return emit(c, m, "Wiped secret "+name)
}

// secretPath is the API path of one secret.
func secretPath(svc, key string) string {
	return "/secrets/" + url.PathEscape(svc) + "/" + url.PathEscape(key)
}

func secretList(c *cli.Context) error {
	l, err := call[secrets.SecretsList](c.Context, http.MethodGet, "/secrets", nil)
	if err != nil {
		return err
	}
	if jsonFlag(c) {
		return printJSON(l)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tDESCRIPTION\tPRESENT\tMODIFIED")
	for _, s := range l.Secrets {
		present := "✗"
		if s.Present {
			present = "✔"
		}
		modified := ""
		if s.ModifiedAt != nil {
			modified = s.ModifiedAt.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, s.Description, present, modified)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if l.DeployPending {
		fmt.Println("\n⚠ Deploy pending — run 'homelab deploy' to apply secret changes.")
	}
	return nil
}

func printWarnings(m *secrets.SecretMutation) {
	if m == nil {
		return
	}
	for _, warning := range m.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", warning)
	}
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

// putSecret sets a secret and prints the outcome ("added" / "updated").
func putSecret(c *cli.Context, name, description, val, past string) error {
	svc, key, err := splitSecret(name)
	if err != nil {
		return err
	}
	m, err := call[secrets.SecretMutation](c.Context, http.MethodPut, secretPath(svc, key), secrets.SecretSet{Value: val, Description: description})
	if err != nil {
		return err
	}
	if jsonFlag(c) {
		return printJSON(m)
	}
	fmt.Printf("✓ Secret '%s' %s.", name, past)
	if m.DeployPending {
		fmt.Println(" Deploy pending — run 'homelab deploy' to apply.")
	} else {
		fmt.Println()
	}
	printWarnings(m)
	return nil
}

func handleSecretAdd(c *cli.Context, name, description string, fromStdin bool) error {
	if _, _, err := splitSecret(name); err != nil {
		return err
	}
	if !jsonFlag(c) {
		fmt.Printf("Adding new secret: %s\n", name)
	}
	desc := description
	if desc == "" {
		var err error
		desc, err = readLine("Description: ")
		if err != nil {
			return fmt.Errorf("reading description: %w", err)
		}
	}
	val, err := readSecretFor(name, fromStdin)
	if err != nil {
		return err
	}
	return putSecret(c, name, desc, val, "added")
}

func handleSecretSet(c *cli.Context, args []string, fromStdin bool) error {
	name := ""
	if len(args) >= 1 && args[0] != "" {
		name = args[0]
	} else {
		if fromStdin {
			return fmt.Errorf("secret name is required with --value-stdin")
		}
		var err error
		if name, err = pickSecretInteractive(c); err != nil {
			return err
		}
		if name == "" {
			return nil
		}
	}
	if _, _, err := splitSecret(name); err != nil {
		return err
	}
	val, err := readSecretFor(name, fromStdin)
	if err != nil {
		return err
	}
	return putSecret(c, name, "", val, "updated")
}

func pickSecretInteractive(c *cli.Context) (string, error) {
	l, err := call[secrets.SecretsList](c.Context, http.MethodGet, "/secrets", nil)
	if err != nil {
		return "", err
	}
	list := l.Secrets
	if len(list) == 0 {
		fmt.Fprintln(os.Stderr, "No secrets registered.")
		return "", nil
	}
	fmt.Println("Select a secret to set:")
	for i, s := range list {
		status := "✗"
		if s.Present {
			status = "✔"
		}
		modified := ""
		if s.ModifiedAt != nil {
			modified = " (last set: " + s.ModifiedAt.Local().Format("2006-01-02 15:04") + ")"
		}
		fmt.Printf("  %2d. %s %s — %s%s\n", i+1, status, s.Name, s.Description, modified)
	}
	fmt.Println("   q. Quit")
	fmt.Print("\nChoice: ")
	var input string
	fmt.Scanln(&input)
	if input == "q" || input == "Q" || input == "" {
		return "", nil
	}
	n, err := strconv.Atoi(input)
	if err != nil || n < 1 || n > len(list) {
		fmt.Fprintln(os.Stderr, "Invalid selection.")
		return "", nil
	}
	return list[n-1].Name, nil
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
