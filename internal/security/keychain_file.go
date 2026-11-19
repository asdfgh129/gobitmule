package security

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/asdfgh129/ud/internal/core"
)

// fileBackend stores secrets as individual 0600 files inside a 0700 directory.
//
// This is NOT a keychain: there is no per-application ACL and no encryption at
// rest beyond the filesystem's own protections (and, on a full-disk-encrypted
// machine, the disk key). It exists because the alternative — keeping nothing —
// is worse: credentials would vanish on every restart. CredentialBackend
// therefore reports "file", and docs/06 §9 documents the difference.
type fileBackend struct {
	dir string
}

func newFileBackend(stateDir string) (*fileBackend, error) {
	if stateDir == "" {
		return nil, errBackendUnavailable
	}
	dir := filepath.Join(stateDir, "credentials")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, core.Err(core.E_INTERNAL, "create credential directory: %v", err)
	}
	// MkdirAll honours the umask and leaves an existing directory untouched, so
	// tighten explicitly: a 0755 directory would let any local user list the
	// credential ids.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, core.Err(core.E_INTERNAL, "secure credential directory: %v", err)
	}
	return &fileBackend{dir: dir}, nil
}

func (b *fileBackend) kind() string   { return BackendFile }
func (b *fileBackend) scheme() string { return schemeFile }

func (b *fileBackend) path(id string) string { return filepath.Join(b.dir, id) }

func (b *fileBackend) put(_ context.Context, id, secret string) error {
	// Write to a sibling temp file then rename: a crash mid-write must never
	// leave a truncated credential behind (docs/06 §5 has the same rule for
	// .part files).
	final := b.path(id)
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return core.Err(core.E_INTERNAL, "create credential file: %v", err)
	}
	if _, err := f.WriteString(secret); err != nil {
		f.Close()
		os.Remove(tmp)
		return core.Err(core.E_INTERNAL, "write credential file: %v", err)
	}
	// Sync before rename so the rename cannot publish an empty file after a
	// power loss.
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return core.Err(core.E_INTERNAL, "sync credential file: %v", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return core.Err(core.E_INTERNAL, "close credential file: %v", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return core.Err(core.E_INTERNAL, "publish credential file: %v", err)
	}
	return nil
}

func (b *fileBackend) get(_ context.Context, id string) (string, error) {
	p := b.path(id)
	info, err := os.Stat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", errCredentialNotFound()
		}
		return "", core.Err(core.E_INTERNAL, "stat credential file: %v", err)
	}
	// Refuse to read a file someone else widened: silently accepting a 0644
	// credential is how a local user reads another user's password. Go mode
	// bits do not model NTFS DACLs — os.Stat reports 0666 for every writable
	// file — so on Windows this check can never pass and would permanently
	// brick the file backend (found by the 2026-11-19 Windows real-machine
	// run). There the protection is the per-user profile DACL, and the
	// system backend is DPAPI anyway; this file backend only exists as the
	// POSIX degradation path.
	if runtime.GOOS != "windows" {
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			return "", core.Err(core.E_INTERNAL,
				"credential file permissions are too open (%#o); expected 0600", mode)
		}
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", core.Err(core.E_INTERNAL, "read credential file: %v", err)
	}
	return string(data), nil
}

func (b *fileBackend) delete(_ context.Context, id string) error {
	if err := os.Remove(b.path(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return core.Err(core.E_INTERNAL, "delete credential file: %v", err)
	}
	return nil
}
