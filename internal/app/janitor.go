package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/asdfgh129/gobitmule/internal/config"
	"github.com/asdfgh129/gobitmule/internal/store"
)

// eventJanitor enforces the audit-log retention policy from docs/06 §7
// ("events 保留策略可配（默认 7 天 / 10 万条），定期清理").
//
// It is deliberately non-fatal: a failed sweep is logged and retried on the
// next tick. Dropping audit rows is worse than failing a request; failing the
// daemon because the audit log could not be trimmed would be worse still, and
// the row cap still bounds growth in the meantime.
type eventJanitor struct {
	st       *store.Store
	cfg      config.EventsConfig
	log      *slog.Logger
	now      func() time.Time
	interval time.Duration
}

func newEventJanitor(st *store.Store, cfg config.EventsConfig, log *slog.Logger) *eventJanitor {
	return &eventJanitor{
		st: st, cfg: cfg, log: log,
		now:      time.Now,
		interval: cfg.EffectivePruneInterval(),
	}
}

// sweep runs exactly one retention pass. Exposed (not inlined into run) so a
// test can assert the policy without waiting for a tick.
func (j *eventJanitor) sweep(ctx context.Context) (store.PruneResult, error) {
	cutoff := j.cfg.CutoffMillis(j.now())
	res, err := j.st.PruneEvents(ctx, cutoff, j.cfg.MaxRows)
	if err != nil {
		return res, err
	}
	if res.DeletedByAge > 0 || res.DeletedByCount > 0 {
		j.log.Info("events pruned",
			"deletedByAge", res.DeletedByAge, "deletedByCount", res.DeletedByCount,
			"remaining", res.Remaining, "retentionDays", j.cfg.RetentionDays,
			"maxRows", j.cfg.MaxRows)
	}
	return res, nil
}

// run sweeps once immediately (so a long-idle install is trimmed at startup
// rather than one interval later), then on every tick until ctx is cancelled.
func (j *eventJanitor) run(ctx context.Context) {
	j.sweepLogged(ctx)
	t := time.NewTicker(j.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			j.sweepLogged(ctx)
		}
	}
}

func (j *eventJanitor) sweepLogged(ctx context.Context) {
	if _, err := j.sweep(ctx); err != nil && ctx.Err() == nil {
		j.log.Warn("events prune failed; will retry on next tick", "error", err)
	}
}
