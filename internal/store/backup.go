package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Offline backup / snapshot primitives for `udctl backup|restore|migrate`
// (docs/13 §8 升级与迁移, §11 备份与灾难恢复). Unlike Store.Backup — which
// runs on the daemon's own connection — these helpers open a SECOND
// connection, so the daemon can keep serving while an operator takes a
// consistent snapshot: SQLite WAL gives every reader a stable view of the
// last committed transaction, and VACUUM INTO copies it out without touching
// the source file.

// CurrentSchemaVersion is the DDL version a freshly migrated database gets
// (docs/06 §3). `udctl restore` / `udctl migrate` use it to reject bundles or
// databases written by a NEWER binary instead of failing halfway through.
func CurrentSchemaVersion() int { return currentSchemaVersion }

var currentSchemaVersion = countMigrations()

func countMigrations() int {
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			n++
		}
	}
	return n
}

// BackupFile snapshots srcPath into destPath. destPath must not exist —
// SQLite's VACUUM INTO refuses to overwrite, and silently replacing an
// operator-provided path is exactly the kind of surprise a backup tool must
// never produce. A stale dest from an earlier failed run is removed first.
func BackupFile(ctx context.Context, srcPath, destPath string) error {
	if srcPath == "" || destPath == "" {
		return fmt.Errorf("backup: src and dest paths are required")
	}
	// Refuse to invent a database: opening a missing source read-write would
	// silently CREATE an empty SQLite file and "back it up" successfully.
	if _, err := os.Stat(srcPath); err != nil {
		return fmt.Errorf("backup: source database %s: %w", srcPath, err)
	}
	if abs, err := absSameFile(srcPath, destPath); err != nil {
		return err
	} else if abs {
		return fmt.Errorf("backup: dest %q is the source database", destPath)
	}
	// VACUUM INTO fails if the target exists; remove only a plain leftover
	// file, never a directory that would swallow a user's data.
	if info, err := os.Stat(destPath); err == nil {
		if info.IsDir() {
			return fmt.Errorf("backup: dest %q exists and is a directory", destPath)
		}
		if err := os.Remove(destPath); err != nil {
			return fmt.Errorf("backup: remove stale dest: %w", err)
		}
	}
	db, err := openOffline(srcPath, false)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, destPath); err != nil {
		return fmt.Errorf("backup: vacuum into %s: %w", destPath, err)
	}
	return nil
}

// absSameFile reports whether two paths denote the same existing file, so a
// `udctl backup state.db state.db` cannot VACUUM INTO its own source.
func absSameFile(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, nil // source checked elsewhere; nothing to compare
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false, nil
	}
	return os.SameFile(ai, bi), nil
}

// SnapshotMeta opens a snapshot (or any ud database) READ-ONLY and returns its
// schema_version plus the distinct keychain credential refs recorded in
// resources.cred_ref and protocol_identity.{passkey,ident_key,credits}_ref.
// Refs only: the secrets themselves never leave the OS keychain
// (ADR-0015 ④, docs/13 §11), so the exported manifest can be archived or
// shipped without leaking credentials.
func SnapshotMeta(path string) (schemaVersion int, credRefs []string, err error) {
	db, err := openOffline(path, true)
	if err != nil {
		return 0, nil, err
	}
	defer db.Close()

	var v sql.NullInt64
	if err := db.QueryRowContext(context.Background(),
		`SELECT value FROM app_meta WHERE key='schema_version'`).Scan(&v); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil, fmt.Errorf("snapshot %s has no schema_version (not a ud database?)", path)
		}
		return 0, nil, fmt.Errorf("read schema_version: %w", err)
	}
	schemaVersion = int(v.Int64)

	rows, err := db.QueryContext(context.Background(), `
		SELECT DISTINCT ref FROM (
			SELECT cred_ref AS ref FROM resources WHERE cred_ref <> ''
			UNION
			SELECT passkey_ref FROM protocol_identity WHERE passkey_ref <> ''
			UNION
			SELECT ident_key_ref FROM protocol_identity WHERE ident_key_ref <> ''
			UNION
			SELECT credits_ref FROM protocol_identity WHERE credits_ref <> ''
		) ORDER BY ref`)
	if err != nil {
		return 0, nil, fmt.Errorf("collect credential refs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return 0, nil, err
		}
		credRefs = append(credRefs, ref)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	sort.Strings(credRefs)
	return schemaVersion, credRefs, nil
}

// openOffline opens a second, minimal connection to a ud database. mode
// read-only additionally refuses any accidental write from a snapshot reader.
func openOffline(path string, readOnly bool) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("open offline: empty database path")
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(10000)"
	if readOnly {
		dsn += "&mode=ro&_pragma=query_only(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open offline %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	// Fail fast on a non-SQLite file (a config or a truncated db) instead of
	// letting the first query return a confusing "no such table".
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("open offline %s: not a usable SQLite database: %w", path, err)
	}
	return db, nil
}
