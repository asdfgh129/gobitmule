package goed2k

// #8-#11: preview, collection, ipfilter and search rating.

import (
	"bytes"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/disk"
	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

func TestIPFilterParsesRangesLevelsAndCIDR(t *testing.T) {
	filter := NewIPFilter()
	count, err := filter.Load(strings.NewReader(strings.Join([]string{
		"# comment line",
		"1.2.3.0 - 1.2.3.255 , 000 , blocked range",
		"10.0.0.0/8, 200 , high level",
		"192.168.1.5 , 100 , single",
		"",
	}, "\n")))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if count != 3 {
		t.Fatalf("loaded %d ranges, want 3", count)
	}
	if !filter.IsFiltered(mustIP(t, "1.2.3.77")) {
		t.Fatal("low level range must be blocked at the default level")
	}
	if filter.IsFiltered(mustIP(t, "8.8.8.8")) {
		t.Fatal("address outside every range must pass")
	}
	if filter.IsFiltered(mustIP(t, "10.1.2.3")) {
		t.Fatal("level 200 must not be blocked at default level 127")
	}
	if !filter.IsFiltered(mustIP(t, "192.168.1.5")) {
		t.Fatal("single address at level 100 must be blocked")
	}
	filter.SetLevel(255)
	if !filter.IsFiltered(mustIP(t, "10.1.2.3")) {
		t.Fatal("level 200 must be blocked after raising the level to 255")
	}
	if filter.Path() != "" {
		t.Fatalf("in-memory filter reports a path %q", filter.Path())
	}
}

func TestIPFilterLoadFileAndAddr(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ipfilter.dat")
	if err := os.WriteFile(path, []byte("203.0.113.0 - 203.0.113.255 , 0 , test net\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	filter := NewIPFilter()
	if _, err := filter.LoadFile(path); err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if filter.Path() != path {
		t.Fatalf("path=%q want %q", filter.Path(), path)
	}
	addr := tcpAddrStub{ip: "203.0.113.9"}
	if !filter.IsFilteredAddr(addr) {
		t.Fatal("tcp addr in blocked range must be filtered")
	}
}

func TestSearchSpamRating(t *testing.T) {
	filter := NewSpamFilter()
	disguised := filter.Rate("Some.Movie.2024.1080p.mkv.exe", 2*1024*1024*1024, 10)
	if !disguised.Spam || disguised.Score > 2 {
		t.Fatalf("double extension must be spam: %+v", disguised)
	}
	clean := filter.Rate("ubuntu-24.04-desktop-amd64.iso", 5*1024*1024*1024, 20)
	if clean.Spam || clean.Score < 4 {
		t.Fatalf("well seeded iso should rate high: %+v", clean)
	}
	tiny := filter.Rate("track.mp3", 4096, 1)
	if !tiny.Spam {
		t.Fatalf("4 KiB mp3 must be spam: %+v", tiny)
	}

	filter.AddWord("password")
	keyword := filter.Rate("passwords-collection.txt", 1024*1024, 1)
	if keyword.Score >= 3 || len(keyword.Reasons) == 0 {
		t.Fatalf("keyword hit must lower the score: %+v", keyword)
	}

	// Disabling the filter keeps the rating but stops dropping results.
	filter.SetEnabled(false)
	if filter.Enabled() {
		t.Fatal("filter should report disabled")
	}
}

func TestParseTextCollection(t *testing.T) {
	hashA := "26DFB3060428ACECE9EC8864DE7126AE"
	hashB := "0123456789ABCDEF0123456789ABCDEF"
	root := strings.Repeat("A", 32) // base32 of 20 zero bytes
	body := strings.Join([]string{
		"# a text collection",
		"ed2k://|file|a.avi|1024|" + hashA + "|/",
		"not a link at all",
		"ed2k://|file|b.bin|2048|" + hashB + "|h=" + root + "|/",
		"",
	}, "\n")
	collection, err := ParseCollection([]byte(body))
	if err != nil {
		t.Fatalf("ParseCollection: %v", err)
	}
	if !collection.Text {
		t.Fatal("text collection not marked as text")
	}
	if len(collection.Files) != 2 {
		t.Fatalf("parsed %d files, want 2", len(collection.Files))
	}
	if collection.Files[0].Name != "a.avi" || collection.Files[0].Size != 1024 {
		t.Fatalf("first entry = %+v", collection.Files[0])
	}
	if collection.Files[1].AICHRoot != root {
		t.Fatalf("AICH root = %q want %q", collection.Files[1].AICHRoot, root)
	}
	links := collection.Links()
	if len(links) != 2 || !strings.Contains(links[0], hashA) {
		t.Fatalf("links = %v", links)
	}
}

func TestParseBinaryCollection(t *testing.T) {
	hash := protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE")

	var buf bytes.Buffer
	// version 2
	_ = binary.Write(&buf, binary.LittleEndian, uint32(2))
	// one header tag: collection name
	_ = binary.Write(&buf, binary.LittleEndian, uint32(1))
	buf.Write(binStringTag(ftFileName, "My Collection"))
	// one file
	_ = binary.Write(&buf, binary.LittleEndian, uint32(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(3))
	buf.Write(binHashTag(ftFileHash, hash.Bytes()))
	buf.Write(binStringTag(ftFileName, "movie.mkv"))
	var size [4]byte
	binary.LittleEndian.PutUint32(size[:], 123456)
	buf.Write([]byte{tagTypeUint32 | 0x80, ftFileSize})
	buf.Write(size[:])

	collection, err := ParseCollection(buf.Bytes())
	if err != nil {
		t.Fatalf("ParseCollection: %v", err)
	}
	if collection.Text {
		t.Fatal("binary collection marked as text")
	}
	if collection.Name != "My Collection" {
		t.Fatalf("name = %q", collection.Name)
	}
	if len(collection.Files) != 1 {
		t.Fatalf("files = %d", len(collection.Files))
	}
	got := collection.Files[0]
	if !got.Hash.Equal(hash) || got.Name != "movie.mkv" || got.Size != 123456 {
		t.Fatalf("entry = %+v", got)
	}
}

func TestCreatePreviewWritesCompletePrefix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.mkv")
	content := make([]byte, PieceSize+4096)
	for i := range content {
		content[i] = byte(i % 251)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	session := NewSession(NewSettings())
	handler := disk.NewDesktopFileHandler(path)
	t.Cleanup(func() { _ = handler.Close() })
	transfer, err := NewTransfer(session, AddTransferParams{
		Hash:       protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE"),
		CreateTime: CurrentTime(),
		Size:       int64(len(content)),
		FilePath:   path,
		Handler:    handler,
	})
	if err != nil {
		t.Fatalf("NewTransfer: %v", err)
	}
	if _, err := transfer.CreatePreview(dir); err == nil {
		t.Fatal("preview without a complete leading part must fail")
	}
	transfer.picker.RestoreHave(0)
	if ready := transfer.PreviewReady(); ready != 1 {
		t.Fatalf("PreviewReady = %d want 1", ready)
	}
	info, err := transfer.CreatePreview(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatalf("CreatePreview: %v", err)
	}
	if info.Bytes != PieceSize || info.Parts != 1 || info.Complete {
		t.Fatalf("info = %+v", info)
	}
	if !strings.HasSuffix(info.Path, ".mkv") {
		t.Fatalf("preview path %q lost the extension", info.Path)
	}
	got, err := os.ReadFile(info.Path)
	if err != nil {
		t.Fatalf("read preview: %v", err)
	}
	if !bytes.Equal(got, content[:PieceSize]) {
		t.Fatalf("preview payload mismatch: %d bytes", len(got))
	}
}

// binStringTag encodes a tag whose value is an eD2k string (uint16 length).
func binStringTag(id uint8, value string) []byte {
	out := []byte{tagTypeString | 0x80, id, 0, 0}
	binary.LittleEndian.PutUint16(out[2:], uint16(len(value)))
	return append(out, value...)
}

// binHashTag encodes a 16-byte hash tag.
func binHashTag(id uint8, value []byte) []byte {
	out := []byte{tagTypeHash | 0x80, id}
	return append(out, value...)
}

// tcpAddrStub implements net.Addr with a TCP address shape.
type tcpAddrStub struct{ ip string }

func (a tcpAddrStub) Network() string { return "tcp" }
func (a tcpAddrStub) String() string  { return a.ip + ":4662" }

func mustIP(t *testing.T, value string) net.IP {
	t.Helper()
	parsed := net.ParseIP(value)
	if parsed == nil {
		t.Fatalf("bad ip %q", value)
	}
	return parsed
}
