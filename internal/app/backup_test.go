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

	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"

	"github.com/asdfgh129/gobitmule/internal/core"
	"github.com/asdfgh129/gobitmule/internal/store"
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
	if m.SchemaVersion != 2 || m.Format != BackupFormat {
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
	if _, err := raw2.Exec(`UPDATE app_meta SET value='3' WHERE key='schema_version'`); err != nil {
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
	if rep.SchemaVersion != 2 || rep.RecoveredTasks != 1 {
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
	if !rep.AlreadyCurrent || rep.From != 2 || rep.To != 2 {
		t.Fatalf("report = %+v, want already-current v2", rep)
	}

	_, err = Migrate(ctx, live, 3)
	if err == nil {
		t.Fatal("--from 3 must refuse a v2 database")
	}
	if !strings.Contains(err.Error(), "--from 3") {
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

func TestBackupBundleExportsRedactedConfig(t *testing.T) {
	disableDaemonGuard(t)
	ctx := context.Background()
	dir := t.TempDir()
	live := filepath.Join(dir, "state.db")
	seedTaskStore(t, live)
	cfg := filepath.Join(dir, "config.yaml")
	content := "remoteToken: secret-token\nlisten: \"127.0.0.1:0\"\n"
	if err := os.WriteFile(cfg, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(dir, "bundle")
	m, err := BackupBundle(ctx, live, cfg, bundle)
	if err != nil {
		t.Fatalf("BackupBundle with config: %v", err)
	}
	if m.ConfigSource != cfg || len(m.Files) != 2 {
		t.Fatalf("manifest = %+v, want config + state.db", m)
	}
	if !m.Files[0].Redacted || m.Files[0].Name != "config.yaml" {
		t.Fatalf("config entry = %+v, want redacted config.yaml", m.Files[0])
	}
	raw, err := os.ReadFile(filepath.Join(bundle, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := yaml.Unmarshal(raw, &got); err != nil {
		t.Fatalf("exported config is not valid YAML: %v", err)
	}
	if got["remoteToken"] != "" {
		t.Fatalf("bearer token leaked into bundle: %q", got["remoteToken"])
	}
	if got["listen"] != "127.0.0.1:0" {
		t.Fatalf("other keys must survive redaction: %v", got)
	}
}

func TestBackupBundleCopiesUnparseableConfigVerbatim(t *testing.T) {
	disableDaemonGuard(t)
	ctx := context.Background()
	dir := t.TempDir()
	live := filepath.Join(dir, "state.db")
	seedTaskStore(t, live)
	cfg := filepath.Join(dir, "broken.yaml")
	content := "::not: [valid\n"
	if err := os.WriteFile(cfg, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(dir, "bundle")
	m, err := BackupBundle(ctx, live, cfg, bundle)
	if err != nil {
		t.Fatalf("BackupBundle must still document a broken config: %v", err)
	}
	if len(m.Files) != 2 || m.Files[0].Redacted {
		t.Fatalf("manifest = %+v, want verbatim unredacted config", m)
	}
	raw, err := os.ReadFile(filepath.Join(bundle, "broken.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != content {
		t.Fatalf("broken config changed: %q", raw)
	}
}

func TestBackupBundleMissingExplicitConfigIsIgnored(t *testing.T) {
	disableDaemonGuard(t)
	ctx := context.Background()
	dir := t.TempDir()
	live := filepath.Join(dir, "state.db")
	seedTaskStore(t, live)
	absent := filepath.Join(dir, "absent.yaml")
	bundle := filepath.Join(dir, "bundle")
	m, err := BackupBundle(ctx, live, absent, bundle)
	if err != nil {
		t.Fatalf("missing optional config must not fail backup: %v", err)
	}
	if m.ConfigSource != "" || len(m.Files) != 1 {
		t.Fatalf("manifest = %+v, want state.db only", m)
	}
}

func TestBackupBundleRejectsEmptyDestAndMissingSource(t *testing.T) {
	disableDaemonGuard(t)
	ctx := context.Background()
	if _, err := BackupBundle(ctx, "any.db", "", ""); err == nil {
		t.Fatal("empty destination must be refused")
	}
	dir := t.TempDir()
	if _, err := BackupBundle(ctx, filepath.Join(dir, "absent.db"), "", filepath.Join(dir, "bundle")); err == nil {
		t.Fatal("missing source database must be refused")
	}
}

func TestRestoreBundleRefusesBadManifests(t *testing.T) {
	disableDaemonGuard(t)
	ctx := context.Background()
	cases := []struct {
		name    string
		content string
		write   bool
	}{
		{"missing", "", false},
		{"invalid-json", "{", true},
		{"unsupported-format", `{"format":"other","files":["state.db"]}`, true},
		{"empty-files", `{"format":"ud-backup/1","files":[]}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.write {
				if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := RestoreBundle(ctx, dir, filepath.Join(dir, "state.db")); err == nil {
				t.Fatal("bad manifest must be refused")
			}
		})
	}
}

func TestDaemonGuardIgnoresMalformedAndUnhealthyDiscovery(t *testing.T) {
	old := discoveryProbePath
	t.Cleanup(func() { discoveryProbePath = old })
	path := filepath.Join(t.TempDir(), "daemon.json")
	discoveryProbePath = path

	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if running, _ := daemonRunning(); running {
		t.Fatal("malformed discovery file must not count as a running daemon")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	raw, _ := json.Marshal(Discovery{Addr: addr})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if running, gotAddr := daemonRunning(); running || gotAddr != addr {
		t.Fatalf("unhealthy daemon = %v/%q, want false/%q", running, gotAddr, addr)
	}

	if err := os.WriteFile(path, []byte(`{"addr":"127.0.0.1:1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if running, _ := daemonRunning(); running {
		t.Fatal("unreachable daemon must not block offline commands")
	}
}

func TestMigrateRefusesMissingDBAndRunningDaemon(t *testing.T) {
	disableDaemonGuard(t)
	ctx := context.Background()
	if _, err := Migrate(ctx, filepath.Join(t.TempDir(), "absent.db"), 0); err == nil {
		t.Fatal("migrating a missing database must fail")
	}

	dir := t.TempDir()
	live := filepath.Join(dir, "state.db")
	seedTaskStore(t, live)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	disc := filepath.Join(dir, "daemon.json")
	raw, _ := json.Marshal(Discovery{Addr: strings.TrimPrefix(srv.URL, "http://")})
	if err := os.WriteFile(disc, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	old := discoveryProbePath
	discoveryProbePath = disc
	t.Cleanup(func() { discoveryProbePath = old })
	if _, err := Migrate(ctx, live, 0); err == nil {
		t.Fatal("migrate must refuse while a daemon answers")
	}
}

func TestBackupHelpersRefuseMissingFiles(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent")
	if err := copyFile(missing, filepath.Join(dir, "out")); err == nil {
		t.Fatal("copyFile from a missing source must fail")
	}
	if _, err := fileSHA256(missing); err == nil {
		t.Fatal("fileSHA256 on a missing file must fail")
	}
}
