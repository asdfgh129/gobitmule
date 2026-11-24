package ed2ktransport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/core"
)

// Offline tests for Goed2kDownloader. A fake engine/handle injects a real temp
// file, so the full Open path is exercised with ZERO network (docs/12 §1).

type fakeHandle struct {
	file     *os.File
	finished bool
}

func (h *fakeHandle) IsFinished() bool  { return h.finished }
func (h *fakeHandle) GetFile() *os.File { return h.file }

type fakeEngine struct {
	startErr  error
	nodesErr  error
	serverErr error
	addErr    error
	waitErr   error
	waitBlock chan struct{} // if non-nil, Wait blocks until closed
	handle    transferHandle
	stopped   bool
	addedLink string
}

func (e *fakeEngine) Start() error                    { return e.startErr }
func (e *fakeEngine) LoadDHTNodesDat(...string) error { return e.nodesErr }
func (e *fakeEngine) ConnectServerMet(string) error   { return e.serverErr }
func (e *fakeEngine) Wait() error {
	if e.waitBlock != nil {
		<-e.waitBlock
	}
	return e.waitErr
}
func (e *fakeEngine) Stop() error { e.stopped = true; return nil }
func (e *fakeEngine) AddLink(link, _ string) (transferHandle, string, error) {
	e.addedLink = link
	return e.handle, "", e.addErr
}

func completedHandle(t *testing.T, body []byte) (*fakeHandle, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "done.bin")
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() }) // Windows: release handle before TempDir cleanup
	return &fakeHandle{file: f, finished: true}, p
}

func TestGoed2kOpenEndToEnd(t *testing.T) {
	h, _ := completedHandle(t, []byte("0123456789abcdef"))
	eng := &fakeEngine{handle: h}
	dl := newGoed2kDownloaderWithFactory(
		Goed2kConfig{OutputDir: t.TempDir()},
		func() (engine, error) { return eng, nil },
	)
	r := &core.Resource{
		Kind:     core.KindED2K,
		Files:    []core.ResourceFile{{Path: "test file.bin", Size: 16, Selected: true}},
		Identity: core.ProtocolIdentity{ED2KFileHash: strings.Repeat("ab", 16)},
	}
	reader, size, err := dl.Open(context.Background(), r)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reader.Close()
	if size != 16 {
		t.Fatalf("size=%d want 16", size)
	}
	buf := make([]byte, 16)
	if _, err := reader.Read(buf); err != nil || string(buf) != "0123456789abcdef" {
		t.Fatalf("read=%q (%v)", buf, err)
	}
	if !strings.Contains(eng.addedLink, "test+file.bin") || !strings.Contains(eng.addedLink, "|16|") {
		t.Fatalf("unexpected ed2k link: %q", eng.addedLink)
	}
	if !eng.stopped {
		t.Fatal("engine must be stopped after transfer")
	}
}

func TestGoed2kOpenErrors(t *testing.T) {
	cases := []struct {
		name string
		eng  *fakeEngine
		want string
	}{
		{"start", &fakeEngine{startErr: errors.New("bind fail")}, "start failed"},
		{"addlink", &fakeEngine{addErr: errors.New("bad link")}, "AddLink failed"},
		{"wait", &fakeEngine{waitErr: errors.New("wait fail")}, "wait failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dl := newGoed2kDownloaderWithFactory(Goed2kConfig{OutputDir: t.TempDir()},
				func() (engine, error) { return tc.eng, nil })
			r := &core.Resource{Kind: core.KindED2K,
				Files:    []core.ResourceFile{{Path: "x.bin", Size: 1, Selected: true}},
				Identity: core.ProtocolIdentity{ED2KFileHash: strings.Repeat("ab", 16)}}
			if _, _, err := dl.Open(context.Background(), r); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestGoed2kOpenInitAndBootstrapErrors(t *testing.T) {
	dl := newGoed2kDownloaderWithFactory(
		Goed2kConfig{OutputDir: t.TempDir(), NodesDat: "nodes.dat", ServerMet: "server.met"},
		func() (engine, error) { return &fakeEngine{nodesErr: errors.New("bad nodes")}, nil })
	r := &core.Resource{Kind: core.KindED2K,
		Files:    []core.ResourceFile{{Path: "x.bin", Size: 1, Selected: true}},
		Identity: core.ProtocolIdentity{ED2KFileHash: strings.Repeat("ab", 16)}}
	if _, _, err := dl.Open(context.Background(), r); err == nil || !strings.Contains(err.Error(), "nodes.dat") {
		t.Fatalf("nodes error must surface, got %v", err)
	}
	dl2 := newGoed2kDownloaderWithFactory(
		Goed2kConfig{OutputDir: t.TempDir(), ServerMet: "s.met"},
		func() (engine, error) { return &fakeEngine{serverErr: errors.New("bad server")}, nil })
	if _, _, err := dl2.Open(context.Background(), r); err == nil || !strings.Contains(err.Error(), "server.met") {
		t.Fatalf("server error must surface, got %v", err)
	}
}

func TestGoed2kOpenFactoryAndConfigGuards(t *testing.T) {
	r := &core.Resource{Kind: core.KindED2K,
		Files:    []core.ResourceFile{{Path: "x.bin", Size: 1, Selected: true}},
		Identity: core.ProtocolIdentity{ED2KFileHash: strings.Repeat("ab", 16)}}
	if _, _, err := (*Goed2kDownloader)(nil).Open(context.Background(), r); err == nil {
		t.Fatal("nil downloader must fail")
	}
	if _, _, err := newGoed2kDownloaderWithFactory(Goed2kConfig{}, nil).Open(context.Background(), r); err == nil {
		t.Fatal("nil factory must fail")
	}
	if _, _, err := newGoed2kDownloaderWithFactory(Goed2kConfig{},
		func() (engine, error) { return &fakeEngine{}, nil }).Open(context.Background(), r); err == nil ||
		!strings.Contains(err.Error(), "output directory") {
		t.Fatal("missing output dir must fail")
	}
	if _, _, err := newGoed2kDownloaderWithFactory(Goed2kConfig{OutputDir: t.TempDir()},
		func() (engine, error) { return nil, errors.New("factory boom") },
	).Open(context.Background(), r); err == nil || !strings.Contains(err.Error(), "init failed") {
		t.Fatal("factory error must surface")
	}
	badHash := &core.Resource{Kind: core.KindED2K,
		Files: []core.ResourceFile{{Path: "x.bin", Size: 1, Selected: true}}}
	if _, _, err := newGoed2kDownloaderWithFactory(Goed2kConfig{OutputDir: t.TempDir()},
		func() (engine, error) { return &fakeEngine{}, nil }).Open(context.Background(), badHash); err == nil {
		t.Fatal("missing MD4 hash must fail")
	}
}

func TestGoed2kOpenNotFinishedAndNoFile(t *testing.T) {
	h, _ := completedHandle(t, []byte("x"))
	h.finished = false
	dl := newGoed2kDownloaderWithFactory(Goed2kConfig{OutputDir: t.TempDir()},
		func() (engine, error) { return &fakeEngine{handle: h}, nil })
	r := &core.Resource{Kind: core.KindED2K,
		Files:    []core.ResourceFile{{Path: "x.bin", Size: 1, Selected: true}},
		Identity: core.ProtocolIdentity{ED2KFileHash: strings.Repeat("ab", 16)}}
	if _, _, err := dl.Open(context.Background(), r); err == nil || !strings.Contains(err.Error(), "not finished") {
		t.Fatalf("not-finished must fail, got %v", err)
	}
	dl2 := newGoed2kDownloaderWithFactory(Goed2kConfig{OutputDir: t.TempDir()},
		func() (engine, error) { return &fakeEngine{handle: &fakeHandle{finished: true}}, nil })
	if _, _, err := dl2.Open(context.Background(), r); err == nil || !strings.Contains(err.Error(), "no reader") {
		t.Fatalf("nil file must fail, got %v", err)
	}
}

func TestGoed2kOpenRespectsCancellation(t *testing.T) {
	h, _ := completedHandle(t, []byte("x"))
	block := make(chan struct{})
	defer close(block)
	eng := &fakeEngine{handle: h, waitBlock: block}
	dl := newGoed2kDownloaderWithFactory(Goed2kConfig{OutputDir: t.TempDir()},
		func() (engine, error) { return eng, nil })
	r := &core.Resource{Kind: core.KindED2K,
		Files:    []core.ResourceFile{{Path: "x.bin", Size: 1, Selected: true}},
		Identity: core.ProtocolIdentity{ED2KFileHash: strings.Repeat("ab", 16)}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := dl.Open(ctx, r); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ctx must abort, got %v", err)
	}
	if !eng.stopped {
		t.Fatal("engine must be stopped on cancellation")
	}
}

func TestBuildFileLink(t *testing.T) {
	got := buildFileLink("a b&c.bin", 123, strings.ToUpper(strings.Repeat("ab", 16)))
	if !strings.HasPrefix(got, "ed2k://|file|") || !strings.HasSuffix(got, "|/") {
		t.Fatalf("bad link shape: %q", got)
	}
	if !strings.Contains(got, "|123|"+strings.Repeat("ab", 16)) {
		t.Fatalf("size/hash not canonical: %q", got)
	}
	if strings.Contains(got, "a b&c") {
		t.Fatalf("name must be escaped: %q", got)
	}
}
