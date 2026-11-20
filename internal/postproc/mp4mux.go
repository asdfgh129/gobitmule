package postproc

// Minimal ISO BMFF writer for the P2 Remux path (ADR-0011). It writes a
// progressive MP4 with one video (H.264/AVC) and/or one audio (AAC) track,
// moov before mdat (faststart), and sample tables built from the rebased
// 90 kHz timestamps. No external dependency, no cgo.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"sort"
)

var (
	errNoStreams      = errors.New("no supported H.264/AAC stream in input")
	errAVCConfig      = errors.New("H.264 SPS/PPS missing or malformed")
	errAACConfig      = errors.New("ADTS AAC frame missing or malformed")
	errUnsupportedSPS = errors.New("SPS scaling matrix is not supported by the built-in remuxer")
)

type sample struct {
	data     []byte
	dts      int64 // 90 kHz, rebased to >= 0
	cts      int64 // composition offset (PTS - DTS), signed
	duration uint32
}

type mediaTrack struct {
	kind       string // "vide" | "soun"
	timescale  uint32
	samples    []sample
	avcC       []byte
	esds       []byte
	width      uint16
	height     uint16
	sampleRate uint32
	channels   uint16
}

func box(typ string, payload []byte) []byte {
	out := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	copy(out[4:8], typ)
	return append(out, payload...)
}

func fullBox(typ string, version byte, flags uint32, payload []byte) []byte {
	b := make([]byte, 4)
	b[0] = version
	b[1], b[2], b[3] = byte(flags>>16), byte(flags>>8), byte(flags)
	return box(typ, append(b, payload...))
}

func be16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func be32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func be64(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }

// annexBToAVCC converts one Annex B access unit to length-prefixed NAL units,
// collecting parameter sets into the codec configuration.
func annexBToAVCC(data []byte, cfg *avcConfig) ([]byte, error) {
	nals, err := splitAnnexB(data)
	if err != nil || len(nals) == 0 {
		return nil, errAVCConfig
	}
	var out bytes.Buffer
	for _, nal := range nals {
		if len(nal) == 0 {
			continue
		}
		switch nal[0] & 0x1f {
		case 7:
			cfg.sps = append([]byte(nil), nal...)
			continue
		case 8:
			cfg.pps = append([]byte(nil), nal...)
			continue
		case 9, 6: // AUD / SEI carry no sample data
			continue
		}
		_ = binary.Write(&out, binary.BigEndian, uint32(len(nal)))
		out.Write(nal)
	}
	if out.Len() == 0 {
		return nil, errAVCConfig
	}
	// Parameter sets may arrive in their own access unit; avcC is built lazily
	// once both have been seen rather than failing on the first video sample.
	if cfg.sps != nil && cfg.pps != nil && cfg.avcC == nil {
		cfg.avcC, err = buildAVCC(cfg.sps, cfg.pps)
		if err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}

type avcConfig struct {
	sps, pps []byte
	avcC     []byte
}

func splitAnnexB(b []byte) ([][]byte, error) {
	type nalSpan struct{ start, end int }
	var spans []nalSpan
	for i := 0; i+3 <= len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			spans = append(spans, nalSpan{start: i + 3})
			i += 2
		}
	}
	if len(spans) == 0 {
		return nil, errAVCConfig
	}
	out := make([][]byte, 0, len(spans))
	for j, s := range spans {
		e := len(b)
		if j+1 < len(spans) {
			// start code begins three bytes before the next NAL's payload.
			e = spans[j+1].start - 3
			for e > s.start && b[e-1] == 0 {
				e--
			}
		}
		if e > s.start {
			out = append(out, b[s.start:e])
		}
	}
	return out, nil
}

func buildAVCC(sps, pps []byte) ([]byte, error) {
	if len(sps) < 4 || len(pps) < 2 {
		return nil, errAVCConfig
	}
	var b bytes.Buffer
	b.WriteByte(1)
	b.Write(sps[1:4]) // profile, compatibility, level
	b.WriteByte(0xff) // 6 reserved bits + lengthSizeMinusOne=3
	b.WriteByte(0xe1) // 3 reserved bits + one SPS
	_ = binary.Write(&b, binary.BigEndian, uint16(len(sps)))
	b.Write(sps)
	b.WriteByte(1)
	_ = binary.Write(&b, binary.BigEndian, uint16(len(pps)))
	b.Write(pps)
	return box("avcC", b.Bytes()), nil // avcC is a plain box, not a FullBox
}

type aacConfig struct {
	objectType byte
	sampleIdx  byte
	channels   byte
	rate       uint32
	esds       []byte
}

// adtsToSamples extracts raw AAC access units from one ADTS-carrying PES.
func adtsToSamples(data []byte, cfg *aacConfig) ([][]byte, error) {
	var out [][]byte
	for i := 0; i+7 <= len(data); {
		if data[i] != 0xff || data[i+1]&0xf0 != 0xf0 {
			i++
			continue
		}
		prot := data[i+1]&1 == 0
		obj := (data[i+2] >> 6) & 0x03
		idx := (data[i+2] >> 2) & 0x0f
		ch := ((data[i+2] & 1) << 2) | ((data[i+3] >> 6) & 0x03)
		frameLen := int(data[i+3]&0x03)<<11 | int(data[i+4])<<3 | int(data[i+5])>>5
		header := 7
		if prot {
			header = 9
		}
		if int(idx) >= len(aacSampleRates) || ch == 0 || frameLen < header || i+frameLen > len(data) {
			i++
			continue
		}
		if cfg.esds == nil {
			cfg.objectType = obj + 1
			cfg.sampleIdx = idx
			cfg.channels = ch
			cfg.rate = aacSampleRates[idx]
			var e error
			cfg.esds, e = buildESDS(cfg.objectType, cfg.sampleIdx, cfg.channels)
			if e != nil {
				return nil, e
			}
		}
		out = append(out, append([]byte(nil), data[i+header:i+frameLen]...))
		i += frameLen
	}
	if len(out) == 0 {
		return nil, errAACConfig
	}
	return out, nil
}

var aacSampleRates = [...]uint32{
	96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050,
	16000, 12000, 11025, 8000, 7350,
}

func buildESDS(objType, sampleIdx, channels byte) ([]byte, error) {
	asc := []byte{
		(objType << 3) | (sampleIdx >> 1),
		((sampleIdx & 1) << 7) | (channels << 3),
	}
	dsi := append([]byte{0x05, 0x02}, asc...)
	dcd := []byte{0x40, 0x15, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0} // 13B DecoderConfig body
	dcd = append(dcd, dsi...)
	dcdBox := append([]byte{0x04, byte(len(dcd))}, dcd...)
	slBox := []byte{0x06, 0x01, 0x02}
	esBody := append([]byte{0, 0, 0}, dcdBox...) // ES_ID(2) + flags(1)
	esBody = append(esBody, slBox...)
	es := append([]byte{0x03, byte(len(esBody))}, esBody...)
	return fullBox("esds", 0, 0, es), nil
}

// bitReader reads SPS fields without allocating a full H.264 parser.
type bitReader struct {
	b []byte
	n int
}

func (r *bitReader) u(n int) (uint64, error) {
	var v uint64
	for i := 0; i < n; i++ {
		if r.n >= len(r.b)*8 {
			return 0, errUnsupportedSPS
		}
		v <<= 1
		if r.b[r.n/8]&(0x80>>(r.n%8)) != 0 {
			v |= 1
		}
		r.n++
	}
	return v, nil
}

func (r *bitReader) ue() (uint64, error) {
	zeros := 0
	for {
		v, err := r.u(1)
		if err != nil {
			return 0, err
		}
		if v == 1 {
			break
		}
		zeros++
		if zeros > 32 {
			return 0, errUnsupportedSPS
		}
	}
	if zeros == 0 {
		return 0, nil
	}
	rest, err := r.u(zeros)
	if err != nil {
		return 0, err
	}
	return (1 << zeros) - 1 + rest, nil
}

func spsDimensions(sps []byte) (uint16, uint16, error) {
	if len(sps) < 4 {
		return 0, 0, errAVCConfig
	}
	r := &bitReader{b: sps[1:]}
	profile, err := r.u(8)
	if err != nil {
		return 0, 0, err
	}
	_, _ = r.u(8) // constraint flags
	_, _ = r.u(8) // level
	_, _ = r.ue() // seq_parameter_set_id
	chroma := uint64(1)
	switch profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		chroma, err = r.ue()
		if err != nil {
			return 0, 0, err
		}
		if chroma == 3 {
			_, _ = r.u(1)
		}
		_, _ = r.ue()
		_, _ = r.ue()
		_, _ = r.u(1)
		matrix, err := r.u(1)
		if err != nil {
			return 0, 0, err
		}
		if matrix != 0 {
			return 0, 0, errUnsupportedSPS
		}
	}
	_, _ = r.ue() // log2_max_frame_num_minus4
	pocType, err := r.ue()
	if err != nil {
		return 0, 0, err
	}
	if pocType == 0 {
		_, _ = r.ue()
	} else if pocType == 1 {
		_, _ = r.u(1)
		_, _ = r.ue()
		_, _ = r.ue()
		n, err := r.ue()
		if err != nil {
			return 0, 0, err
		}
		for i := uint64(0); i < n; i++ {
			_, _ = r.ue()
		}
	}
	_, _ = r.ue() // max_num_ref_frames
	_, _ = r.u(1) // gaps
	mbs, err := r.ue()
	if err != nil {
		return 0, 0, err
	}
	mapUnits, err := r.ue()
	if err != nil {
		return 0, 0, err
	}
	frameOnly, err := r.u(1)
	if err != nil {
		return 0, 0, err
	}
	if frameOnly == 0 {
		_, _ = r.u(1) // mb_adaptive_frame_field_flag
	}
	_, _ = r.u(1) // direct_8x8_inference_flag
	crop, err := r.u(1)
	if err != nil {
		return 0, 0, err
	}
	var cl, cr, ct, cb uint64
	if crop != 0 {
		cl, _ = r.ue()
		cr, _ = r.ue()
		ct, _ = r.ue()
		cb, _ = r.ue()
	}
	subW, subH := uint64(2), uint64(2)
	if chroma == 0 || chroma == 3 {
		subW, subH = 1, 1
	} else if chroma == 1 {
		subW = 2
	}
	w := (mbs+1)*16 - cl*subW - cr*subW
	h := (2-frameOnly)*(mapUnits+1)*16 - ct*subH - cb*subH
	return uint16(w), uint16(h), nil
}

// rebase converts unwrapped timestamps into monotonic DTS plus signed CTS
// offsets, then derives per-sample durations.
func rebase(samples []sample, hasDTS bool) {
	if len(samples) == 0 {
		return
	}
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].dts < samples[j].dts })
	base := samples[0].dts
	var prev int64
	for i := range samples {
		if samples[i].dts < base {
			samples[i].dts = base
		}
		if i > 0 && samples[i].dts <= prev {
			samples[i].dts = prev + 1 // enforce strictly increasing decode order
		}
		prev = samples[i].dts
		samples[i].dts -= base
		if !hasDTS {
			samples[i].cts = 0
		}
	}
	for i := range samples {
		if i+1 < len(samples) {
			d := samples[i+1].dts - samples[i].dts
			if d > 0 && d < 1<<24 {
				samples[i].duration = uint32(d)
			} else {
				samples[i].duration = 3000
			}
		} else {
			samples[i].duration = 3000
			if len(samples) > 1 {
				samples[i].duration = samples[len(samples)-2].duration
			}
		}
	}
}

// writeMP4 assembles ftyp + moov + mdat with moov first (faststart). Chunk
// offsets are computed from a first pass over the tables so no data copy is
// needed after the sample buffers are built.
func writeMP4(tracks []*mediaTrack) ([]byte, error) {
	var w bytes.Buffer
	ftyp := box("ftyp", append([]byte("isom"), append(be32(0x200), []byte("isomiso2avc1mp41")...)...))
	payloadSize := uint64(0)
	for _, t := range tracks {
		for _, s := range t.samples {
			payloadSize += uint64(len(s.data))
		}
	}
	if payloadSize+16 > 1<<32 {
		return nil, errors.New("remux output exceeds 4 GiB MP4 limit")
	}
	mdatHeader := 8
	if payloadSize+8 > 1<<32-1 {
		mdatHeader = 16
	}
	placeholders := make(map[*mediaTrack][]uint32, len(tracks))
	for _, t := range tracks {
		placeholders[t] = make([]uint32, len(t.samples))
	}
	build := func(offsets map[*mediaTrack][]uint32) []byte {
		var moov bytes.Buffer
		const timescale = uint32(90000)
		var duration uint64
		for _, t := range tracks {
			for _, s := range t.samples {
				if d := uint64(s.dts) + uint64(s.duration); d > duration {
					duration = d
				}
			}
		}
		// mvhd v0: version+flags(4), creation(4), modification(4), timescale(4),
		// duration(4), rate(4), volume(2), reserved(2+8), matrix(36),
		// pre_defined(24), next_track_ID(4).
		mvhd := make([]byte, 100)
		binary.BigEndian.PutUint32(mvhd[12:16], timescale)
		binary.BigEndian.PutUint32(mvhd[16:20], uint32(duration))
		binary.BigEndian.PutUint32(mvhd[20:24], 0x00010000)
		binary.BigEndian.PutUint16(mvhd[24:26], 0x0100)
		copy(mvhd[36:72], identityMatrix())
		binary.BigEndian.PutUint32(mvhd[96:100], uint32(len(tracks)+1))
		moov.Write(box("mvhd", mvhd))
		for i, t := range tracks {
			moov.Write(buildTrack(t, uint32(i+1), timescale, offsets[t]))
		}
		return moov.Bytes()
	}
	probe := build(placeholders)
	// The two-pass size check must use the final serialized moov, including
	// its 8-byte box header; stco offsets are computed from the same length.
	probe = box("moov", probe)
	mdatStart := uint64(len(ftyp) + len(probe) + mdatHeader)
	offsets := make(map[*mediaTrack][]uint32, len(tracks))
	off := mdatStart // samples are written track-by-track into one mdat
	for _, t := range tracks {
		list := make([]uint32, 0, len(t.samples))
		for _, s := range t.samples {
			list = append(list, uint32(off))
			off += uint64(len(s.data))
		}
		offsets[t] = list
	}
	moov := box("moov", build(offsets))
	if len(moov) != len(probe) {
		return nil, errors.New("internal remux error: moov size changed")
	}
	w.Write(ftyp)
	w.Write(moov)
	if mdatHeader == 8 {
		w.Write(be32(uint32(payloadSize + 8)))
		w.Write([]byte("mdat"))
	} else {
		w.Write(be32(1))
		w.Write([]byte("mdat"))
		w.Write(be64(payloadSize + 16))
	}
	for _, t := range tracks {
		for _, s := range t.samples {
			w.Write(s.data)
		}
	}
	return w.Bytes(), nil
}

func identityMatrix() []byte {
	v := []uint32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}
	b := make([]byte, 0, 36)
	for _, x := range v {
		b = append(b, be32(x)...)
	}
	return b
}

func buildTrack(t *mediaTrack, id, movieTimescale uint32, offsets []uint32) []byte {
	dur := uint64(0)
	for _, s := range t.samples {
		dur += uint64(s.duration)
	}
	// tkhd v0: version+flags(4), creation(4), modification(4), track_ID(4),
	// reserved(4), duration(4), reserved(8), layer(2), alt_group(2),
	// volume(2), reserved(2), matrix(36), width(4), height(4).
	tkhd := make([]byte, 84)
	tkhd[1], tkhd[2], tkhd[3] = 0, 0, 7
	binary.BigEndian.PutUint32(tkhd[12:16], id)
	binary.BigEndian.PutUint32(tkhd[20:24], uint32(dur*uint64(movieTimescale)/uint64(t.timescale)))
	copy(tkhd[40:76], identityMatrix())
	if t.kind == "vide" {
		binary.BigEndian.PutUint32(tkhd[76:80], uint32(t.width)<<16)
		binary.BigEndian.PutUint32(tkhd[80:84], uint32(t.height)<<16)
	} else {
		binary.BigEndian.PutUint32(tkhd[76:80], 0x01000000)
	}

	// mdhd v0: version+flags(4), creation(4), modification(4), timescale(4),
	// duration(4), language(2), pre_defined(2).
	mdhd := make([]byte, 24)
	binary.BigEndian.PutUint32(mdhd[12:16], t.timescale)
	binary.BigEndian.PutUint64(mdhd[16:24], dur)
	mdhd[22], mdhd[23] = 0x55, 0xc4 // "und" language code
	hdlrBody := append(be32(0), []byte(t.kind)...)
	hdlrBody = append(hdlrBody, make([]byte, 12)...)
	hdlrBody = append(hdlrBody, append([]byte(t.kind), 0)...)

	var hdr []byte
	if t.kind == "vide" {
		hdr = fullBox("vmhd", 0, 1, make([]byte, 8))
	} else {
		hdr = fullBox("smhd", 0, 0, make([]byte, 4))
	}
	dref := fullBox("dref", 0, 0, append(be32(1), fullBox("url ", 0, 1, nil)...))
	dinf := box("dinf", dref)

	stsd := fullBox("stsd", 0, 0, append(be32(1), t.sampleEntry()...))
	var stbl bytes.Buffer
	stbl.Write(stsd)
	stbl.Write(buildRLE(t.samples, func(s sample) uint32 { return s.duration }, "stts"))
	hasCTS := false
	for _, s := range t.samples {
		if s.cts != 0 {
			hasCTS = true
			break
		}
	}
	if hasCTS {
		version := byte(0)
		for _, s := range t.samples {
			if s.cts < 0 {
				version = 1
			}
		}
		var payload []byte
		payload = append(payload, be32(0)...) // entry count, filled below
		for i := 0; i < len(t.samples); {
			v := t.samples[i].cts
			j := i + 1
			for j < len(t.samples) && t.samples[j].cts == v {
				j++
			}
			payload = append(payload, be32(uint32(j-i))...)
			if version == 1 {
				payload = append(payload, be32(uint32(int32(v)))...)
			} else {
				payload = append(payload, be32(uint32(v))...)
			}
			i = j
		}
		binary.BigEndian.PutUint32(payload[0:4], uint32((len(payload)-4)/8))
		stbl.Write(fullBox("ctts", version, 0, payload))
	}
	// One sample per chunk; stco lists one offset per sample.
	stsc := fullBox("stsc", 0, 0, append(append(be32(1), be32(1)...), append(be32(1), be32(1)...)...))
	stbl.Write(stsc)
	var stsz []byte
	stsz = append(stsz, be32(0)...)
	stsz = append(stsz, be32(uint32(len(t.samples)))...)
	for _, s := range t.samples {
		stsz = append(stsz, be32(uint32(len(s.data)))...)
	}
	stbl.Write(fullBox("stsz", 0, 0, stsz))
	var stco []byte
	stco = append(stco, be32(uint32(len(offsets)))...)
	for _, o := range offsets {
		stco = append(stco, be32(o)...)
	}
	stbl.Write(fullBox("stco", 0, 0, stco))
	minf := box("minf", append(append(hdr, dinf...), stbl.Bytes()...))
	mdia := box("mdia", append(append(box("mdhd", mdhd), box("hdlr", hdlrBody)...), minf...))
	return box("trak", append(box("tkhd", tkhd), mdia...))
}

func buildRLE(samples []sample, val func(sample) uint32, typ string) []byte {
	var payload []byte
	payload = append(payload, be32(0)...)
	for i := 0; i < len(samples); {
		v := val(samples[i])
		j := i + 1
		for j < len(samples) && val(samples[j]) == v {
			j++
		}
		payload = append(payload, be32(uint32(j-i))...)
		payload = append(payload, be32(v)...)
		i = j
	}
	binary.BigEndian.PutUint32(payload[0:4], uint32((len(payload)-4)/8))
	return fullBox(typ, 0, 0, payload)
}

func (t *mediaTrack) sampleEntry() []byte {
	var payload []byte
	payload = append(payload, make([]byte, 6)...)
	payload = append(payload, be16(1)...)
	if t.kind == "vide" {
		payload = append(payload, make([]byte, 16)...)
		payload = append(payload, be16(t.width)...)
		payload = append(payload, be16(t.height)...)
		payload = append(payload, be32(0x00480000)...)
		payload = append(payload, be32(0x00480000)...)
		payload = append(payload, be32(0)...)
		payload = append(payload, be16(1)...)
		payload = append(payload, make([]byte, 32)...)
		payload = append(payload, be16(0x0018)...)
		payload = append(payload, be16(0xffff)...)
		return box("avc1", append(payload, t.avcC...))
	}
	payload = append(payload, make([]byte, 8)...)
	payload = append(payload, be16(t.channels)...)
	payload = append(payload, be16(16)...)
	payload = append(payload, be16(0)...)
	payload = append(payload, be16(0)...)
	payload = append(payload, be32(t.sampleRate<<16)...)
	return box("mp4a", append(payload, t.esds...))
}
