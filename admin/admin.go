// Package admin implements the adduser/revoke/listusers CLI subcommands (§2).
// They operate on the same SQLite file the server reads, so changes take effect
// live.
package admin

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"nuvio-torrserver/internal/settings"
	"nuvio-torrserver/internal/store"
)

// AddUser mints a token, then prints the full install URL for it.
func AddUser(st *store.Store, set *settings.AdminSettings, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("--name is required")
	}
	token, err := st.CreateUser(name)
	if err != nil {
		return err
	}
	fmt.Printf("Created user %q\n", name)
	fmt.Printf("Install URL: %s\n", installURL(set.PublicHost, token))
	return nil
}

// Revoke deactivates a token.
func Revoke(st *store.Store, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("--token is required")
	}
	if err := st.Revoke(token); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no user with token %q", token)
		}
		return err
	}
	fmt.Printf("Revoked token %s\n", token)
	return nil
}

// ListUsers prints all users as a table.
func ListUsers(st *store.Store, set *settings.AdminSettings) error {
	users, err := st.ListUsers()
	if err != nil {
		return err
	}
	if len(users) == 0 {
		fmt.Println("No users yet. Mint one with: addon adduser --name \"<name>\"")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tTOKEN\tCREATED\tACTIVE\tINSTALL URL")
	for _, u := range users {
		fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%s\n",
			u.Name, u.Token, u.CreatedAt.Format("2006-01-02 15:04"), u.Active,
			installURL(set.PublicHost, u.Token),
		)
	}
	return w.Flush()
}

func installURL(publicHost, token string) string {
	if strings.TrimSpace(publicHost) == "" {
		// PUBLIC_HOST not set (e.g. running the CLI locally); emit a path the
		// operator can prefix with their scheme+host.
		return fmt.Sprintf("/u/%s/manifest.json", token)
	}
	host := strings.TrimSuffix(publicHost, "/")
	scheme := "https://"
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		scheme = ""
	}
	return fmt.Sprintf("%s%s/u/%s/manifest.json", scheme, host, token)
}
