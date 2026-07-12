// Command addon is the single binary for the Family TorrServer Stremio addon.
// Behaviour is selected by the first argument (subcommand); see the build plan §2.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"family-torrserver/admin"
	"family-torrserver/internal/server"
	"family-torrserver/internal/settings"
	"family-torrserver/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	sub := os.Args[1]
	switch sub {
	case "serve":
		if err := runServe(); err != nil {
			fmt.Fprintf(os.Stderr, "serve: %v\n", err)
			os.Exit(1)
		}
	case "adduser":
		if err := runAdduser(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "adduser: %v\n", err)
			os.Exit(1)
		}
	case "revoke":
		if err := runRevoke(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "revoke: %v\n", err)
			os.Exit(1)
		}
	case "listusers":
		if err := runListusers(); err != nil {
			fmt.Fprintf(os.Stderr, "listusers: %v\n", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", sub)
		usage()
		os.Exit(2)
	}
}

func runServe() error {
	set, err := settings.Load()
	if err != nil {
		return err
	}

	st, err := store.Open(set.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := server.New(set, st)
	return srv.Run(ctx)
}

// openAdminStore loads admin settings and opens the shared DB for CLI commands.
func openAdminStore() (*store.Store, *settings.AdminSettings, error) {
	set := settings.LoadAdmin()
	st, err := store.Open(set.DBPath)
	if err != nil {
		return nil, nil, err
	}
	return st, set, nil
}

func runAdduser(args []string) error {
	fs := flag.NewFlagSet("adduser", flag.ContinueOnError)
	name := fs.String("name", "", "display name for the user")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, set, err := openAdminStore()
	if err != nil {
		return err
	}
	defer st.Close()
	return admin.AddUser(st, set, *name)
}

func runRevoke(args []string) error {
	fs := flag.NewFlagSet("revoke", flag.ContinueOnError)
	token := fs.String("token", "", "token to revoke")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, _, err := openAdminStore()
	if err != nil {
		return err
	}
	defer st.Close()
	return admin.Revoke(st, *token)
}

func runListusers() error {
	st, set, err := openAdminStore()
	if err != nil {
		return err
	}
	defer st.Close()
	return admin.ListUsers(st, set)
}

func usage() {
	fmt.Fprint(os.Stderr, `Family TorrServer addon

Usage:
  addon serve                 run the HTTP server
  addon adduser --name NAME   mint a user token (milestone 2)
  addon revoke --token TOKEN  deactivate a token (milestone 2)
  addon listusers             list users (milestone 2)
`)
}
