package hlstransport

import "errors"

// Sentinel errors carry stable diagnostics so the scheduler can classify them
// (E_CAPABILITY_UNAVAILABLE for structural limits, E_RESOLVE_FAILED for bad
// input) without string matching.
var (
	ErrNotPlaylist       = errors.New("not an M3U8 playlist (missing #EXTM3U)")
	ErrPlaylistTooLarge  = errors.New("playlist exceeds the size limit")
	ErrMixedPlaylist     = errors.New("document mixes master (STREAM-INF) and media (EXTINF) tags")
	ErrNoVariants        = errors.New("master playlist declares no variant")
	ErrNoSegments        = errors.New("media playlist declares no segment")
	ErrUnsupportedCipher = errors.New("only METHOD=AES-128 / NONE are supported (SAMPLE-AES is not)")
	ErrKeyWithoutURI     = errors.New("EXT-X-KEY with METHOD=AES-128 is missing URI")
	ErrBadByteRange      = errors.New("malformed EXT-X-BYTERANGE")
	ErrBadMediaGroup     = errors.New("EXT-X-MEDIA row without GROUP-ID")
	ErrLiveNotSupported  = errors.New("live media playlist (no EXT-X-ENDLIST) is not supported")
	ErrKeyFetchFailed    = errors.New("fetching EXT-X-KEY failed")
	ErrDecryptFailed     = errors.New("AES-128 CBC decryption failed")
)
