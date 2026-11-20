package postproc

// Minimal MPEG-TS demuxer for the P2 Remux path (ADR-0011). It parses PAT/PMT
// to discover H.264 (stream_type 0x1b) and AAC (0x0f) elementary streams and
// returns PES access units with 90 kHz timestamps. No ffmpeg, no cgo, no
// external packages: only the subset needed by HLS/DASH and yt-dlp streams is
// implemented. Unsupported streams are ignored explicitly, never guessed.

import (
	"encoding/binary"
	"errors"
)

const tsPacketSize = 188

var (
	errTSSync       = errors.New("mpeg-ts sync byte missing")
	errTSTruncated  = errors.New("mpeg-ts packet truncated")
	errPESMalformed = errors.New("pes header malformed")
)

type tsElementary struct {
	PID        uint16
	StreamType byte
}

type tsProgram struct {
	PMTPID uint16
	ES     []tsElementary
}

type tsPES struct {
	PID     uint16
	PTS     int64 // unwrapped 90 kHz ticks
	DTS     int64 // -1 when absent
	Payload []byte
}

// tsDemux returns the program map and every PES access unit in file order.
func tsDemux(data []byte) (*tsProgram, []tsPES, error) {
	if len(data) < tsPacketSize {
		return nil, nil, errTSTruncated
	}
	base := 0
	for base+tsPacketSize <= len(data) && data[base] != 0x47 {
		base++ // tolerate a leading byte from a malformed concatenation
	}
	if base+tsPacketSize > len(data) {
		return nil, nil, errTSSync
	}
	prog := &tsProgram{}
	var pmtPIDs []uint16
	units := make(map[uint16]*pesBuilder)
	var out []tsPES
	flush := func(pid uint16) {
		if b := units[pid]; b != nil {
			out = append(out, b.pes)
			delete(units, pid)
		}
	}

	for off := base; off+tsPacketSize <= len(data); off += tsPacketSize {
		p := data[off : off+tsPacketSize]
		if p[0] != 0x47 {
			return nil, nil, errTSSync
		}
		pid := uint16(p[1]&0x1f)<<8 | uint16(p[2])
		pusi := p[1]&0x40 != 0
		afc := (p[3] >> 4) & 0x03
		if afc == 0 { // reserved
			continue
		}
		start := 4
		if afc&0x02 != 0 { // adaptation field
			if int(p[4])+1 >= tsPacketSize {
				continue
			}
			start += 1 + int(p[4])
		}
		if afc&0x01 == 0 || start >= tsPacketSize {
			continue
		}
		payload := p[start:]
		if pid == 0 && pusi {
			if ids, err := parsePAT(payload); err == nil {
				pmtPIDs = ids
			}
			continue
		}
		if pusi && len(pmtPIDs) > 0 && containsPID(pmtPIDs, pid) {
			if es, err := parsePMT(payload); err == nil {
				prog.PMTPID = pid
				prog.ES = es
			}
			continue
		}
		if len(prog.ES) == 0 {
			continue
		}
		if !isElementaryPID(prog.ES, pid) {
			continue
		}
		if pusi {
			flush(pid)
			pes, err := parsePES(payload)
			if err != nil {
				continue // a corrupt access unit must not kill the whole remux
			}
			pes.PID = pid
			units[pid] = &pesBuilder{pes: *pes}
			continue
		}
		if b := units[pid]; b != nil {
			b.pes.Payload = append(b.pes.Payload, payload...)
		}
	}
	for _, pid := range pesOrder(prog.ES) {
		flush(pid)
	}
	return prog, out, nil
}

// pesOrder flushes video first so the SPS is available before any audio unit.
func pesOrder(es []tsElementary) []uint16 {
	var video, audio []uint16
	for _, e := range es {
		switch e.StreamType {
		case 0x1b, 0x24:
			video = append(video, e.PID)
		case 0x0f, 0x11:
			audio = append(audio, e.PID)
		}
	}
	return append(video, audio...)
}

type pesBuilder struct{ pes tsPES }

func containsPID(ids []uint16, pid uint16) bool {
	for _, id := range ids {
		if id == pid {
			return true
		}
	}
	return false
}

func isElementaryPID(es []tsElementary, pid uint16) bool {
	for _, e := range es {
		if e.PID == pid {
			return true
		}
	}
	return false
}

func parsePAT(p []byte) ([]uint16, error) {
	// PSI payloads start with a pointer_field; the section follows it.
	if len(p) < 2 || 1+int(p[0]) >= len(p) {
		return nil, errPESMalformed
	}
	p = p[1+int(p[0]):]
	if len(p) < 8 || p[0] != 0 {
		return nil, errPESMalformed
	}
	secLen := int(p[1]&0x0f)<<8 | int(p[2])
	if secLen < 9 || 3+secLen > len(p) {
		return nil, errPESMalformed
	}
	end := 3 + secLen - 4 // drop CRC32
	var out []uint16
	for i := 8; i+4 <= end; i += 4 {
		pid := uint16(p[i+2]&0x1f)<<8 | uint16(p[i+3])
		if pid != 0 {
			out = append(out, pid)
		}
	}
	return out, nil
}

func parsePMT(p []byte) ([]tsElementary, error) {
	if len(p) < 2 || 1+int(p[0]) >= len(p) {
		return nil, errPESMalformed
	}
	p = p[1+int(p[0]):]
	if len(p) < 12 || p[0] != 2 {
		return nil, errPESMalformed
	}
	secLen := int(p[1]&0x0f)<<8 | int(p[2])
	if secLen < 13 || 3+secLen > len(p) {
		return nil, errPESMalformed
	}
	progInfoLen := int(p[10]&0x0f)<<8 | int(p[11])
	pos := 12 + progInfoLen
	end := 3 + secLen - 4
	var out []tsElementary
	for pos+5 <= end {
		out = append(out, tsElementary{
			StreamType: p[pos],
			PID:        uint16(p[pos+1]&0x1f)<<8 | uint16(p[pos+2]),
		})
		esLen := int(p[pos+3]&0x0f)<<8 | int(p[pos+4])
		pos += 5 + esLen
	}
	return out, nil
}

func parsePES(p []byte) (*tsPES, error) {
	if len(p) < 9 || p[0] != 0 || p[1] != 0 || p[2] != 1 {
		return nil, errPESMalformed
	}
	headerLen := int(p[8])
	if headerLen > len(p)-9 {
		return nil, errPESMalformed
	}
	out := &tsPES{DTS: -1}
	// PES_packet_length (when nonzero) bounds the payload; the rest of a TS
	// packet is adaptation/stuffing and must not leak into a codec access unit.
	pesLen := int(p[4])<<8 | int(p[5])
	flags := p[7] >> 6
	if flags >= 2 && headerLen >= 5 {
		out.PTS = readTimestamp(p[9:14])
	}
	if flags == 3 && headerLen >= 10 {
		out.DTS = readTimestamp(p[14:19])
	}
	end := len(p)
	if pesLen > 0 && 6+pesLen < end {
		end = 6 + pesLen
	}
	if end < 9+headerLen {
		end = 9 + headerLen
	}
	out.Payload = append([]byte(nil), p[9+headerLen:end]...)
	// Signal to the builder the declared total payload length, so continuation
	// packets can be trimmed of TS stuffing. Zero means "unbounded" (video).
	return out, nil
}

// readTimestamp decodes the 33-bit PTS/DTS field in 90 kHz units.
func readTimestamp(b []byte) int64 {
	return int64(b[0]>>1&0x07)<<30 |
		int64(b[1])<<22 |
		int64(b[2]>>1)<<15 |
		int64(b[3])<<7 |
		int64(b[4]>>1)
}

// unwrap90 extends a 33-bit timestamp to a monotonic int64 around a reference.
func unwrap90(raw, ref int64) int64 {
	const mod = int64(1) << 33
	const half = int64(1) << 32
	raw &= mod - 1
	v := raw
	for v+half < ref {
		v += mod
	}
	for v-half > ref {
		v -= mod
	}
	return v
}

func tsPacketCount(n int) int { return (n + tsPacketSize - 1) / tsPacketSize }

func u16be(b []byte) uint16 { return binary.BigEndian.Uint16(b) }
