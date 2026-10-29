package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Stinger911/iSync/internal/apiclient"
	"github.com/Stinger911/iSync/internal/apiclient/real"
	"github.com/Stinger911/iSync/internal/config"
	"github.com/Stinger911/iSync/internal/ipc"
	"github.com/Stinger911/iSync/internal/pacer"
	"github.com/Stinger911/iSync/internal/poller"
	"github.com/Stinger911/iSync/internal/stateindex"
	"github.com/Stinger911/iSync/internal/syncengine"
	"github.com/Stinger911/iSync/internal/trash"
	"github.com/Stinger911/iSync/internal/watcher"
)

// Daemon wires watcher, poller, sync runner, state index, and status IPC
type Daemon struct {
	cfg        config.Config
	session    apiclient.Session
	client     apiclient.Client
	state      *stateindex.Store
	trash      *trash.Manager
	watcher    *watcher.Manager
	poller     *poller.Poller
	sig        *poller.ActivitySignal
	status     *ipc.StatusStore
	maxSize    int64
	stop       context.CancelFunc
	wg         sync.WaitGroup
	mu         sync.Mutex
	pairErrors map[string]string
}

// New builds a Daemon from the loaded config and authenticated session.
func New(cfg config.Config, session apiclient.Session) (*Daemon, error) {
	return NewWithPaths(cfg, session, "", "", "")
}

// NewWithPaths returns a Daemon with explicit state/trash/status paths.
// Empty values use the package defaults (intended for tests).
func NewWithPaths(cfg config.Config, session apiclient.Session, statePath, trashPath, statusPath string) (*Daemon, error) {
	if statePath == "" {
		var err error
		statePath, err = config.StatePath()
		if err != nil {
			return nil, err
		}
	}
	if trashPath == "" {
		var err error
		trashPath, err = config.TrashPath()
		if err != nil {
			return nil, err
		}
	}
	if statusPath == "" {
		var err error
		statusPath, err = ipc.DefaultStatusPath()
		if err != nil {
			return nil, err
		}
	}
	_ = os.MkdirAll(filepath.Dir(statePath), 0o700)
	state, err := stateindex.Open(statePath)
	if err != nil {
		return nil, fmt.Errorf("opening state index: %w", err)
	}

	trashMgr := trash.New(trashPath, time.Duration(cfg.Daemon.TrashRetentionDays)*24*time.Hour)

	policy := pacer.Policy{
		Pacer:   pacer.New(pacer.Config{}),
		Limiter: pacer.NewLimiter(cfg.Daemon.MaxConcurrentXfers),
	}
	client := pacer.Wrap(real.New(), policy)

	sig := poller.NewActivitySignal()
	d := &Daemon{
		cfg:        cfg,
		session:    session,
		client:     client,
		state:      state,
		trash:      trashMgr,
		watcher:    watcher.NewManager(1 * time.Second),
		status:     ipc.NewStatusStore(statusPath),
		sig:        sig,
		maxSize:    int64(cfg.Daemon.MaxFileSize),
		pairErrors: make(map[string]string),
	}

	strategy := poller.Strategy{
		Idle:     cfg.Daemon.PollIntervalIdle.Std(),
		Active:   cfg.Daemon.PollIntervalActive.Std(),
		Baseline: cfg.Daemon.PollIntervalBaseline.Std(),
	}
	d.poller = poller.New(strategy, sig)
	return d, nil
}

// Run starts the daemon and blocks until Close is called or ctx is cancelled.
func (d *Daemon) Run() error {
	ctx, cancel := context.WithCancel(context.Background())
	d.stop = cancel

	for _, p := range d.cfg.Pairs {
		if !p.Enabled {
			continue
		}
		pair := p
		if _, err := d.watcher.Add(pair.ID, pair.LocalPath, func(id string, paths []string) {
			d.sig.Notify()
			go d.syncOne(ctx, id)
		}); err != nil {
			return fmt.Errorf("watching %s: %w", pair.ID, err)
		}
	}

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.poller.Run(ctx, d.syncAll)
	}()

	// Initial status write.
	d.writeStatus(ctx)

	d.wg.Wait()
	return ctx.Err()
}

// Close stops the daemon and releases resources.
func (d *Daemon) Close() error {
	if d.stop != nil {
		d.stop()
	}
	_ = d.watcher.Close()
	if d.stop != nil {
		d.wg.Wait()
	}
	return d.state.Close()
}

func (d *Daemon) syncAll(ctx context.Context) error {
	for _, p := range d.cfg.Pairs {
		if !p.Enabled {
			continue
		}
		d.syncOne(ctx, p.ID)
	}
	return nil
}

func (d *Daemon) syncOne(ctx context.Context, pairID string) {
	var pair *config.Pair
	for i := range d.cfg.Pairs {
		if d.cfg.Pairs[i].ID == pairID {
			pair = &d.cfg.Pairs[i]
			break
		}
	}
	if pair == nil {
		return
	}

	runner := syncengine.NewRunner(d.client, d.state, d.trash, d.maxSize)
	err := runner.SyncPair(ctx, pair.ID, pair.LocalPath, pair.RemotePath, d.session)

	d.mu.Lock()
	if err != nil {
		d.pairErrors[pair.ID] = err.Error()
	} else {
		delete(d.pairErrors, pair.ID)
	}
	d.mu.Unlock()

	d.writeStatus(ctx)
}

func (d *Daemon) writeStatus(ctx context.Context) {
	status := ipc.Status{
		UpdatedAt:    time.Now(),
		Healthy:      true,
		PairStatuses: make(map[string]ipc.PairStatus, len(d.cfg.Pairs)),
	}
	d.mu.Lock()
	for _, p := range d.cfg.Pairs {
		ps := ipc.PairStatus{OK: true, LastSyncAt: time.Now()}
		if msg, ok := d.pairErrors[p.ID]; ok {
			ps.OK = false
			ps.Error = msg
			ps.LastSyncAt = time.Time{}
			status.Healthy = false
		}
		status.PairStatuses[p.ID] = ps
	}
	d.mu.Unlock()

	if err := d.status.Write(status); err != nil {
		fmt.Fprintf(os.Stderr, "isyncd: writing status: %v\n", err)
	}
}
