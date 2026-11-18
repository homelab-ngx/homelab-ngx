package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/urfave/cli/v2"
	"homelab-ngx.com/core/internal/auth"
	"homelab-ngx.com/core/internal/secretinput"
)

func authCmd() *cli.Command {
	return &cli.Command{
		Name:  "auth",
		Usage: "The dashboard's password",
		Subcommands: []*cli.Command{
			{
				Name:  "status",
				Usage: "Show whether the dashboard has a password",
				Action: func(c *cli.Context) error {
					s, err := call[auth.Status](c.Context, http.MethodGet, "/auth", nil)
					if err != nil {
						return err
					}
					if jsonFlag(c) {
						return json.NewEncoder(os.Stdout).Encode(s)
					}
					if s.PasswordSet {
						fmt.Println("The dashboard has a password.")
					} else {
						fmt.Println("The dashboard has no password: the next visitor sets one.")
					}
					return nil
				},
			},
			{
				Name:  "set-password",
				Usage: "Set the dashboard's password and sign every browser out",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "password-stdin", Usage: "read the password from stdin"},
				},
				Action: func(c *cli.Context) error {
					pw, err := readNewPassword(c.Bool("password-stdin"))
					if err != nil {
						return err
					}
					if err := send(c.Context, http.MethodPut, "/auth/password", map[string]string{"password": pw}); err != nil {
						return err
					}
					fmt.Println("Password set. Every browser is signed out.")
					return nil
				},
			},
			{
				Name:  "reset",
				Usage: "Remove the dashboard's password: the next visitor sets one",
				Action: func(c *cli.Context) error {
					if err := send(c.Context, http.MethodDelete, "/auth/password", nil); err != nil {
						return err
					}
					fmt.Println("Password removed. The next visitor to the dashboard sets a new one.")
					return nil
				},
			},
		},
	}
}

func readNewPassword(fromStdin bool) (string, error) {
	if fromStdin {
		return secretinput.Read(os.Stdin)
	}
	pw, err := readPassword("New dashboard password: ")
	if err != nil {
		return "", err
	}
	if len(pw) < auth.MinPasswordLen {
		return "", fmt.Errorf("the password needs at least %d characters", auth.MinPasswordLen)
	}
	again, err := readPassword("Again: ")
	if err != nil {
		return "", err
	}
	if again != pw {
		return "", fmt.Errorf("the passwords differ")
	}
	return pw, nil
}
