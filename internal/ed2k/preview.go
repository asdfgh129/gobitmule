package goed2k

// #8: file preview of a partially downloaded transfer, the headless equivalent
// of eMule's CPartFile::PreviewFile.
//
// eMule lets the user preview a file while it is still downloading: it creates
// a temporary file that contains every complete part from the beginning of the
// file (so a media player can start playing) and hands it to the shell. A
// daemon cannot "open" the file, so it materialises that contiguous prefix and
// returns its path; the caller decides how to present it.
//
// A preview requires at least one complete leading part. Parts are 9.5 MB in
// eD2k, so the prefix is always enough for a container header plus the first
// frames.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

// PreviewInfo describes a materialised preview.
type PreviewInfo struct {
	Hash  protocol.Hash `json:"hash"`
	Path  string        `json:"path"`
	Bytes int64         `json:"bytes"`
	Parts int           `json:"parts"`
	// Complete reports that the preview covers the whole file (the transfer
	// finished while the preview was built).
	Complete bool `json:"complete"`
}

// PreviewReady reports how many complete parts start the file, which is
// exactly the amount a preview can contain.
func (t *Transfer) PreviewReady() int {
	if t == nil {
		return 0
	}
	parts := 0
	for parts < t.numPieces && t.picker.IsComplete(parts) {
		parts++
	}
	return parts
}

// CreatePreview writes the contiguous complete prefix of the transfer into dir
// and returns its path. The file is named after the hash so repeated previews
// overwrite the same artefact instead of filling the directory.
func (s *Session) CreatePreview(hash protocol.Hash, dir string) (PreviewInfo, error) {
	transfer := s.LookupTransfer(hash)
	if transfer == nil {
		return PreviewInfo{}, NewError(NoTransfer)
	}
	return transfer.CreatePreview(dir)
}

// CreatePreview materialises the preview for this transfer.
func (t *Transfer) CreatePreview(dir string) (PreviewInfo, error) {
	if t == nil {
		return PreviewInfo{}, NewError(NoTransfer)
	}
	parts := t.PreviewReady()
	if parts == 0 {
		return PreviewInfo{}, NewError(NoTransfer)
	}
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "gobitmule-preview")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return PreviewInfo{}, err
	}

	length := int64(parts) * PieceSize
	complete := parts >= t.numPieces || length >= t.Size()
	if length > t.Size() {
		length = t.Size()
	}
	data, err := t.ReadRange(0, length)
	if err != nil {
		return PreviewInfo{}, err
	}
	if int64(len(data)) < length {
		// The disk handler may return a short read for a part that was
		// verified but not yet flushed; the prefix is still usable.
		length = int64(len(data))
	}
	if length == 0 {
		return PreviewInfo{}, fmt.Errorf("preview: no readable data for %s", t.hash.String())
	}

	name := t.hash.String() + previewExtension(t.FileName())
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return PreviewInfo{}, err
	}
	debugPeerf("preview %s parts=%d bytes=%d path=%s", t.hash.String(), parts, length, path)
	return PreviewInfo{Hash: t.hash, Path: path, Bytes: length, Parts: parts, Complete: complete}, nil
}

// previewExtension keeps the original extension so players pick the right
// demuxer; unknown or unsafe names fall back to ".part".
func previewExtension(fileName string) string {
	ext := strings.ToLower(filepath.Ext(fileName))
	if len(ext) < 2 || len(ext) > 10 {
		return ".part"
	}
	for _, r := range ext[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return ".part"
		}
	}
	return ext
}
