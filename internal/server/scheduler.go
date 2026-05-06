// Internal scheduler for managed-service automatic backups.
//
// This is NOT the user-facing cronjob workload form (that's a Nomad periodic
// batch job). This is an in-process cron loop inside blobd that fires
// pg_dump+ship+prune on a schedule for each Postgres instance that has a
// backup-config.
//
// One goroutine per instance; managed by github.com/robfig/cron/v3. Reload
// is idempotent and safe to call from any goroutine.
package server

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/irrigationreal/blob/internal/api"
)

type Scheduler struct {
	srv *Server
	mu  sync.Mutex
	c   *cron.Cron
	// instance -> entry id, so Reload can remove the previous schedule for
	// just this instance without disturbing others.
	entries map[string]cron.EntryID
}

func newScheduler(srv *Server) *Scheduler {
	c := cron.New(cron.WithLocation(time.UTC), cron.WithSeconds())
	return &Scheduler{srv: srv, c: c, entries: map[string]cron.EntryID{}}
}

func (sc *Scheduler) Start() {
	sc.c.Start()
	configs, err := sc.srv.listAllBackupConfigs()
	if err != nil {
		log.Printf("scheduler: list configs: %v", err)
		return
	}
	for _, cfg := range configs {
		sc.add(cfg)
	}
}

func (sc *Scheduler) Stop() {
	if sc == nil || sc.c == nil {
		return
	}
	ctx := sc.c.Stop()
	<-ctx.Done()
}

// Reload re-reads the config for `instance` from disk and updates its job.
// Removes the schedule entirely if the config is gone or disabled.
func (sc *Scheduler) Reload(instance string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if id, ok := sc.entries[instance]; ok {
		sc.c.Remove(id)
		delete(sc.entries, instance)
	}
	cfg, err := sc.srv.loadBackupConfig(instance)
	if err != nil {
		return
	}
	sc.add(cfg)
}

func (sc *Scheduler) add(cfg *api.PostgresBackupConfig) {
	if !cfg.Enabled {
		return
	}
	if cfg.Schedule == "" {
		cfg.Schedule = defaultBackupSchedule
	}
	// robfig/cron with WithSeconds expects 6 fields. The user provides a
	// standard 5-field cron expression; prepend "0" so it fires on the
	// 0-second of each matching minute.
	expr := "0 " + cfg.Schedule
	id, err := sc.c.AddFunc(expr, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		sc.runOnce(ctx, cfg.Instance)
	})
	if err != nil {
		log.Printf("scheduler: bad cron expr %q for instance %q: %v", cfg.Schedule, cfg.Instance, err)
		return
	}
	sc.entries[cfg.Instance] = id
	log.Printf("scheduler: instance %q scheduled %q", cfg.Instance, cfg.Schedule)
}

// runOnce takes a backup, ships it, and applies retention to both local and
// remote. Designed to be safe to call concurrently per-instance (cron
// guarantees one fire at a time per entry); cross-instance is independent.
func (sc *Scheduler) runOnce(ctx context.Context, instance string) {
	cfg, err := sc.srv.loadBackupConfig(instance)
	if err != nil {
		log.Printf("scheduler[%s]: load config: %v", instance, err)
		return
	}
	if !cfg.Enabled {
		return
	}
	t0 := time.Now()
	bk, err := sc.srv.backupPostgresWithShipping(ctx, instance, cfg)
	if err != nil {
		log.Printf("scheduler[%s]: backup failed: %v", instance, err)
		return
	}
	log.Printf("scheduler[%s]: shipped %s (%d bytes, %s) in %s",
		instance, bk.Filename, bk.BytesSize, bk.RemoteURL, time.Since(t0).Round(100*time.Millisecond))
	if pruned, err := sc.srv.pruneBackups(ctx, instance, cfg); err != nil {
		log.Printf("scheduler[%s]: prune: %v", instance, err)
	} else if len(pruned) > 0 {
		log.Printf("scheduler[%s]: pruned %d backup(s): %v", instance, len(pruned), pruned)
	}
}

// pruneBackups applies retention to BOTH local and remote (so nothing drifts
// between them). Returns the list of filenames removed.
func (s *Server) pruneBackups(ctx context.Context, instance string, cfg *api.PostgresBackupConfig) ([]string, error) {
	// Union of local + remote names.
	names := map[string]struct{}{}
	if list, err := s.listPostgresBackups(instance); err == nil {
		for _, b := range list.Backups {
			names[b.Filename] = struct{}{}
		}
	}
	if cfg.Enabled {
		if remote, err := s.listRemoteBackups(ctx, cfg); err == nil {
			for n := range remote {
				names[n] = struct{}{}
			}
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	all := make([]string, 0, len(names))
	for n := range names {
		all = append(all, n)
	}
	keep := retentionDecision(all, cfg.RetentionDaily, cfg.RetentionWeekly, cfg.RetentionMonthly)
	var pruned []string
	for n := range names {
		if _, k := keep[n]; k {
			continue
		}
		// Local
		localPath := s.postgresBackupsDir(instance) + "/" + n
		if removeErr := tryRemove(localPath); removeErr == nil {
			// also try the .sha256 sidecar if any
			_ = tryRemove(localPath + ".sha256")
		}
		// Remote
		if cfg.Enabled {
			key := cfg.S3Prefix + n
			_ = s.deleteRemoteBackup(ctx, cfg, key)
			_ = s.deleteRemoteBackup(ctx, cfg, key+".sha256")
		}
		pruned = append(pruned, n)
	}
	sort.Strings(pruned)
	return pruned, nil
}

func tryRemove(path string) error {
	return removeIgnoringMissing(path)
}
