// Command addon is the single binary for the Family TorrServer Stremio addon.
// Behaviour is selected by the first argument (subcommand); see the build plan §2.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

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
	case "adduser", "revoke", "listusers":
		// Implemented in milestone 2 (admin package).
		fmt.Fprintf(os.Stderr, "%q is not implemented yet (milestone 2)\n", sub)
		os.Exit(1)
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

func usage() {
	fmt.Fprint(os.Stderr, `Family TorrServer addon

Usage:
  addon serve                 run the HTTP server
  addon adduser --name NAME   mint a user token (milestone 2)
  addon revoke --token TOKEN  deactivate a token (milestone 2)
  addon listusers             list users (milestone 2)
`)
}
