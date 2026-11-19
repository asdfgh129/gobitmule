package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/asdfgh129/ud/internal/core"
	"github.com/asdfgh129/ud/internal/store"
)

// seedTaskStore builds a live store with one task, its resource and two
// chunks whose first half matches the on-disk .part bytes (mirroring
// TestBackupRestoreRollbackDrill in internal/store).
func seedTaskStore(t *testing.T, dbPath string) (core.TaskID, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(dbPath, core.NewEventBus(64))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id := core.NewID()
	now := time.Now().UnixMilli()
	if err := st.InsertTask(ctx, &core.Task{
		ID: id, State: core.StateRunning, Input: "https://x/a.bin",
		InputKind: core.InputURL, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	res := &core.Resource{ID: "res-" + string(id), Kind: core.KindSingle,
		Credential: "dpapi://ud/cred/abcd1234abcd1234abcd1234abcd1234",
		Files:      []core.ResourceFile{{Index: 0, Path: "a.bin", Size: 4, Selected: true}}}
	if err := st.SaveResource(ctx, id, res); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateChunks(ctx, id, 0, 4, 2); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(t.TempDir(), "a.bin.part")
	if err := st.SetOption(ctx, id, "temp_path", part); err != nil {
		t.Fatal(err)
	}
	c0, _, _ := st.Next(ctx, id)
	c1, _, _ := st.Next(ctx, id)
	sum0 := sha256.Sum256([]byte("AB"))
	sum1 := sha256.Sum256([]byte("ZZ")) // deliberately wrong for the on-disk bytes
	_ = st.WriteChunkDone(ctx, c0, res.ID, hex.EncodeToString(sum0[:]), 0)
	_ = st.WriteChunkDone(ctx, c1, res.ID, hex.EncodeToString(sum1[:]), 0)
	if err := os.WriteFile(part, []byte("ABCD"), 0o600); err != nil {
		t.Fatal(err)
	}
	return id, part
}

// disableDaemonGuard points the offline guard at a temp path that does not
// exist, so no real daemon discovery file can interfere with a test.
func disableDaemonGuard(t *testing.T) {
	t.Helper()
	old := discoveryProbePath
	discoveryProbePath = filepath.Join(t.TempDir(), "absent-daemon.json")
	t.Cleanup(func() { discoveryProbePath = old })
}

func TestBackupRestoreBundleRoundTrip(t *testing.T) {
	disableDaemonGuard(t)
	ctx := context.Background()
	dir := t.TempDir()
	live := filepath.Join(dir, "state.db")
	id, _ := seedTaskStore(t, live)

	bundle := filepath.Join(dir, "bundle")
	m, err := BackupBundle(ctx, live, "", bundle)
	if err != nil {
		t.Fatalf("BackupBundle: %v", err)
	}
	if m.SchemaVersion != 1 || m.Format != BackupFormat {
		t.Fatalf("manifest = %+v", m)
	}
	if len(m.KeychainRefs) != 1 || m.KeychainRefs[0] != "dpapi://ud/cred/abcd1234abcd1234abcd1234abcd1234" {
		t.Fatalf("keychain refs = %v", m.KeychainRefs)
	}
	if _, err := os.Stat(filepath.Join(bundle, "keychain_refs.txt")); err != nil {
		t.Fatalf("refs file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bundle, "state.db")); err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}

	// Bad upgrade on the live database: schema bumped, task cancelled, chunk
	// progress wiped — exactly the disaster the rollback path must survive.
	// Raw SQL through a second connection mirrors what a broken upgrade does;
	// app cannot reach store internals from outside the package.
	st, err := store.Open(live, core.NewEventBus(64))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateState(ctx, id, store.StateUpdate{To: core.StateCanceled}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	raw2, err := sql.Open("sqlite", "file:"+live+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw2.Exec(`UPDATE app_meta SET value='2' WHERE key='schema_version'`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw2.Exec(`UPDATE chunks SET state='pending'`); err != nil {
		t.Fatal(err)
	}
	raw2.Close()

	rep, err := RestoreBundle(ctx, bundle, live)
	if err != nil {
		t.Fatalf("RestoreBundle: %v", err)
	}
	if rep.SchemaVersion != 1 || rep.RecoveredTasks != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if rep.PreRestoreBackup == "" {
		t.Fatal("restore must snapshot the pre-restore database")
	}
	if _, err := os.Stat(rep.PreRestoreBackup); err != nil {
		t.Fatalf("pre-restore snapshot missing: %v", err)
	}
	// Crash recovery semantics: only the chunk whose bytes still hash to the
	// recorded digest survives (docs/06 §5).
	if rep.ChunksTotal != 2 || rep.ChunksVerified != 1 {
		t.Fatalf("chunks total=%d verified=%d, want 2/1", rep.ChunksTotal, rep.ChunksVerified)
	}
	if _, err := os.Stat(live + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("stale WAL sidecar survived restore: %v", err)
	}
}

func TestBackupBundleRefusesExistingDest(t *testing.T) {
	disableDaemonGuard(t)
	ctx := context.Background()
	dir := t.TempDir()
	live := filepath.Join(dir, "state.db")
	seedTaskStore(t, live)
	bundle := filepath.Join(dir, "bundle")
	if err := os.MkdirAll(bundle, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "operator-file.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BackupBundle(ctx, live, "", bundle); err == nil {
		t.Fatal("backup into an existing directory must be refused")
	}
}

func TestRestoreBundleVerifiesPayloadChecksums(t *testing.T) {
	disableDaemonGuard(t)
	ctx := context.Background()
	dir := t.TempDir()
	live := filepath.Join(dir, "state.db")
	seedTaskStore(t, live)
	bundle := filepath.Join(dir, "bundle")
	if _, err := BackupBundle(ctx, live, "", bundle); err != nil {
		t.Fatal(err)
	}
	// Flip one byte inside the snapshot.
	raw, err := os.ReadFile(filepath.Join(bundle, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 0xFF
	if err := os.WriteFile(filepath.Join(bundle, "state.db"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreBundle(ctx, bundle, live); err == nil {
		t.Fatal("restore must refuse a corrupted snapshot")
	}
}

func TestRestoreBundleRefusesNewerSchema(t *testing.T) {
	disableDaemonGuard(t)
	ctx := context.Background()
	dir := t.TempDir()
	live := filepath.Join(dir, "state.db")
	seedTaskStore(t, live)
	bundle := filepath.Join(dir, "bundle")
	if _, err := BackupBundle(ctx, live, "", bundle); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(bundle, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["schemaVersion"] = float64(99)
	out, _ := json.Marshal(m)
	if err := os.WriteFile(manifestPath, out, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = RestoreBundle(ctx, bundle, live)
	if err == nil {
		t.Fatal("a bundle from a newer schema must be refused with guidance")
	}
	if !strings.Contains(err.Error(), "docs/13") {
		t.Fatalf("refusal must point at the rollback runbook: %v", err)
	}
}

func TestRestoreBundleRefusesRunningDaemon(t *testing.T) {
	// A real /healthz endpoint behind a temp discovery file — the same guard
	// the daemon's own discovery file feeds on a real host.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	disc := filepath.Join(t.TempDir(), "daemon.json")
	addr := strings.TrimPrefix(srv.URL, "http://")
	raw, _ := json.Marshal(Discovery{Addr: addr, PID: os.Getpid()})
	if err := os.WriteFile(disc, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	old := discoveryProbePath
	discoveryProbePath = disc
	t.Cleanup(func() { discoveryProbePath = old })

	dir := t.TempDir()
	live := filepath.Join(dir, "state.db")
	seedTaskStore(t, live)
	bundle := filepath.Join(dir, "bundle")
	if _, err := BackupBundle(context.Background(), live, "", bundle); err != nil {
		t.Fatal(err)
	}
	_, err := RestoreBundle(context.Background(), bundle, live)
	if err == nil {
		t.Fatal("restore must refuse while a daemon answers on the discovery address")
	}
	if !strings.Contains(err.Error(), "停止") {
		t.Fatalf("refusal must tell the operator to stop the daemon: %v", err)
	}
}

func TestMigrateAlreadyCurrentAndFromMismatch(t *testing.T) {
	disableDaemonGuard(t)
	ctx := context.Background()
	dir := t.TempDir()
	live := filepath.Join(dir, "state.db")
	seedTaskStore(t, live)

	rep, err := Migrate(ctx, live, 0)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if !rep.AlreadyCurrent || rep.From != 1 || rep.To != 1 {
		t.Fatalf("report = %+v, want already-current v1", rep)
	}

	_, err = Migrate(ctx, live, 2)
	if err == nil {
		t.Fatal("--from 2 must refuse a v1 database")
	}
	if !strings.Contains(err.Error(), "--from 2") {
		t.Fatalf("refusal must name the mismatch: %v", err)
	}

	// A database written by a NEWER binary is refused before any write.
	db, err := sql.Open("sqlite", "file:"+live+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE app_meta SET value='9' WHERE key='schema_version'`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	_, err = Migrate(ctx, live, 0)
	if err == nil {
		t.Fatal("migrating a future-schema database must fail closed")
	}
}

func TestDefaultDBPathNonEmpty(t *testing.T) {
	if DefaultDBPath() == "" || !strings.HasSuffix(DefaultDBPath(), "state.db") {
		t.Fatalf("DefaultDBPath = %q", DefaultDBPath())
	}
}
