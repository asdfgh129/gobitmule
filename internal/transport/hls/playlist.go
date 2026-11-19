// Package hlstransport implements the L1 HLS streaming transport (WBS 13,
// FR-TRN-08): media-playlist parsing, AES-128 CBC decryption, variant
// selection and bounded-concurrent segment download with sequential commit
// into the scheduler-owned ChunkWriter. The zero-sidecar baseline holds —
// everything here is pure Go stdlib.
//
// Explicit L1 boundaries (ADR-0023):
//   - VOD only. A media playlist without EXT-X-ENDLIST is a live stream and is
//     rejected instead of silently downloading forever.
//   - METHOD=AES-128 only. SAMPLE-AES / fMP4 CENC encryption is rejected.
//   - No DASH (WBS 13 的另一半), no ad insertion tags, no alternative audio /
//     subtitle renditions beyond the selected variant.
package hlstransport

import (
	"bufio"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// maxPlaylistBytes bounds any single playlist document read into memory. A
// hostile server answering the .m3u8 URL with a 10 GiB body must fail closed
// before the reader allocates (docs/11 §4 资源上限 posture).
const maxPlaylistBytes = 4 << 20

// Playlist is a parsed HLS document. Exactly one of Master / Media is set.
type Playlist struct {
	Master *MasterPlaylist
	Media  *MediaPlaylist
}

// MasterPlaylist is an EXT-X-STREAM-INF document (adaptive set).
type MasterPlaylist struct {
	Variants []Variant
}

// Variant is one EXT-X-STREAM-INF entry. URI is resolved against the master
// playlist URL; Bandwidth is the declared peak bitrate in bits per second.
type Variant struct {
	URI           string
	Bandwidth     int64
	Resolution    string
	Codecs        string
	ProgramID     int
	IsFallback    bool // no BANDWIDTH attribute: RFC 8216 says treat as 0-ranked
	RenditionName string
}

// MediaPlaylist is an EXTINF document (one playable rendition, VOD only).
type MediaPlaylist struct {
	Segments  []Segment
	VOD       bool  // EXT-X-ENDLIST seen
	TargetDur int64 // EXT-X-TARGETDURATION, seconds
	MediaSeq  int64 // EXT-X-MEDIA-SEQUENCE
	IsFMP4    bool  // EXT-X-MAP or .m4s-style segments (fragmented MP4)
}

// Segment is one media segment. Key is the cipher state at this segment
// (nil when METHOD=NONE is in effect).
type Segment struct {
	URI       string
	Duration  float64 // EXTINF, seconds
	Key       *Key    // nil = unencrypted
	ByteRange *ByteRange
	Title     string
}

// Key is an EXT-X-KEY entry.
type Key struct {
	Method string // "NONE" | "AES-128"
	URI    string
	IV     string // hex WITHOUT 0x prefix, empty = derive from media sequence
}

// ByteRange is an EXT-X-BYTERANGE: <n>[@<o>]; a missing offset continues the
// previous sub-range of the same segment resource.
type ByteRange struct {
	Length int64
	Offset int64 // -1 = continue from previous extent
}

// ParsePlaylist parses a raw M3U8 document. base is the playlist URL used to
// resolve relative URIs (RFC 8216 §4.3: URIs are relative to the playlist).
func ParsePlaylist(base string, raw []byte) (*Playlist, error) {
	if len(raw) > maxPlaylistBytes {
		return nil, ErrPlaylistTooLarge
	}
	scanner := bufio.NewScanner(strings.NewReader(stringFrom(raw)))
	scanner.Buffer(make([]byte, 0, 64*1024), maxPlaylistBytes)
	baseURL, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("playlist base URL: %w", err)
	}

	// One pass: sniff the document type first. A master playlist is the one
	// carrying EXT-X-STREAM-INF; anything with EXTINF is a media playlist.
	hasStreamInf, hasInf := false, false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			hasStreamInf = true
		case strings.HasPrefix(line, "#EXTINF:"):
			hasInf = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan playlist: %w", err)
	}
	if hasStreamInf && hasInf {
		return nil, ErrMixedPlaylist
	}
	scanner = bufio.NewScanner(strings.NewReader(stringFrom(raw)))
	scanner.Buffer(make([]byte, 0, 64*1024), maxPlaylistBytes)
	if hasStreamInf {
		pl, err := parseMaster(baseURL, scanner)
		if err != nil {
			return nil, err
		}
		return &Playlist{Master: pl}, nil
	}
	if !hasInf {
		return nil, ErrNotPlaylist
	}
	pl, err := parseMedia(baseURL, scanner)
	if err != nil {
		return nil, err
	}
	return &Playlist{Media: pl}, nil
}

// stringFrom keeps ParsePlaylist readable without repeating the conversion.
func stringFrom(raw []byte) string { return string(raw) }

func resolveURI(base *url.URL, raw string) string {
	ref, err := url.Parse(raw)
	if err != nil || ref.IsAbs() {
		return raw
	}
	return base.ResolveReference(ref).String()
}

// parseAttr parses an RFC 8216 attribute list ("A=1,B=\"x\""). Quoted values
// may contain commas and '='; unquoted ones may not.
func parseAttr(s string) map[string]string {
	out := map[string]string{}
	var key, val strings.Builder
	inKey := true
	inQuote := false
	flush := func() {
		k := strings.TrimSpace(key.String())
		v := strings.TrimSpace(val.String())
		if k != "" {
			out[strings.ToUpper(k)] = strings.Trim(v, `"`)
		}
		key.Reset()
		val.Reset()
		inKey = true
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case c == '=' && inKey && !inQuote:
			inKey = false
		case c == ',' && !inQuote:
			flush()
		default:
			if inKey {
				key.WriteByte(c)
			} else {
				val.WriteByte(c)
			}
		}
	}
	flush()
	return out
}

func parseMaster(base *url.URL, scanner *bufio.Scanner) (*MasterPlaylist, error) {
	pl := &MasterPlaylist{}
	pendingAttr := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			pendingAttr = strings.TrimPrefix(line, "#EXT-X-STREAM-INF:")
		case strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#EXT"):
			// comment — ignore
		case line == "":
			// blank — ignore
		case pendingAttr != "":
			attr := parseAttr(pendingAttr)
			v := Variant{URI: resolveURI(base, line)}
			v.Bandwidth, _ = strconv.ParseInt(attr["BANDWIDTH"], 10, 64)
			v.Resolution = attr["RESOLUTION"]
			v.Codecs = attr["CODECS"]
			v.ProgramID, _ = strconv.Atoi(attr["PROGRAM-ID"])
			v.RenditionName = attr["NAME"]
			if _, ok := attr["BANDWIDTH"]; !ok {
				v.IsFallback = true
			}
			pl.Variants = append(pl.Variants, v)
			pendingAttr = ""
		default:
			// A URI without a preceding EXT-X-STREAM-INF in a master playlist:
			// an I-FRAME playlist or session data we do not consume.
			pendingAttr = ""
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan master playlist: %w", err)
	}
	if len(pl.Variants) == 0 {
		return nil, ErrNoVariants
	}
	return pl, nil
}

func parseMedia(base *url.URL, scanner *bufio.Scanner) (*MediaPlaylist, error) {
	pl := &MediaPlaylist{}
	curKey := (*Key)(nil)
	curInf := -1.0
	curTitle := ""
	pendingByteRange := (*ByteRange)(nil)
	prevExtentOffset := int64(-1)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "" || (strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#EXT")):
			// blank / comment
		case strings.HasPrefix(line, "#EXTM3U"):
			// magic — validated by the caller via LooksLikePlaylist
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			pl.TargetDur, _ = strconv.ParseInt(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"), 10, 64)
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			pl.MediaSeq, _ = strconv.ParseInt(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"), 10, 64)
		case strings.HasPrefix(line, "#EXT-X-ENDLIST"):
			pl.VOD = true
		case strings.HasPrefix(line, "#EXT-X-KEY:"):
			k, err := parseKey(strings.TrimPrefix(line, "#EXT-X-KEY:"), base)
			if err != nil {
				return nil, err
			}
			if k != nil && k.Method == "NONE" {
				curKey = nil
			} else {
				curKey = k
			}
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			pl.IsFMP4 = true
		case strings.HasPrefix(line, "#EXT-X-BYTERANGE:"):
			br, err := parseByteRange(strings.TrimPrefix(line, "#EXT-X-BYTERANGE:"), prevExtentOffset)
			if err != nil {
				return nil, err
			}
			pendingByteRange = br
			prevExtentOffset = br.Offset + br.Length
		case strings.HasPrefix(line, "#EXTINF:"):
			body := strings.TrimPrefix(line, "#EXTINF:")
			dur, title := splitEXTINF(body)
			curInf = dur
			curTitle = title
		case strings.HasPrefix(line, "#EXT-X-DISCONTINUITY"), strings.HasPrefix(line, "#EXT-X-PROGRAM-DATE-TIME"),
			strings.HasPrefix(line, "#EXT-X-GAP"), strings.HasPrefix(line, "#EXT-X-BITRATE"):
			// accepted and ignored for L1 commit semantics
		case strings.HasPrefix(line, "#EXT-X"):
			// unknown protocol tag: RFC 8216 requires clients to ignore
			// unrecognized tags rather than fail.
		case curInf >= 0:
			uri := resolveURI(base, line)
			if !pl.IsFMP4 && (strings.HasSuffix(path.Base(uri), ".m4s") || strings.HasSuffix(path.Base(uri), ".mp4")) {
				pl.IsFMP4 = true
			}
			pl.Segments = append(pl.Segments, Segment{
				URI:       uri,
				Duration:  curInf,
				Key:       curKey,
				ByteRange: pendingByteRange,
				Title:     curTitle,
			})
			curInf = -1
			curTitle = ""
			pendingByteRange = nil
		default:
			// URI without EXTINF — tolerate (some servers omit EXTINF on the
			// last segment), give it the target duration.
			uri := resolveURI(base, line)
			if !pl.IsFMP4 && (strings.HasSuffix(path.Base(uri), ".m4s") || strings.HasSuffix(path.Base(uri), ".mp4")) {
				pl.IsFMP4 = true
			}
			pl.Segments = append(pl.Segments, Segment{
				URI:      uri,
				Duration: float64(pl.TargetDur),
				Key:      curKey,
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan media playlist: %w", err)
	}
	if len(pl.Segments) == 0 {
		return nil, ErrNoSegments
	}
	return pl, nil
}

// splitEXTINF splits "12.34, some title" into duration and title. The title
// may itself contain commas, so only the first comma separates.
func splitEXTINF(body string) (float64, string) {
	i := strings.IndexByte(body, ',')
	durStr, title := body, ""
	if i >= 0 {
		durStr, title = body[:i], body[i+1:]
	}
	dur, err := strconv.ParseFloat(strings.TrimSpace(durStr), 64)
	if err != nil {
		dur = -1
	}
	return dur, strings.TrimSpace(title)
}

func parseKey(s string, base *url.URL) (*Key, error) {
	attr := parseAttr(s)
	switch strings.ToUpper(attr["METHOD"]) {
	case "NONE":
		return &Key{Method: "NONE"}, nil
	case "AES-128":
		uri := attr["URI"]
		if uri == "" {
			return nil, ErrKeyWithoutURI
		}
		return &Key{Method: "AES-128", URI: resolveURI(base, uri), IV: strings.TrimPrefix(attr["IV"], "0x")}, nil
	case "SAMPLE-AES":
		return nil, ErrUnsupportedCipher
	default:
		return nil, ErrUnsupportedCipher
	}
}

func parseByteRange(s string, prev int64) (*ByteRange, error) {
	at := strings.IndexByte(s, '@')
	lenStr, offStr := s, ""
	if at >= 0 {
		lenStr, offStr = s[:at], s[at+1:]
	}
	length, err := strconv.ParseInt(strings.TrimSpace(lenStr), 10, 64)
	if err != nil || length < 0 {
		return nil, ErrBadByteRange
	}
	br := &ByteRange{Length: length, Offset: -1}
	if offStr != "" {
		off, err := strconv.ParseInt(strings.TrimSpace(offStr), 10, 64)
		if err != nil || off < 0 {
			return nil, ErrBadByteRange
		}
		br.Offset = off
	} else if prev >= 0 {
		br.Offset = prev
	}
	return br, nil
}

// LooksLikePlaylist is the cheap structural sniff used by the resolver before
// committing to an HLS resource: an M3U8 document starts with #EXTM3U.
func LooksLikePlaylist(raw []byte) bool {
	s := strings.TrimLeft(string(raw), " \r\n\t\uFEFF")
	return strings.HasPrefix(s, "#EXTM3U")
}

// SelectVariant picks the variant to download. L1 policy (ADR-0023): highest
// declared BANDWIDTH wins; entries without BANDWIDTH rank lowest; ties fall
// back to document order (stable). This mirrors the "best quality" default
// users expect from yt-dlp-style selection.
func (m *MasterPlaylist) SelectVariant() Variant {
	best := 0
	for i := 1; i < len(m.Variants); i++ {
		if m.Variants[i].Bandwidth > m.Variants[best].Bandwidth {
			best = i
		}
	}
	return m.Variants[best]
}
