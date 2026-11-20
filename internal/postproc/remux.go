package postproc

// Remux is the P2 built-in, pure-Go PostProcessor (ADR-0011/0012). It turns
// one or more already-downloaded MPEG-TS streams (HLS / DASH TS / yt-dlp TS
// formats) into a progressive MP4 with moov before mdat. A fragmented MP4
// (DASH fMP4) is validated and passed through, because it is already an ISO
// BMFF container and no re-encoding or re-packetising is required.
//
// Contract: input is local paths only; output is <target>.remux.part; the
// Scheduler owns the atomic rename to <target>.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asdfgh129/ud/internal/core"
)

const (
	RemuxName = "remux"
	// maxRemuxInput bounds the in-memory working set. Streams above this need
	// the optional ffmpeg Transcode plugin (ADR-0011) or a future streaming
	// remuxer; failing explicitly is safer than an OOM kill of the daemon.
	maxRemuxInput = int64(1 << 30)
)

type Remux struct{}

func NewRemux() *Remux { return &Remux{} }

func (Remux) Name() string { return RemuxName }

// Sidecars deliberately reports nothing: Remux is part of the zero-sidecar
// baseline and must never be confused with ffmpeg.
func (Remux) Sidecars() []string { return nil }

func (r Remux) Process(ctx context.Context, in core.LocalFiles, out core.OutputSpec) (*core.ProcessResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, core.Err(core.E_POSTPROCESS_FAILED, "%v", err)
	}
	if len(in.Paths) == 0 {
		return nil, core.Err(core.E_POSTPROCESS_FAILED, "remux requires at least one local input file")
	}
	if out.Target == "" || strings.Contains(out.Target, "://") || !filepath.IsAbs(out.Target) {
		return nil, core.Err(core.E_INVALID_INPUT, "remux output target must be an absolute local path")
	}
	suffix := out.TempSuffix
	if suffix == "" {
		suffix = ".part"
	}
	if strings.ContainsAny(suffix, "/\\") || suffix == "." || suffix == ".." {
		return nil, core.Err(core.E_INVALID_INPUT, "invalid temp suffix %q", suffix)
	}
	outPath := out.Target + ".remux" + suffix

	var total int64
	inputs := make([][]byte, 0, len(in.Paths))
	for _, p := range in.Paths {
		if err := validateLocalPath(p); err != nil {
			return nil, core.Err(core.E_POSTPROCESS_FAILED, "%v", err)
		}
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			return nil, core.Err(core.E_POSTPROCESS_FAILED, "input file unavailable: %v", err)
		}
		total += st.Size()
		if total > maxRemuxInput {
			return nil, core.Err(core.E_CAPABILITY_UNAVAILABLE,
				"remux input exceeds %d MiB; use the optional ffmpeg plugin", maxRemuxInput>>20)
		}
		if err := ctx.Err(); err != nil {
			return nil, core.Err(core.E_POSTPROCESS_FAILED, "%v", err)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, core.Err(core.E_POSTPROCESS_FAILED, "read input: %v", err)
		}
		inputs = append(inputs, b)
	}
	if len(inputs) == 1 && looksLikeMP4(inputs[0]) {
		if err := validateMP4(inputs[0]); err != nil {
			return nil, core.Err(core.E_POSTPROCESS_FAILED, "fragmented MP4 input is invalid: %v", err)
		}
		if err := writeAtomic(outPath, inputs[0]); err != nil {
			return nil, err
		}
		return result(outPath), nil
	}

	video := &mediaTrack{kind: "vide", timescale: 90000}
	audio := &mediaTrack{kind: "soun", timescale: 90000}
	avc := &avcConfig{}
	aac := &aacConfig{}
	var haveVideo, haveAudio bool
	var videoRef, audioRef int64 = -1, -1

	for fi, data := range inputs {
		prog, units, err := tsDemux(data)
		if err != nil {
			return nil, core.Err(core.E_POSTPROCESS_FAILED, "input %d: %v", fi+1, err)
		}
		for _, u := range units {
			isVideo := isVideoPID(prog.ES, u.PID)
			pts := unwrap90(u.PTS, videoRef)
			if isVideo {
				if videoRef < 0 {
					videoRef = pts
				}
				pts = unwrap90(u.PTS, videoRef)
			} else {
				if audioRef < 0 {
					audioRef = pts
				}
				pts = unwrap90(u.PTS, audioRef)
			}
			dts := pts
			if u.DTS >= 0 {
				dts = unwrap90(u.DTS, pts)
			}
			cts := pts - dts
			if isVideo {
				packed, err := annexBToAVCC(u.Payload, avc)
				if err != nil {
					return nil, core.Err(core.E_POSTPROCESS_FAILED, "H.264 access unit: %v", err)
				}
				video.samples = append(video.samples, sample{data: packed, dts: dts, cts: cts})
				haveVideo = true
			} else {
				frames, err := adtsToSamples(u.Payload, aac)
				if err != nil {
					return nil, core.Err(core.E_POSTPROCESS_FAILED, "AAC access unit: %v", err)
				}
				frameDur := uint32(1024 * 90000 / aac.rate)
				for i, frame := range frames {
					fdts := dts + int64(i)*int64(frameDur)
					audio.samples = append(audio.samples, sample{data: frame, dts: fdts, duration: frameDur})
				}
				haveAudio = true
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, core.Err(core.E_POSTPROCESS_FAILED, "%v", err)
		}
	}
	if !haveVideo && !haveAudio {
		return nil, core.Err(core.E_POSTPROCESS_FAILED, "%v", errNoStreams)
	}

	tracks := make([]*mediaTrack, 0, 2)
	if haveVideo {
		if avc.sps == nil || avc.pps == nil || avc.avcC == nil {
			return nil, core.Err(core.E_POSTPROCESS_FAILED, "%v", errAVCConfig)
		}
		rebase(video.samples, true)
		video.avcC = avc.avcC
		w, h, err := spsDimensions(avc.sps)
		if err != nil {
			return nil, core.Err(core.E_POSTPROCESS_FAILED, "H.264 SPS: %v", err)
		}
		video.width, video.height = w, h
		tracks = append(tracks, video)
	}
	if haveAudio {
		rebase(audio.samples, false)
		audio.esds = aac.esds
		audio.sampleRate = aac.rate
		audio.channels = uint16(aac.channels)
		tracks = append(tracks, audio)
	}
	outBytes, err := writeMP4(tracks)
	if err != nil {
		return nil, core.Err(core.E_POSTPROCESS_FAILED, "%v", err)
	}
	if err := writeAtomic(outPath, outBytes); err != nil {
		return nil, err
	}
	return result(outPath), nil
}

func result(path string) *core.ProcessResult {
	return &core.ProcessResult{Outputs: []string{path}, TempPaths: []string{path}}
}

func isVideoPID(es []tsElementary, pid uint16) bool {
	for _, e := range es {
		if e.PID == pid {
			return e.StreamType == 0x1b || e.StreamType == 0x24
		}
	}
	return false
}

func looksLikeMP4(b []byte) bool {
	return len(b) > 12 && bytes.Equal(b[4:8], []byte("ftyp"))
}

// validateMP4 checks the outer box chain only; a full CENC/fMP4 parse is out
// of scope. Corrupt inputs fail here rather than being silently committed.
func validateMP4(b []byte) error {
	for off := 0; off+8 <= len(b); {
		size := int(binary.BigEndian.Uint32(b[off : off+4]))
		if size == 1 {
			if off+16 > len(b) {
				return errors.New("truncated large box")
			}
			size = int(binary.BigEndian.Uint64(b[off+8 : off+16]))
		}
		if size < 8 || off+size > len(b) {
			return fmt.Errorf("box %q has invalid size %d at %d", b[off+4:off+8], size, off)
		}
		off += size
	}
	return nil
}

func writeAtomic(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return core.Err(core.E_STORAGE_FULL, "write remux output: %v", err)
	}
	if f, err := os.Open(path); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	return nil
}
