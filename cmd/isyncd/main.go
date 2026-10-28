// Command isyncd is the iSync background daemon: wires the local watcher,
// remote poller, sync engine, credential store, and state index together
// and runs continuously under systemd. See plan.md Phase 7.
package main

import (
	"fmt"
	"os"

	"github.com/Stinger911/iSync/internal/config"
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

	// TODO(plan.md Phase 7): wire credstore, stateindex, apiclient, watcher,
	// poller, and syncengine together and run the daemon loop.
	fmt.Fprintln(os.Stderr, "isyncd: daemon loop not implemented yet (see plan.md Phase 7)")
	os.Exit(1)
}
