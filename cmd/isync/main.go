// Command isync is the iSync CLI: login, folder-pair management, status,
// and one-off syncs. See plan.md Phase 7.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "version":
		fmt.Println("isync (dev build) — see plan.md for the implementation roadmap")
	case "login", "logout", "add", "remove", "list", "status", "sync", "daemon":
		fmt.Fprintf(os.Stderr, "isync %s: not implemented yet (see plan.md Phase 7)\n", os.Args[1])
		os.Exit(1)
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
