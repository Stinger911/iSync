// Command isyncd is the iSync background daemon: wires the local watcher,
// remote poller, sync engine, credential store, and state index together
// and runs continuously under systemd. See plan.md Phase 7.
package main

import (
	"fmt"
	"os"

	"github.com/Stinger911/iSync/internal/apiclient"
	"github.com/Stinger911/iSync/internal/config"
	"github.com/Stinger911/iSync/internal/credstore"
	"github.com/Stinger911/iSync/internal/daemon"
)

func main() {
	path, err := config.DefaultPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isyncd: resolving config path: %v\n", err)
		os.Exit(1)
	}

	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "isyncd: loading config %s: %v\n", path, err)
		os.Exit(1)
	}

	if len(cfg.Pairs) == 0 {
		fmt.Fprintln(os.Stderr, "isyncd: no folder pairs registered — run `isync add` first")
		os.Exit(1)
	}

	session, err := loadSession()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isyncd: session: %v\n", err)
		os.Exit(1)
	}

	d, err := daemon.New(cfg, session)
	if err != nil {
		fmt.Fprintf(os.Stderr, "isyncd: initializing: %v\n", err)
		os.Exit(1)
	}
	defer d.Close()

	if err := d.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "isyncd: %v\n", err)
		os.Exit(1)
	}
}

func loadSession() (apiclient.Session, error) {
	credPath, err := config.CredentialPath()
	if err != nil {
		return apiclient.Session{}, err
	}
	if !credstore.Exists(credPath) {
		return apiclient.Session{}, fmt.Errorf("no credentials stored; run `isync login` first")
	}

	passphrase := os.Getenv("ISYNC_MASTER_PASSPHRASE")
	if passphrase == "" {
		// systemd LoadCredential= maps the credential to a file path in
		// $CREDENTIALS_DIRECTORY, or we allow a plain env var.
		dir := os.Getenv("CREDENTIALS_DIRECTORY")
		if dir != "" {
			data, err := os.ReadFile(dir + "/master-passphrase")
			if err == nil {
				passphrase = string(data)
			}
		}
	}
	if passphrase == "" {
		return apiclient.Session{}, fmt.Errorf("ISYNC_MASTER_PASSPHRASE not set and no systemd credential found")
	}

	creds, err := credstore.Load(credPath, passphrase)
	if err != nil {
		return apiclient.Session{}, err
	}
	if raw, ok := creds.Extra["session"]; ok && raw != "" {
		return apiclient.Session{Raw: []byte(raw)}, nil
	}
	return apiclient.Session{}, fmt.Errorf("no stored session; run `isync login` again")
}
