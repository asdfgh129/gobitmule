package goed2k

// #10: eMule-compatible peer IP filter.
//
// The classic ipfilter.dat line is
//
//	001.000.000.000 - 001.255.255.255 , 000 , description
//
// where the middle field is a level 0..255. eMule's CIPFilter::IsFiltered drops
// an address when its range level is strictly below the configured filter level
// (127 by default), so with level 127 entries 000..126 are blocked and 127..255
// pass. CIDR ("10.0.0.0/8") and single addresses are accepted as a convenience
// for hand written lists.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
)

// DefaultIPFilterLevel mirrors eMule's default filter level.
const DefaultIPFilterLevel = 127

type ipRange struct {
	start uint32
	end   uint32
	level int
	label string
}

// IPFilter is a concurrency-safe IPv4 block list. A zero value is unusable;
// construct it with NewIPFilter.
type IPFilter struct {
	mu       sync.RWMutex
	ranges   []ipRange
	maxLevel int
	path     string
}

// NewIPFilter returns an empty filter at eMule's default level.
func NewIPFilter() *IPFilter {
	return &IPFilter{maxLevel: DefaultIPFilterLevel}
}

// SetLevel changes which range levels are blocked (entries with
// level <= maxLevel). eMule exposes this as "IP filter level".
func (f *IPFilter) SetLevel(level int) {
	if level < 0 {
		level = 0
	}
	if level > 255 {
		level = 255
	}
	f.mu.Lock()
	f.maxLevel = level
	f.mu.Unlock()
}

// Level reports the configured filter level.
func (f *IPFilter) Level() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.maxLevel
}

// Count reports how many ranges are loaded.
func (f *IPFilter) Count() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.ranges)
}

// Path reports the file the ranges were loaded from ("" when built in memory).
func (f *IPFilter) Path() string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.path
}

// Enabled reports whether any range is loaded; callers use it to skip the
// lookup entirely on the hot path.
func (f *IPFilter) Enabled() bool {
	return f != nil && f.Count() > 0
}

// LoadFile replaces the current ranges with the contents of path.
func (f *IPFilter) LoadFile(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	n, err := f.Load(file)
	if err != nil {
		return n, fmt.Errorf("%s: %w", path, err)
	}
	f.mu.Lock()
	f.path = path
	f.mu.Unlock()
	return n, nil
}

// Load parses an ipfilter.dat stream and replaces the current ranges.
func (f *IPFilter) Load(r io.Reader) (int, error) {
	ranges := make([]ipRange, 0, 1024)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		entry, err := parseIPFilterLine(line)
		if err != nil {
			// A single malformed line must not discard the whole list.
			continue
		}
		ranges = append(ranges, entry)
	}
	if err := scanner.Err(); err != nil {
		return len(ranges), err
	}
	f.mu.Lock()
	f.ranges = ranges
	f.mu.Unlock()
	return len(ranges), nil
}

// Add appends one range programmatically.
func (f *IPFilter) Add(start, end net.IP, level int, label string) error {
	lo, ok := ipv4ToUint32(start)
	if !ok {
		return errors.New("ipfilter: start address must be IPv4")
	}
	hi, ok := ipv4ToUint32(end)
	if !ok {
		return errors.New("ipfilter: end address must be IPv4")
	}
	if hi < lo {
		lo, hi = hi, lo
	}
	f.mu.Lock()
	f.ranges = append(f.ranges, ipRange{start: lo, end: hi, level: level, label: label})
	f.mu.Unlock()
	return nil
}

// IsFiltered reports whether ip falls in a blocked range. Matching eMule, a
// range blocks when its level is strictly below the configured level, and the
// unspecified address 0.0.0.0 is never filtered.
func (f *IPFilter) IsFiltered(ip net.IP) bool {
	if f == nil {
		return false
	}
	addr, ok := ipv4ToUint32(ip)
	if !ok || addr == 0 {
		return false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if len(f.ranges) == 0 {
		return false
	}
	maxLevel := f.maxLevel
	for _, r := range f.ranges {
		if r.level >= maxLevel {
			continue
		}
		if addr >= r.start && addr <= r.end {
			return true
		}
	}
	return false
}

// IsFilteredAddr filters a net.Addr (TCP/UDP peers). Non-IP addrs pass.
func (f *IPFilter) IsFilteredAddr(addr net.Addr) bool {
	if f == nil || addr == nil {
		return false
	}
	switch v := addr.(type) {
	case *net.TCPAddr:
		return f.IsFiltered(v.IP)
	case *net.UDPAddr:
		return f.IsFiltered(v.IP)
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	return f.IsFiltered(net.ParseIP(host))
}

func parseIPFilterLine(line string) (ipRange, error) {
	// Strip the description first: it may itself contain commas or colons.
	level := 0
	body := line
	desc := ""
	parts := strings.Split(line, ",")
	if len(parts) >= 2 {
		body = strings.TrimSpace(parts[0])
		if lv, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
			level = lv
		}
		if len(parts) >= 3 {
			desc = strings.TrimSpace(strings.Join(parts[2:], ","))
		}
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return ipRange{}, errors.New("ipfilter: empty range")
	}
	if strings.Contains(body, "-") {
		bounds := strings.SplitN(body, "-", 2)
		lo, ok := ipv4ToUint32(net.ParseIP(strings.TrimSpace(bounds[0])))
		if !ok {
			return ipRange{}, errors.New("ipfilter: bad start address")
		}
		hi, ok := ipv4ToUint32(net.ParseIP(strings.TrimSpace(bounds[1])))
		if !ok {
			return ipRange{}, errors.New("ipfilter: bad end address")
		}
		if hi < lo {
			lo, hi = hi, lo
		}
		return ipRange{start: lo, end: hi, level: level, label: desc}, nil
	}
	if strings.Contains(body, "/") {
		_, network, err := net.ParseCIDR(body)
		if err != nil {
			return ipRange{}, err
		}
		lo, ok := ipv4ToUint32(network.IP)
		if !ok {
			return ipRange{}, errors.New("ipfilter: CIDR must be IPv4")
		}
		ones, bits := network.Mask.Size()
		if bits != 32 {
			return ipRange{}, errors.New("ipfilter: CIDR must be IPv4")
		}
		hi := lo | uint32((uint64(1)<<(32-ones))-1)
		return ipRange{start: lo, end: hi, level: level, label: desc}, nil
	}
	single, ok := ipv4ToUint32(net.ParseIP(body))
	if !ok {
		return ipRange{}, errors.New("ipfilter: bad address")
	}
	return ipRange{start: single, end: single, level: level, label: desc}, nil
}

// ipv4ToUint32 converts an IPv4 address (including 4-in-6 form) to its
// big-endian integer representation.
func ipv4ToUint32(ip net.IP) (uint32, bool) {
	if ip == nil {
		return 0, false
	}
	v4 := ip.To4()
	if v4 == nil {
		return 0, false
	}
	return uint32(v4[0])<<24 | uint32(v4[1])<<16 | uint32(v4[2])<<8 | uint32(v4[3]), true
}
