// Command isync is the iSync CLI: login, folder-pair management, status,
// and one-off syncs. See plan.md Phase 7.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/Stinger911/iSync/internal/apiclient"
	"github.com/Stinger911/iSync/internal/apiclient/real"
	"github.com/Stinger911/iSync/internal/config"
	"github.com/Stinger911/iSync/internal/credstore"
	"github.com/Stinger911/iSync/internal/daemon"
	"github.com/Stinger911/iSync/internal/ipc"
	"github.com/Stinger911/iSync/internal/stateindex"
	"github.com/Stinger911/iSync/internal/syncengine"
	"github.com/Stinger911/iSync/internal/trash"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "version":
		fmt.Println("isync v0.1.0-dev")
	case "login":
		os.Exit(cmdLogin())
	case "logout":
		os.Exit(cmdLogout())
	case "add":
		os.Exit(cmdAdd())
	case "remove":
		os.Exit(cmdRemove())
	case "list":
		os.Exit(cmdList())
	case "status":
		os.Exit(cmdStatus())
	case "sync":
		os.Exit(cmdSync())
	case "daemon":
		os.Exit(cmdDaemon())
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: isync <command>

commands:
  login     authenticate with an Apple ID (incl. 2FA)
  logout    remove stored credentials
  add       register an iCloud Drive <-> local folder pair
  remove    unregister a folder pair
  list      list registered folder pairs
  status    show daemon/sync status
  sync      run a one-off sync
  daemon    run in the foreground (systemd ExecStart target)
  version   print version info`)
}

func cfgPath() string {
	p, err := config.DefaultPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isync: resolving config path: %v\n", err)
		os.Exit(1)
	}
	return p
}

func loadConfigOrEmpty() (config.Config, error) {
	cfg, err := config.Load(cfgPath())
	if err != nil && !os.IsNotExist(err) {
		return config.Config{}, err
	}
	return cfg, nil
}

func cmdLogin() int {
	reader := bufio.NewReader(os.Stdin)
	appleID := prompt(reader, "Apple ID: ")
	pass := promptSecret("Password: ")
	master := promptSecret("Master passphrase for credential file: ")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client := real.New()
	session, challenge, err := client.Authenticate(ctx, appleID, pass)
	if err != nil {
		fmt.Fprintf(os.Stderr, "isync login: %v\n", err)
		return 1
	}
	if challenge != nil {
		code := prompt(reader, "2FA code: ")
		session, err = challenge.Submit(ctx, code)
		if err != nil {
			fmt.Fprintf(os.Stderr, "isync login: 2FA: %v\n", err)
			return 1
		}
	}

	credPath, err := config.CredentialPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isync login: credential path: %v\n", err)
		return 1
	}
	if err := credstore.Save(credPath, string(master), credstore.Credentials{
		AppleID:       string(appleID),
		SessionCookie: "",
		TrustToken:    "",
		Extra:         map[string]string{"session": string(session.Raw)},
	}); err != nil {
		fmt.Fprintf(os.Stderr, "isync login: saving credentials: %v\n", err)
		return 1
	}

	fmt.Println("Login successful; credentials stored.")
	return 0
}

func cmdLogout() int {
	credPath, err := config.CredentialPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isync logout: credential path: %v\n", err)
		return 1
	}
	sessionPath := filepath.Join(filepath.Dir(credPath), "session.age")
	for _, p := range []string{credPath, sessionPath} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "isync logout: removing %s: %v\n", p, err)
			return 1
		}
	}
	fmt.Println("Credentials removed.")
	return 0
}

func cmdAdd() int {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: isync add <pair-id> <local-path>")
		return 1
	}
	pairID, localPath := os.Args[2], os.Args[3]
	if pairID == "" || strings.Contains(pairID, "/") {
		fmt.Fprintln(os.Stderr, "isync add: invalid pair id")
		return 1
	}

	abs, err := filepath.Abs(localPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "isync add: resolving local path: %v\n", err)
		return 1
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "isync add: local path must be an existing directory\n")
		return 1
	}

	cfg, err := loadConfigOrEmpty()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isync add: loading config: %v\n", err)
		return 1
	}
	remotePath := prompt(bufio.NewReader(os.Stdin), "iCloud Drive path: ")
	if remotePath == "" || !strings.HasPrefix(remotePath, "/") {
		fmt.Fprintln(os.Stderr, "isync add: iCloud Drive path must be an absolute path starting with /")
		return 1
	}

	for _, p := range cfg.Pairs {
		if p.ID == pairID {
			fmt.Fprintf(os.Stderr, "isync add: pair %q already exists\n", pairID)
			return 1
		}
	}

	cfg.Pairs = append(cfg.Pairs, config.Pair{
		ID:         pairID,
		LocalPath:  abs,
		RemotePath: remotePath,
		ICloudPath: remotePath,
		Enabled:    true,
	})
	if err := config.Save(cfgPath(), cfg); err != nil {
		fmt.Fprintf(os.Stderr, "isync add: saving config: %v\n", err)
		return 1
	}
	fmt.Printf("Added pair %q: %s \u003c-\u003e %s\n", pairID, abs, remotePath)
	return 0
}

func cmdRemove() int {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: isync remove <pair-id>")
		return 1
	}
	pairID := os.Args[2]

	cfg, err := loadConfigOrEmpty()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isync remove: loading config: %v\n", err)
		return 1
	}

	found := false
	for i, p := range cfg.Pairs {
		if p.ID == pairID {
			cfg.Pairs = append(cfg.Pairs[:i], cfg.Pairs[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		fmt.Fprintf(os.Stderr, "isync remove: pair %q not found\n", pairID)
		return 1
	}
	if err := config.Save(cfgPath(), cfg); err != nil {
		fmt.Fprintf(os.Stderr, "isync remove: saving config: %v\n", err)
		return 1
	}
	fmt.Printf("Removed pair %q\n", pairID)
	return 0
}

func cmdList() int {
	cfg, err := loadConfigOrEmpty()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isync list: loading config: %v\n", err)
		return 1
	}
	if len(cfg.Pairs) == 0 {
		fmt.Println("No folder pairs registered.")
		return 0
	}
	for _, p := range cfg.Pairs {
		status := "enabled"
		if !p.Enabled {
			status = "disabled"
		}
		fmt.Printf("%s [%s]\n  local:  %s\n  remote: %s\n", p.ID, status, p.LocalPath, p.RemotePath)
	}
	return 0
}

func cmdStatus() int {
	statusPath, err := ipc.DefaultStatusPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isync status: status path: %v\n", err)
		return 1
	}
	store := ipc.NewStatusStore(statusPath)
	status, err := store.Read()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isync status: reading status: %v\n", err)
		return 1
	}
	health := "healthy"
	if !status.Healthy {
		health = "unhealthy"
	}
	fmt.Printf("daemon: %s\nupdated: %s\n", health, status.UpdatedAt.Format(time.RFC3339))
	if status.Message != "" {
		fmt.Printf("message: %s\n", status.Message)
	}
	for id, ps := range status.PairStatuses {
		state := "ok"
		if !ps.OK {
			state = "error"
		}
		fmt.Printf("pair %s: %s\n", id, state)
		if ps.Error != "" {
			fmt.Printf("  error: %s\n", ps.Error)
		}
	}
	return 0
}

func cmdSync() int {
	var pairID string
	if len(os.Args) >= 3 {
		pairID = os.Args[2]
	}

	cfg, err := loadConfigOrEmpty()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isync sync: loading config: %v\n", err)
		return 1
	}
	if len(cfg.Pairs) == 0 {
		fmt.Fprintln(os.Stderr, "isync sync: no pairs configured")
		return 1
	}

	session, err := loadSession()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isync sync: session: %v\n", err)
		return 1
	}

	ctx := context.Background()
	client := real.New()
	pairs := cfg.Pairs
	if pairID != "" {
		found := false
		for _, p := range cfg.Pairs {
			if p.ID == pairID {
				pairs = []config.Pair{p}
				found = true
				break
			}
		}
		if !found {
			fmt.Fprintf(os.Stderr, "isync sync: pair %q not found\n", pairID)
			return 1
		}
	}

	maxSize := int64(cfg.Daemon.MaxFileSize)
	for _, p := range pairs {
		if !p.Enabled {
			continue
		}
		statePath, err := config.StatePath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "isync sync: state path: %v\n", err)
			return 1
		}
		state, err := stateindex.Open(statePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "isync sync: opening state: %v\n", err)
			return 1
		}
		trashDir, err := config.TrashPath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "isync sync: trash path: %v\n", err)
			return 1
		}
		mgr := trash.New(trashDir, time.Duration(cfg.Daemon.TrashRetentionDays)*24*time.Hour)
		runner := syncengine.NewRunner(client, state, mgr, maxSize)
		if err := runner.SyncPair(ctx, p.ID, p.LocalPath, p.RemotePath, session); err != nil {
			_ = state.Close()
			fmt.Fprintf(os.Stderr, "isync sync: pair %s: %v\n", p.ID, err)
			return 1
		}
		_ = state.Close()
		fmt.Printf("Synced %s\n", p.ID)
	}
	return 0
}

func cmdDaemon() int {
	cfg, err := config.Load(cfgPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "isyncd: loading config: %v\n", err)
		return 1
	}
	if len(cfg.Pairs) == 0 {
		fmt.Fprintln(os.Stderr, "isyncd: no folder pairs registered")
		return 1
	}

	session, err := loadSession()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isyncd: session: %v\n", err)
		return 1
	}

	d, err := daemon.New(cfg, session)
	if err != nil {
		fmt.Fprintf(os.Stderr, "isyncd: initializing: %v\n", err)
		return 1
	}
	defer d.Close()

	if err := d.Run(); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "isyncd: %v\n", err)
		return 1
	}
	return 0
}

func loadSession() (apiclient.Session, error) {
	credPath, err := config.CredentialPath()
	if err != nil {
		return apiclient.Session{}, err
	}
	master := string(promptSecret("Master passphrase: "))

	creds, err := credstore.Load(credPath, master)
	if err != nil {
		return apiclient.Session{}, err
	}
	if raw, ok := creds.Extra["session"]; ok && raw != "" {
		return apiclient.Session{Raw: []byte(raw)}, nil
	}

	// Fall back to password login using stored credentials.
	appleID, pass := creds.AppleID, ""
	if p, ok := creds.Extra["password"]; ok {
		pass = p
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := real.New()
	session, challenge, err := client.Authenticate(ctx, appleID, pass)
	if err != nil {
		return apiclient.Session{}, err
	}
	if challenge != nil {
		return apiclient.Session{}, errors.New("stored session missing and 2FA required; run `isync login`")
	}
	creds.Extra["session"] = string(session.Raw)
	_ = credstore.Save(credPath, master, creds)
	return session, nil
}

func prompt(r *bufio.Reader, text string) string {
	fmt.Print(text)
	s, _ := r.ReadString('\n')
	return strings.TrimSpace(s)
}

func promptSecret(text string) string {
	fmt.Print(text)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	if err != nil {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintf(os.Stderr, "isync: reading secret: %v\n", err)
		os.Exit(1)
	}
	fmt.Println()
	return string(b)
}
