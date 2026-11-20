// Package dashtransport implements the DASH half of WBS 13 (FR-TRN-08):
// MPD parsing, segment addressing (SegmentTemplate with $Number$ / $Time$
// identifiers and SegmentList), representation selection and sequential
// commit into the scheduler-owned ChunkWriter. Pure Go stdlib — the zero
// sidecar baseline holds.
//
// Explicit L1 boundaries (ADR-0024):
//   - static MPD only (`type="dynamic"` is a live session and is rejected,
//     matching the HLS live boundary);
//   - exactly one Period and one AdaptationSet: DASH with separate audio /
//     video renditions produces two byte streams that only the WBS 14 pure
//     Go Remux can combine into a playable file — until then such MPDs are
//     refused with E_CAPABILITY_UNAVAILABLE rather than silently downloading
//     a track without sound;
//   - cleartext only: a <ContentProtection> element (DRM / CENC) is refused;
//     the FR-TRN-08 AES-128 clause is delivered by the HLS half;
//   - SegmentTemplate and SegmentList are supported; SegmentBase (index-based
//     byte ranges inside one file) is refused.
package dashtransport

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Parser limits mirror the Metalink posture (ADR-0006 安全要求): a hostile
// MPD must fail closed before the reader allocates.
const (
	maxMPDBytes   = 4 << 20
	maxSegments   = 20000
	maxTimeline   = 10000
	maxRepsPerSet = 64
)

// Sentinel errors: stable diagnostics the scheduler classifies without
// string matching.
var (
	ErrNotMPD              = errors.New("not an MPD document (missing <MPD>)")
	ErrMPDTooLarge         = errors.New("MPD exceeds the size limit")
	ErrDynamicNotSupport   = errors.New("dynamic MPD (live session) is not supported")
	ErrMultiPeriod         = errors.New("multi-period MPD is not supported")
	ErrMultiAdaptationSet  = errors.New("separate audio/video adaptation sets require the WBS 14 Remux and are not supported yet")
	ErrDRMNotSupport       = errors.New("encrypted DASH (ContentProtection / CENC) is not supported")
	ErrSegmentBase         = errors.New("SegmentBase addressing is not supported (use SegmentTemplate or SegmentList)")
	ErrNoRepresentations   = errors.New("MPD declares no usable representation")
	ErrTemplateUnbounded   = errors.New("segment count cannot be bounded (missing mediaPresentationDuration / Period@duration for @duration templates)")
	ErrTimeWithoutTimeline = errors.New("$Time$ addressing requires a SegmentTimeline")
	ErrBadTemplate         = errors.New("malformed segment template")
)

// MPD is the parsed document (L1: one Period, one selected AdaptationSet).
type MPD struct {
	IsDynamic                 bool
	Period                    *Period
	MediaPresentationDuration float64 // seconds
}

// Period holds the adaptation sets.
type Period struct {
	BaseURL        string
	Duration       float64 // seconds, Period@duration (may be 0)
	AdaptationSets []AdaptationSet
}

// AdaptationSet groups representations of one media component.
type AdaptationSet struct {
	BaseURL         string
	ContentType     string
	MimeType        string
	Representations []Representation
	template        *SegmentTemplate // set-level defaults (attribute inheritance)
}

// Representation is one selectable encoding.
type Representation struct {
	ID        string
	Bandwidth int64
	Codecs    string
	BaseURL   string
	Template  *SegmentTemplate // effective template (set-level attrs merged)
	List      *SegmentList
}

// SegmentTemplate is the template addressing scheme. Exactly one of Duration
// (bounded by the period duration) or Timeline bounds the segment count;
// $Time$ addressing REQUIRES the timeline.
type SegmentTemplate struct {
	Timescale      int64
	Duration       int64
	Initialization string
	Media          string
	StartNumber    int64
	Timeline       []TimelineEntry
}

// TimelineEntry is an <S> row: t = start time (-1 → inherit previous end),
// d = duration, r = repeat count.
type TimelineEntry struct {
	T int64 // -1 = inherit
	D int64
	R int64
}

// SegmentList is the explicit addressing scheme.
type SegmentList struct {
	Timescale      int64
	Initialization string // <Initialization sourceURL>
	Segments       []string
}

// ---------- raw shapes ----------

type rawMPD struct {
	XMLName                   xml.Name    `xml:"MPD"`
	Type                      string      `xml:"type,attr"`
	MediaPresentationDuration string      `xml:"mediaPresentationDuration,attr"`
	BaseURLs                  []string    `xml:"BaseURL"`
	Periods                   []rawPeriod `xml:"Period"`
}

type rawPeriod struct {
	Duration       string          `xml:"duration,attr"`
	BaseURLs       []string        `xml:"BaseURL"`
	AdaptationSets []rawAdaptation `xml:"AdaptationSet"`
}

type rawAdaptation struct {
	ContentType     string              `xml:"contentType,attr"`
	MimeType        string              `xml:"mimeType,attr"`
	BaseURLs        []string            `xml:"BaseURL"`
	ContentProtect  []rawAny            `xml:"ContentProtection"`
	Template        *rawTemplate        `xml:"SegmentTemplate"`
	Representations []rawRepresentation `xml:"Representation"`
}

type rawAny struct {
	XMLName xml.Name
}

type rawRepresentation struct {
	ID             string       `xml:"id,attr"`
	Bandwidth      string       `xml:"bandwidth,attr"`
	Codecs         string       `xml:"codecs,attr"`
	BaseURLs       []string     `xml:"BaseURL"`
	ContentProtect []rawAny     `xml:"ContentProtection"`
	Template       *rawTemplate `xml:"SegmentTemplate"`
	List           *rawSegList  `xml:"SegmentList"`
	SegmentBase    *rawAny      `xml:"SegmentBase"`
}

type rawTemplate struct {
	Timescale      string        `xml:"timescale,attr"`
	Duration       string        `xml:"duration,attr"`
	Initialization string        `xml:"initialization,attr"`
	Media          string        `xml:"media,attr"`
	StartNumber    string        `xml:"startNumber,attr"`
	Timeline       []rawTimeline `xml:"SegmentTimeline>S"`
}

type rawTimeline struct {
	T string `xml:"t,attr"`
	D string `xml:"d,attr"`
	R string `xml:"r,attr"`
}

type rawSegList struct {
	Timescale      string      `xml:"timescale,attr"`
	Initialization *rawInit    `xml:"Initialization"`
	Segments       []rawSegURL `xml:"SegmentURL"`
}

type rawInit struct {
	SourceURL string `xml:"sourceURL,attr"`
}

type rawSegURL struct {
	SourceURL string `xml:"sourceURL,attr"`
	Media     string `xml:"media,attr"`
}

// ---------- parsing ----------

// LooksLikeMPD is the cheap structural sniff used before committing to a
// DASH resource.
func LooksLikeMPD(raw []byte) bool {
	s := strings.TrimLeft(string(raw), " \r\n\t\uFEFF")
	return strings.HasPrefix(s, "<?xml") && strings.Contains(s[:min(len(s), 4096)], "<MPD")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ParseMPD parses a raw MPD document. mpdURL resolves the BaseURL chain.
func ParseMPD(mpdURL string, raw []byte) (*MPD, error) {
	if len(raw) > maxMPDBytes {
		return nil, ErrMPDTooLarge
	}
	upper := strings.ToUpper(string(raw))
	if strings.Contains(upper, "<!DOCTYPE") || strings.Contains(upper, "<!ENTITY") {
		return nil, fmt.Errorf("DTD / entity declarations are forbidden in MPD XML")
	}
	var doc rawMPD
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotMPD, err)
	}

	m := &MPD{IsDynamic: strings.EqualFold(doc.Type, "dynamic")}
	if dur, err := parseISODuration(doc.MediaPresentationDuration); err == nil {
		m.MediaPresentationDuration = dur
	} else if doc.MediaPresentationDuration != "" {
		return nil, fmt.Errorf("%w: bad mediaPresentationDuration %q", ErrBadTemplate, doc.MediaPresentationDuration)
	}
	if m.IsDynamic {
		return nil, ErrDynamicNotSupport
	}
	if len(doc.Periods) != 1 {
		return nil, fmt.Errorf("%w: found %d periods", ErrMultiPeriod, len(doc.Periods))
	}
	rawP := doc.Periods[0]
	p := &Period{BaseURL: lastBaseURL(doc.BaseURLs, rawP.BaseURLs)}
	if d, err := parseISODuration(rawP.Duration); err == nil {
		p.Duration = d
	} else if rawP.Duration != "" {
		return nil, fmt.Errorf("%w: bad Period@duration %q", ErrBadTemplate, rawP.Duration)
	}

	if len(rawP.AdaptationSets) != 1 {
		return nil, fmt.Errorf("%w: found %d adaptation sets", ErrMultiAdaptationSet, len(rawP.AdaptationSets))
	}
	rawA := rawP.AdaptationSets[0]
	if len(rawA.ContentProtect) > 0 {
		return nil, ErrDRMNotSupport
	}
	set := AdaptationSet{
		BaseURL:     lastBaseURL(nil, rawA.BaseURLs),
		ContentType: rawA.ContentType,
		MimeType:    rawA.MimeType,
	}
	if rawA.Template != nil {
		st, err := buildTemplate(rawA.Template)
		if err != nil {
			return nil, err
		}
		set.template = st
	}
	if len(rawA.Representations) == 0 || len(rawA.Representations) > maxRepsPerSet {
		return nil, fmt.Errorf("%w: %d representations in set", ErrNoRepresentations, len(rawA.Representations))
	}
	for _, rawR := range rawA.Representations {
		if len(rawR.ContentProtect) > 0 {
			return nil, ErrDRMNotSupport
		}
		if rawR.SegmentBase != nil {
			return nil, ErrSegmentBase
		}
		bw, _ := strconv.ParseInt(strings.TrimSpace(rawR.Bandwidth), 10, 64)
		rep := Representation{
			ID:        rawR.ID,
			Bandwidth: bw,
			Codecs:    rawR.Codecs,
			BaseURL:   lastBaseURL(nil, rawR.BaseURLs),
		}
		if rawR.Template != nil {
			st, err := buildTemplate(rawR.Template)
			if err != nil {
				return nil, err
			}
			mergeTemplate(st, set.template)
			rep.Template = st
		} else if set.template != nil {
			cp := *set.template
			rep.Template = &cp
		}
		if rawR.List != nil {
			sl, err := buildSegList(rawR.List)
			if err != nil {
				return nil, err
			}
			rep.List = sl
		}
		if rep.Template == nil && rep.List == nil {
			return nil, ErrSegmentBase
		}
		set.Representations = append(set.Representations, rep)
	}
	if len(set.Representations) == 0 {
		return nil, ErrNoRepresentations
	}
	p.AdaptationSets = []AdaptationSet{set}
	m.Period = p
	return m, nil
}

// lastBaseURL folds a BaseURL chain into the last explicit value; DASH
// semantics resolve each level against the previous one, and the transport
// applies them in order against the MPD URL.
func lastBaseURL(upper, own []string) string {
	all := append(append([]string{}, upper...), own...)
	for i := len(all) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(all[i]); s != "" {
			return s
		}
	}
	return ""
}

// BaseURLChain returns every non-empty BaseURL level the representation
// sits behind, in resolution order (MPD → Period → Set → Representation).
func (m *MPD) BaseURLChain(rep *Representation) []string {
	var chain []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			chain = append(chain, s)
		}
	}
	add(m.Period.BaseURL)
	add(m.Period.AdaptationSets[0].BaseURL)
	add(rep.BaseURL)
	return chain
}

func buildTemplate(raw *rawTemplate) (*SegmentTemplate, error) {
	st := &SegmentTemplate{}
	var err error
	if st.Timescale, err = parseAttrInt(raw.Timescale, "timescale"); err != nil {
		return nil, err
	}
	if st.Duration, err = parseAttrInt(raw.Duration, "duration"); err != nil {
		return nil, err
	}
	if st.StartNumber, err = parseAttrInt(raw.StartNumber, "startNumber"); err != nil {
		return nil, err
	}
	st.Initialization = raw.Initialization
	st.Media = raw.Media
	if st.Timescale == 0 {
		st.Timescale = 1
	}
	if len(raw.Timeline) > maxTimeline {
		return nil, fmt.Errorf("%w: timeline has %d rows", ErrBadTemplate, len(raw.Timeline))
	}
	for _, s := range raw.Timeline {
		e := TimelineEntry{T: -1}
		if s.T != "" {
			if e.T, err = strconv.ParseInt(strings.TrimSpace(s.T), 10, 64); err != nil {
				return nil, fmt.Errorf("%w: bad S@t %q", ErrBadTemplate, s.T)
			}
		}
		if e.D, err = strconv.ParseInt(strings.TrimSpace(s.D), 10, 64); err != nil || e.D <= 0 {
			return nil, fmt.Errorf("%w: bad S@d %q", ErrBadTemplate, s.D)
		}
		if s.R != "" {
			if e.R, err = strconv.ParseInt(strings.TrimSpace(s.R), 10, 64); err != nil {
				return nil, fmt.Errorf("%w: bad S@r %q", ErrBadTemplate, s.R)
			}
		}
		st.Timeline = append(st.Timeline, e)
	}
	return st, nil
}

// mergeTemplate overlays the set-level defaults onto a representation-level
// template: zero-valued attributes inherit from the set (spec §5.3.9.2.2).
func mergeTemplate(rep, set *SegmentTemplate) {
	if set == nil {
		return
	}
	if rep.Timescale == 1 && set.Timescale != 0 {
		rep.Timescale = set.Timescale
	}
	if rep.Duration == 0 {
		rep.Duration = set.Duration
	}
	if rep.Initialization == "" {
		rep.Initialization = set.Initialization
	}
	if rep.Media == "" {
		rep.Media = set.Media
	}
	if rep.StartNumber == 0 {
		rep.StartNumber = set.StartNumber
	}
	if len(rep.Timeline) == 0 {
		rep.Timeline = set.Timeline
	}
}

func buildSegList(raw *rawSegList) (*SegmentList, error) {
	sl := &SegmentList{}
	var err error
	if sl.Timescale, err = parseAttrInt(raw.Timescale, "timescale"); err != nil {
		return nil, err
	}
	if sl.Timescale == 0 {
		sl.Timescale = 1
	}
	if raw.Initialization != nil {
		sl.Initialization = raw.Initialization.SourceURL
	}
	if len(raw.Segments) == 0 {
		return nil, fmt.Errorf("%w: SegmentList has no SegmentURL", ErrBadTemplate)
	}
	if len(raw.Segments) > maxSegments {
		return nil, fmt.Errorf("%w: SegmentList has %d entries", ErrBadTemplate, len(raw.Segments))
	}
	for _, s := range raw.Segments {
		u := s.SourceURL
		if u == "" {
			u = s.Media
		}
		if u == "" {
			return nil, fmt.Errorf("%w: SegmentURL without sourceURL/media", ErrBadTemplate)
		}
		sl.Segments = append(sl.Segments, u)
	}
	return sl, nil
}

func parseAttrInt(s, name string) (int64, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: bad %s %q", ErrBadTemplate, name, s)
	}
	return n, nil
}

// parseISODuration parses the "PT1H2M3.5S" shape used by MPD duration
// attributes (case-insensitive per spec). Y / M are calendar-dependent and
// rejected: DASH segment math needs exact seconds. "P1D" is exact.
func parseISODuration(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty duration")
	}
	lower := strings.ToLower(s)
	if !strings.HasPrefix(lower, "p") {
		return 0, fmt.Errorf("duration %q lacks P prefix", s)
	}
	body := lower[1:]
	t := strings.IndexByte(body, 't')
	date, timePart := body, ""
	if t >= 0 {
		date, timePart = body[:t], body[t+1:]
		if strings.TrimSpace(timePart) == "" && strings.TrimSpace(date) == "" {
			return 0, fmt.Errorf("duration %q has no components", s)
		}
	}
	sec := 0.0
	if date != "" {
		if strings.ContainsAny(date, "ym") {
			return 0, fmt.Errorf("calendar components (Y/M) are not supported in %q", s)
		}
		d, err := parseNum(date, "d")
		if err != nil {
			return 0, err
		}
		sec += d * 86400
	}
	rest := timePart
	if i := strings.IndexByte(rest, 'h'); i >= 0 {
		v, err := strconv.ParseFloat(strings.TrimSpace(rest[:i]), 64)
		if err != nil {
			return 0, fmt.Errorf("bad hours in %q", s)
		}
		sec += v * 3600
		rest = rest[i+1:]
	}
	if i := strings.IndexByte(rest, 'm'); i >= 0 {
		v, err := strconv.ParseFloat(strings.TrimSpace(rest[:i]), 64)
		if err != nil {
			return 0, fmt.Errorf("bad minutes in %q", s)
		}
		sec += v * 60
		rest = rest[i+1:]
	}
	if i := strings.IndexByte(rest, 's'); i >= 0 {
		v, err := strconv.ParseFloat(strings.TrimSpace(rest[:i]), 64)
		if err != nil {
			return 0, fmt.Errorf("bad seconds in %q", s)
		}
		sec += v
		rest = rest[i+1:]
	}
	if strings.TrimSpace(rest) != "" {
		return 0, fmt.Errorf("trailing garbage in duration %q", s)
	}
	return sec, nil
}

func parseNum(s, unit string) (float64, error) {
	s = strings.TrimSpace(s)
	if !strings.HasSuffix(s, unit) {
		return 0, fmt.Errorf("duration segment %q lacks %s", s, unit)
	}
	return strconv.ParseFloat(strings.TrimSuffix(s, unit), 64)
}

// ---------- selection and expansion ----------

// SelectRepresentation picks the highest-BANDWIDTH representation (ties fall
// back to document order), the same policy as the HLS variant selection
// (ADR-0023).
func (m *MPD) SelectRepresentation() *Representation {
	set := &m.Period.AdaptationSets[0]
	best := 0
	for i := 1; i < len(set.Representations); i++ {
		if set.Representations[i].Bandwidth > set.Representations[best].Bandwidth {
			best = i
		}
	}
	return &set.Representations[best]
}

// resolveAgainst applies the BaseURL chain then the segment URL.
func resolveAgainst(mpdURL string, chain []string, raw string) string {
	base := mpdURL
	for _, c := range chain {
		base = resolveURL(base, c)
	}
	return resolveURL(base, raw)
}

func resolveURL(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil || r.IsAbs() {
		return ref
	}
	return b.ResolveReference(r).String()
}

// ExpandSegmentURLs produces the ordered segment URL list for a
// representation. baseURLChain is the representation's BaseURL chain; every
// produced URL is absolute.
func (m *MPD) ExpandSegmentURLs(mpdURL string, rep *Representation) (initURL string, segments []string, err error) {
	chain := m.BaseURLChain(rep)
	if rep.List != nil {
		var init string
		if rep.List.Initialization != "" {
			init = resolveAgainst(mpdURL, chain, rep.List.Initialization)
		}
		for _, s := range rep.List.Segments {
			segments = append(segments, resolveAgainst(mpdURL, chain, s))
		}
		return init, segments, nil
	}
	st := rep.Template
	if st == nil {
		return "", nil, ErrSegmentBase
	}
	if strings.Contains(st.Media, "$Time") && len(st.Timeline) == 0 {
		return "", nil, ErrTimeWithoutTimeline
	}
	var initRaw string
	if st.Initialization != "" {
		initRaw = expandTemplate(st.Initialization, rep.ID, rep.Bandwidth, 0, 0)
	}
	periodDur := m.Period.Duration
	if periodDur == 0 {
		periodDur = m.MediaPresentationDuration
	}
	if len(st.Timeline) > 0 {
		// Timeline mode: $Time$ is each segment's start in timescale units.
		t := int64(0)
		number := st.StartNumber
		for i, e := range st.Timeline {
			if e.T >= 0 || i == 0 {
				if e.T >= 0 {
					t = e.T
				} else if i == 0 {
					t = 0
				}
			}
			for k := int64(0); k <= e.R; k++ {
				if len(segments) >= maxSegments {
					return "", nil, fmt.Errorf("%w: timeline expands past %d segments", ErrBadTemplate, maxSegments)
				}
				segments = append(segments, resolveAgainst(mpdURL, chain,
					expandTemplate(st.Media, rep.ID, rep.Bandwidth, number, t)))
				number++
				t += e.D
			}
		}
		if len(segments) == 0 {
			return "", nil, fmt.Errorf("%w: timeline yields no segments", ErrBadTemplate)
		}
		return resolveAgainst(mpdURL, chain, initRaw), segments, nil
	}
	// @duration mode: the segment count must be bound by the period duration.
	if st.Duration <= 0 || periodDur <= 0 {
		return "", nil, ErrTemplateUnbounded
	}
	// Number of segments = ceil(period * timescale / duration) — the last
	// segment may be shorter than @duration but never absent.
	total := (int64(float64(st.Timescale)*periodDur) + st.Duration - 1) / st.Duration
	if total > maxSegments {
		return "", nil, fmt.Errorf("%w: @duration template expands to %d segments", ErrBadTemplate, total)
	}
	for n := st.StartNumber; n < st.StartNumber+total; n++ {
		segments = append(segments, resolveAgainst(mpdURL, chain,
			expandTemplate(st.Media, rep.ID, rep.Bandwidth, n, 0)))
	}
	if len(segments) == 0 {
		return "", nil, fmt.Errorf("%w: template yields no segments", ErrBadTemplate)
	}
	return resolveAgainst(mpdURL, chain, initRaw), segments, nil
}

// expandTemplate substitutes the RFC-style identifiers of ISO/IEC 23009-1
// §5.3.9.4.2: $RepresentationID$, $Bandwidth$, $Number$, $Time$, $$ escape,
// and the %0Nd width form ($Number%05d$).
func expandTemplate(tpl, repID string, bandwidth, number, time int64) string {
	var b strings.Builder
	for i := 0; i < len(tpl); i++ {
		c := tpl[i]
		if c != '$' {
			b.WriteByte(c)
			continue
		}
		j := strings.IndexByte(tpl[i+1:], '$')
		if j < 0 {
			// Lone '$': keep verbatim (malformed input tolerated like HLS).
			b.WriteByte(c)
			continue
		}
		token := tpl[i+1 : i+1+j]
		i += 1 + j // consume "$token$"
		if token == "" {
			b.WriteByte('$') // "$$" escape
			continue
		}
		name, width := token, ""
		if k := strings.IndexByte(token, '%'); k >= 0 {
			name, width = token[:k], token[k+1:]
		}
		format := "%d"
		if width != "" && strings.HasSuffix(width, "d") {
			format = "%" + width
		}
		switch name {
		case "RepresentationID":
			b.WriteString(repID)
		case "Bandwidth":
			fmt.Fprintf(&b, format, bandwidth)
		case "Number":
			fmt.Fprintf(&b, format, number)
		case "Time":
			fmt.Fprintf(&b, format, time)
		default:
			// Unknown identifier: emit verbatim per spec's tolerance.
			b.WriteString("$" + token + "$")
		}
	}
	return b.String()
}
