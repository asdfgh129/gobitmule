package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/asdfgh129/ud/internal/core"
)

// InsertEvent appends a redacted audit event (docs/11 §3).
func (s *Store) InsertEvent(ctx context.Context, e core.Event) error {
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO events(task_id,type,payload,ts) VALUES(?,?,?,?)`,
			nullable(string(e.TaskID)), string(e.Type), string(payload), e.Ts)
		return err
	})
}

// PruneResult reports one retention sweep (docs/06 §7). The two counters are
// kept apart on purpose: a sweep that only hits the row cap must not be read
// as "old events were dropped", and vice versa.
type PruneResult struct {
	DeletedByAge   int64
	DeletedByCount int64
	Remaining      int64
}

// PruneEvents enforces the audit-log retention policy in a single transaction:
// events older than beforeTS are dropped, then everything but the newest
// maxRows rows is dropped. A zero beforeTS or maxRows disables that half of the
// policy (config validation refuses both being zero).
//
// Both deletes share one transaction so a concurrent reader never sees a
// half-pruned log, and a failure on the row cap cannot leave the age sweep
// applied-but-unreported. Cost is bounded by the idx_events_ts index plus the
// primary key, so a 100k-row table sweeps without a full scan.
func (s *Store) PruneEvents(ctx context.Context, beforeTS int64, maxRows int) (PruneResult, error) {
	var res PruneResult
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		res = PruneResult{}
		if beforeTS > 0 {
			r, err := tx.ExecContext(ctx, `DELETE FROM events WHERE ts < ?`, beforeTS)
			if err != nil {
				return err
			}
			res.DeletedByAge, _ = r.RowsAffected()
		}
		if maxRows > 0 {
			// Find the id that must survive as the oldest of the newest
			// maxRows rows, then delete everything strictly older. OFFSET
			// keeps this O(maxRows) instead of a NOT IN re-scan.
			var keepID int64
			err := tx.QueryRowContext(ctx,
				`SELECT id FROM events ORDER BY id DESC LIMIT 1 OFFSET ?`, maxRows-1).Scan(&keepID)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				// Fewer than maxRows rows remain: nothing to cap.
			case err != nil:
				return err
			default:
				r, err := tx.ExecContext(ctx, `DELETE FROM events WHERE id < ?`, keepID)
				if err != nil {
					return err
				}
				res.DeletedByCount, _ = r.RowsAffected()
			}
		}
		return tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&res.Remaining)
	})
	return res, err
}

// Events returns recent audit events, newest last.
func (s *Store) Events(ctx context.Context, limit int) ([]core.Event, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(task_id,''),type,payload,ts FROM
		(SELECT * FROM events ORDER BY id DESC LIMIT ?) ORDER BY id ASC`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Event
	for rows.Next() {
		var e core.Event
		var taskID, payload string
		if err := rows.Scan(&taskID, &e.Type, &payload, &e.Ts); err != nil {
			return nil, err
		}
		e.TaskID = core.TaskID(taskID)
		_ = json.Unmarshal([]byte(payload), &e.Payload)
		out = append(out, e)
	}
	return out, rows.Err()
}
