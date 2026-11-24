package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveED2KPreservesOtherSectionsAndComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := "# keep me\nlisten: \"127.0.0.1:8080\"\ned2k:\n  enabled: false # toggle later\n  nodesDat: \"old.dat\"\n  # hand-edited note\n  unknown: 7\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Defaults().ED2K
	cfg.Enabled = true
	cfg.NodesDat = "/tmp/nodes.dat"
	if err := SaveED2K(path, cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"# keep me", "127.0.0.1:8080", "hand-edited note", "unknown: 7", `nodesDat: /tmp/nodes.dat`, "enabled: true"} {
		if !strings.Contains(text, want) {
			t.Errorf("saved config missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "old.dat") {
		t.Errorf("stale value survived:\n%s", text)
	}
}

func TestManagerPatchPersistsED2KOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("listen: \"127.0.0.1:9999\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewManager(Defaults(), nil)
	m.SetPersistPath(path)
	if _, err := m.Patch(map[string]any{
		"global.maxConcurrentTasks": 3,
		"ed2k.enabled":              true,
		"ed2k.serverMet":            "~/server.met",
	}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	text := string(raw)
	if !strings.Contains(text, "enabled: true") || !strings.Contains(text, "serverMet:") {
		t.Errorf("ed2k settings not persisted:\n%s", text)
	}
	if strings.Contains(text, "maxConcurrentTasks") {
		t.Errorf("runtime-only key leaked into config:\n%s", text)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.ED2K.Enabled || !strings.HasSuffix(loaded.ED2K.ServerMet, "server.met") {
		t.Fatalf("reloaded ed2k = %+v", loaded.ED2K)
	}
}
