package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/asdfgh129/ud/internal/core"
	"github.com/asdfgh129/ud/internal/store"
	"github.com/asdfgh129/ud/internal/version"
)

// discoveryProbePath overrides the discovery file the offline guard probes.
// Empty means the real per-user discovery file; tests point it at a temp file.
var discoveryProbePath string

// Backup / restore / migrate implement docs/13 §8 (升级与迁移) and §11
// (备份与灾难恢复) for `udctl`: back up the SQLite state plus the config and
// the keychain reference manifest; restore validates the bundle, replaces the
// database and rebuilds chunk state against the real .part bytes; migrate is
// the explicit operator hook for schema-version transitions.
//
// These commands are deliberately OFFLINE: restore and migrate replace or
// rewrite the database file, which a running daemon keeps open (and which
// Windows does not allow replacing at all). Every entry point therefore
// refuses to run while a daemon answers on the local discovery address.

// BackupFormat is the manifest schema marker. Bump it whenever the bundle
// layout changes in a way older binaries cannot read.
const BackupFormat = "ud-backup/1"

// BackupManifest describes one bundle. SHA-256 over every payload file makes
// a corrupted download detectable before anything replaces the live database.
type BackupManifest struct {
	Format        string       `json:"format"`
	CreatedAt     int64        `json:"createdAt"`
	AppVersion    string       `json:"appVersion"`
	SchemaVersion int          `json:"schemaVersion"`
	Files         []BackupFile `json:"files"`
	KeychainRefs  []string     `json:"keychainRefs"`
	SourceDB      string       `json:"sourceDb"`
	ConfigSource  string       `json:"configSource,omitempty"`
}

// BackupFile is one payload file with its integrity fields.
type BackupFile struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
	// Redacted marks a config copy whose remoteToken was removed (a bundle
	// must never carry a bearer token, docs/11 §6 redaction posture).
	Redacted bool `json:"redacted,omitempty"`
}

// RestoreReport is what `udctl restore` prints after rebuilding chunk state.
type RestoreReport struct {
	SchemaVersion    int      `json:"schemaVersion"`
	PreRestoreBackup string   `json:"preRestoreBackup,omitempty"`
	RecoveredTasks   int      `json:"recoveredTasks"`
	ChunksTotal      int      `json:"chunksTotal"`
	ChunksVerified   int      `json:"chunksVerified"`
	KeychainRefs     []string `json:"keychainRefs"`
}

// MigrateReport is what `udctl migrate` prints.
type MigrateReport struct {
	From           int      `json:"from"`
	To             int      `json:"to"`
	Applied        []string `json:"applied,omitempty"`
	AlreadyCurrent bool     `json:"alreadyCurrent"`
}

// DefaultDBPath mirrors the daemon's default database location
// (internal/app/daemon.go): the per-user config directory, with a TempDir
// fallback for exotic hosts.
func DefaultDBPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "ud", "state.db")
}

// BackupBundle writes a backup bundle into dest (which must not exist yet, so
// an operator can never silently overwrite an earlier bundle):
//
//	dest/state.db            consistent SQLite snapshot (VACUUM INTO)
//	dest/config.yaml         resolved config, remoteToken removed if parseable
//	dest/keychain_refs.txt   distinct credential refs (refs only, no secrets)
//	dest/manifest.json       metadata + SHA-256 over every payload file
//
// The daemon may keep running during a backup: SQLite WAL gives the second
// connection a consistent view (internal/store.BackupFile).
func BackupBundle(ctx context.Context, dbPath, configPath, dest string) (*BackupManifest, error) {
	if dest == "" {
		return nil, fmt.Errorf("backup: destination directory is required")
	}
	if _, err := os.Stat(dest); err == nil {
		return nil, fmt.Errorf("backup: destination %s already exists; choose a new directory", dest)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("backup: stat destination: %w", err)
	}
	if dbPath == "" {
		dbPath = DefaultDBPath()
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("backup: source database %s: %w", dbPath, err)
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return nil, fmt.Errorf("backup: create destination: %w", err)
	}

	snapshot := filepath.Join(dest, "state.db")
	if err := store.BackupFile(ctx, dbPath, snapshot); err != nil {
		return nil, err
	}
	schemaVersion, credRefs, err := store.SnapshotMeta(snapshot)
	if err != nil {
		return nil, err
	}

	m := &BackupManifest{
		Format:        BackupFormat,
		CreatedAt:     time.Now().UnixMilli(),
		AppVersion:    version.String(),
		SchemaVersion: schemaVersion,
		KeychainRefs:  credRefs,
		SourceDB:      dbPath,
	}

	if cfgSource := resolveBackupConfig(configPath); cfgSource != "" {
		name, redacted, err := copyBackupConfig(dest, cfgSource)
		if err != nil {
			return nil, err
		}
		m.ConfigSource = cfgSource
		info, err := os.Stat(filepath.Join(dest, name))
		if err != nil {
			return nil, err
		}
		sum, err := fileSHA256(filepath.Join(dest, name))
		if err != nil {
			return nil, err
		}
		m.Files = append(m.Files, BackupFile{Name: name, Bytes: info.Size(), SHA256: sum, Redacted: redacted})
	}

	if len(credRefs) > 0 {
		refsFile := filepath.Join(dest, "keychain_refs.txt")
		var body []byte
		for _, ref := range credRefs {
			body = append(body, []byte(ref)...)
			body = append(body, '\n')
		}
		if err := os.WriteFile(refsFile, body, 0o600); err != nil {
			return nil, fmt.Errorf("backup: write keychain refs: %w", err)
		}
	}

	dbInfo, err := os.Stat(snapshot)
	if err != nil {
		return nil, err
	}
	dbSum, err := fileSHA256(snapshot)
	if err != nil {
		return nil, err
	}
	m.Files = append(m.Files, BackupFile{Name: "state.db", Bytes: dbInfo.Size(), SHA256: dbSum})

	// manifest.json is written last and carries no checksum of itself.
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dest, "manifest.json"), raw, 0o600); err != nil {
		return nil, fmt.Errorf("backup: write manifest: %w", err)
	}
	return m, nil
}

// resolveBackupConfig picks the config to export: the explicit flag wins,
// then the daemon's ./config.yaml convention. An absent config exports an
// empty manifest entry — the daemon runs fine on defaults.
func resolveBackupConfig(configPath string) string {
	if configPath != "" {
		if _, err := os.Stat(configPath); err != nil {
			return ""
		}
		return configPath
	}
	const candidate = "config.yaml"
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

// copyBackupConfig copies the config into the bundle. When the file parses as
// YAML, remoteToken is stripped first: the config copy travels with the
// bundle and must not carry a bearer token. A file that does not parse is
// copied verbatim (a broken config still documents the deployment).
func copyBackupConfig(dest, source string) (name string, redacted bool, err error) {
	raw, err := os.ReadFile(source)
	if err != nil {
		return "", false, fmt.Errorf("backup: read config: %w", err)
	}
	var cfg struct {
		RemoteToken string `yaml:"remoteToken"`
	}
	name = filepath.Base(source)
	out := raw
	if yaml.Unmarshal(raw, &cfg) == nil && cfg.RemoteToken != "" {
		var full map[string]any
		if yaml.Unmarshal(raw, &full) == nil {
			if _, ok := full["remoteToken"]; ok {
				full["remoteToken"] = ""
				if b, err := yaml.Marshal(full); err == nil {
					out = b
					redacted = true
				}
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dest, name), out, 0o600); err != nil {
		return "", false, fmt.Errorf("backup: write config: %w", err)
	}
	return name, redacted, nil
}

// RestoreBundle replaces the live database with the bundle's snapshot and
// rebuilds chunk state against the real .part bytes (docs/13 §11: 恢复时校验
// schema_version、重建分片状态、逐片校验 .part). Steps:
//
//  1. refuse while a daemon answers on the discovery address;
//  2. verify the manifest and every payload SHA-256;
//  3. refuse a bundle written by a NEWER schema than this binary supports
//     (no rollback script exists — fail closed with guidance, docs/13 §8.4);
//  4. take a safety snapshot of the current live database;
//  5. replace state.db (removing stale WAL/SHM sidecars first);
//  6. reopen the store and run crash recovery, which resets non-verified
//     chunks and re-hashes the .part bytes.
func RestoreBundle(ctx context.Context, bundleDir, liveDBPath string) (*RestoreReport, error) {
	if liveDBPath == "" {
		liveDBPath = DefaultDBPath()
	}
	if running, addr := daemonRunning(); running {
		return nil, fmt.Errorf("daemon 正在运行 (%s)：恢复前必须先停止 udd（docs/13 §8.5）", addr)
	}
	manifest, err := readBackupManifest(bundleDir)
	if err != nil {
		return nil, err
	}
	if manifest.SchemaVersion > store.CurrentSchemaVersion() {
		return nil, fmt.Errorf(
			"备份 schema v%d 高于本二进制支持的 v%d：请改用对应版本的 udd（回滚前先备份，见 docs/13 §8.5）",
			manifest.SchemaVersion, store.CurrentSchemaVersion())
	}
	for _, f := range manifest.Files {
		p := filepath.Join(bundleDir, f.Name)
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("备份缺少文件 %s: %w", f.Name, err)
		}
		if info.Size() != f.Bytes {
			return nil, fmt.Errorf("备份文件 %s 大小不符（%d ≠ %d）", f.Name, info.Size(), f.Bytes)
		}
		sum, err := fileSHA256(p)
		if err != nil {
			return nil, err
		}
		if sum != f.SHA256 {
			return nil, fmt.Errorf("备份文件 %s 校验失败：SHA-256 不符，备份已损坏", f.Name)
		}
	}
	snapshotVersion, credRefs, err := store.SnapshotMeta(filepath.Join(bundleDir, "state.db"))
	if err != nil {
		return nil, err
	}
	if snapshotVersion != manifest.SchemaVersion {
		return nil, fmt.Errorf("备份自述 schema v%d，实际 v%d：manifest 与数据不一致",
			manifest.SchemaVersion, snapshotVersion)
	}

	report := &RestoreReport{
		SchemaVersion: snapshotVersion,
		KeychainRefs:  credRefs,
	}

	// Safety net: snapshot the current live database before it is replaced.
	if _, err := os.Stat(liveDBPath); err == nil {
		pre := liveDBPath + ".pre-restore." + time.Now().Format("20060102-150405")
		if err := store.BackupFile(ctx, liveDBPath, pre); err != nil {
			return nil, fmt.Errorf("restore: 无法为现有数据库建立安全快照: %w", err)
		}
		report.PreRestoreBackup = pre
	}

	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(liveDBPath + suffix)
	}
	if err := os.MkdirAll(filepath.Dir(liveDBPath), 0o700); err != nil {
		return nil, err
	}
	if err := copyFile(filepath.Join(bundleDir, "state.db"), liveDBPath); err != nil {
		return nil, fmt.Errorf("restore: 写入恢复数据库: %w", err)
	}

	// Rebuild chunk state: Recover pauses in-flight tasks, resets non-finished
	// chunks and re-hashes the .part bytes so only verified progress survives.
	st, err := store.Open(liveDBPath, core.NewEventBus(64))
	if err != nil {
		return nil, fmt.Errorf("restore: 打开恢复后的数据库: %w", err)
	}
	defer st.Close()
	ids, err := st.Recover(ctx)
	if err != nil {
		return nil, fmt.Errorf("restore: 重建分片状态: %w", err)
	}
	report.RecoveredTasks = len(ids)
	for _, id := range ids {
		chs, err := st.Chunks(ctx, id)
		if err != nil {
			continue
		}
		report.ChunksTotal += len(chs)
		// Chunks that survived the re-hash keep their Done/Verified state —
		// that is "逐片校验 .part" reusing real progress (docs/06 §5).
		for _, c := range chs {
			if c.State == core.ChunkDone || c.State == core.ChunkVerified {
				report.ChunksVerified++
			}
		}
	}
	return report, nil
}

// Migrate applies pending schema migrations to dbPath. With --from N the
// command refuses unless the database is exactly at version N — the explicit
// intent check docs/13 §8.4 requires of an operator mid-upgrade. Like the
// daemon's own startup, migrations are idempotent; "already current" is the
// normal no-op outcome on today's DDL v1.
func Migrate(ctx context.Context, dbPath string, from int) (*MigrateReport, error) {
	if dbPath == "" {
		dbPath = DefaultDBPath()
	}
	if running, addr := daemonRunning(); running {
		return nil, fmt.Errorf("daemon 正在运行 (%s)：迁移前必须先停止 udd（docs/13 §8.4）", addr)
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("migrate: 数据库 %s 不存在: %w", dbPath, err)
	}
	current, _, err := store.SnapshotMeta(dbPath)
	if err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if current > store.CurrentSchemaVersion() {
		return nil, fmt.Errorf(
			"数据库 schema v%d 高于本二进制支持的 v%d：拒绝启动式升级，请改用匹配版本的 udd（docs/13 §8.4）",
			current, store.CurrentSchemaVersion())
	}
	if from != 0 && current != from {
		return nil, fmt.Errorf("migrate: --from %d 与实际 schema v%d 不符", from, current)
	}

	report := &MigrateReport{From: current}
	if current == store.CurrentSchemaVersion() {
		report.To = current
		report.AlreadyCurrent = true
		return report, nil
	}
	// Opening the store applies every pending migration idempotently — the
	// same path the daemon takes on startup.
	st, err := store.Open(dbPath, core.NewEventBus(64))
	if err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	defer st.Close()
	to, err := st.SchemaVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("migrate: 读取迁移后版本: %w", err)
	}
	report.To = to
	for v := current + 1; v <= to; v++ {
		report.Applied = append(report.Applied, fmt.Sprintf("%03d_init.sql", v))
	}
	return report, nil
}

// daemonRunning checks the local discovery file and probes /healthz so the
// offline commands fail loudly instead of corrupting a live database.
func daemonRunning() (bool, string) {
	path := discoveryProbePath
	if path == "" {
		path = DiscoveryPath()
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, ""
	}
	var d Discovery
	if json.Unmarshal(raw, &d) != nil || d.Addr == "" {
		return false, ""
	}
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get("http://" + d.Addr + "/api/v1/healthz")
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK, d.Addr
}

func readBackupManifest(bundleDir string) (*BackupManifest, error) {
	raw, err := os.ReadFile(filepath.Join(bundleDir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("restore: 读取 manifest.json: %w", err)
	}
	var m BackupManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("restore: manifest.json 不是合法 JSON: %w", err)
	}
	if m.Format != BackupFormat {
		return nil, fmt.Errorf("restore: 备份格式 %q 不受支持（需要 %s）", m.Format, BackupFormat)
	}
	if len(m.Files) == 0 {
		return nil, fmt.Errorf("restore: manifest 没有列出任何文件")
	}
	return &m, nil
}

// copyFile streams src to dst so a multi-GiB database never has to fit in
// memory, and lands with owner-only permissions like the daemon's own files.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
