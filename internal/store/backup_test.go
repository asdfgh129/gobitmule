package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asdfgh129/ud/internal/core"
)

// BackupFile must work while the daemon-equivalent store stays open: WAL
// gives the second connection a consistent view, and VACUUM INTO must not
// disturb the source (docs/13 §11).
func TestBackupFileWhileStoreOpen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	live := filepath.Join(dir, "live.db")
	st, err := Open(live, core.NewEventBus(16))
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

	snapshot := filepath.Join(dir, "snapshot.db")
	if err := BackupFile(ctx, live, snapshot); err != nil {
		t.Fatalf("BackupFile with a live store: %v", err)
	}
	v, refs, err := SnapshotMeta(snapshot)
	if err != nil {
		t.Fatalf("SnapshotMeta: %v", err)
	}
	if v != CurrentSchemaVersion() {
		t.Fatalf("snapshot schema v%d, want v%d", v, CurrentSchemaVersion())
	}
	if len(refs) != 0 {
		t.Fatalf("refs = %v, want none", refs)
	}
	// The live store must still work after the snapshot.
	if err := st.SetMeta(ctx, "probe", "ok"); err != nil {
		t.Fatalf("live store unusable after BackupFile: %v", err)
	}
}

func TestBackupFileRefusesMissingSource(t *testing.T) {
	dir := t.TempDir()
	err := BackupFile(context.Background(), filepath.Join(dir, "nope.db"), filepath.Join(dir, "out.db"))
	if err == nil {
		t.Fatal("backup of a missing source must fail instead of creating an empty snapshot")
	}
	if _, err := os.Stat(filepath.Join(dir, "out.db")); !os.IsNotExist(err) {
		t.Fatal("a failed backup must not leave a snapshot behind")
	}
}

func TestBackupFileRefusesSelfAndDirectoryDest(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	live := filepath.Join(dir, "live.db")
	st, err := Open(live, core.NewEventBus(8))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := BackupFile(ctx, live, live); err == nil {
		t.Fatal("backing a database onto itself must fail")
	}
	if err := BackupFile(ctx, live, dir); err == nil {
		t.Fatal("a directory destination must be refused")
	}
}

func TestBackupFileRemovesStaleDestFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	live := filepath.Join(dir, "live.db")
	st, err := Open(live, core.NewEventBus(8))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dest := filepath.Join(dir, "snap.db")
	if err := os.WriteFile(dest, []byte("stale garbage from an earlier failed run"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := BackupFile(ctx, live, dest); err != nil {
		t.Fatalf("BackupFile over a stale dest file: %v", err)
	}
	v, _, err := SnapshotMeta(dest)
	if err != nil || v != CurrentSchemaVersion() {
		t.Fatalf("stale dest not replaced: v=%d err=%v", v, err)
	}
}

// The ref manifest must cover every column a credential ref can live in,
// deduplicated and ordered (docs/13 §11: 钥匙串引用清单).
func TestSnapshotMetaCollectsCredentialRefs(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	live := filepath.Join(dir, "live.db")
	st, err := Open(live, core.NewEventBus(16))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id := core.NewID()
	now := time.Now().UnixMilli()
	if err := st.InsertTask(ctx, &core.Task{
		ID: id, State: core.StateRunning, Input: "magnet:?xt=urn:btih:x",
		InputKind: core.InputMagnet, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	res := &core.Resource{
		ID:   "res-" + string(id),
		Kind: core.KindTorrent,
		// Credential lands in resources.cred_ref; the BT identity refs land in
		// protocol_identity.{passkey,ident_key,credits}_ref.
		Credential: "dpapi://ud/cred/aa",
		Identity: core.ProtocolIdentity{
			Private:     true,
			PasskeyRef:  "keychain://ud/cred/bb",
			IdentKeyRef: "secret://ud/cred/cc",
			CreditsRef:  "dpapi://ud/cred/aa", // duplicate on purpose
		},
		Files: []core.ResourceFile{{Index: 0, Path: "x.bin", Size: 1, Selected: true}},
	}
	if err := st.SaveResource(ctx, id, res); err != nil {
		t.Fatal(err)
	}

	snapshot := filepath.Join(dir, "snap.db")
	if err := BackupFile(ctx, live, snapshot); err != nil {
		t.Fatal(err)
	}
	_, refs, err := SnapshotMeta(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dpapi://ud/cred/aa", "keychain://ud/cred/bb", "secret://ud/cred/cc"}
	if len(refs) != len(want) {
		t.Fatalf("refs = %v, want %v", refs, want)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Fatalf("refs = %v, want %v", refs, want)
		}
	}
}

func TestSnapshotMetaRejectsNonSQLiteFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "garbage.db")
	if err := os.WriteFile(p, []byte("this is definitely not a SQLite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SnapshotMeta(p); err == nil {
		t.Fatal("SnapshotMeta on a non-SQLite file must fail")
	}
}

func TestCurrentSchemaVersionMatchesOpen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "a.db"), core.NewEventBus(8))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	v, err := st.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v != CurrentSchemaVersion() {
		t.Fatalf("CurrentSchemaVersion = %d, fresh store reports %d", CurrentSchemaVersion(), v)
	}
}
