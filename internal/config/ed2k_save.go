package config

// UI-managed eD2k settings are persisted by rewriting only the `ed2k:` mapping
// of config.yaml. A yaml.Node round-trip keeps comments, key order of unrelated
// sections and unknown keys intact; values coming from env/CLI are never
// copied into the file.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// SaveED2K writes cfg.ED2K into path, creating a minimal config file when the
// path does not exist yet. The write is atomic (temp file + rename).
func SaveED2K(path string, cfg ED2KConfig) error {
	if path == "" {
		return errors.New("config path is empty")
	}

	var doc yaml.Node
	if raw, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("parse %q: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %q: %w", path, err)
	}
	if len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{
			Kind: yaml.MappingNode, Tag: "!!map",
		}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("config %q must be a YAML mapping", path)
	}

	var section *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "ed2k" {
			section = root.Content[i+1]
			break
		}
	}
	if section == nil {
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "ed2k"},
			&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
		section = root.Content[len(root.Content)-1]
	} else if section.Kind != yaml.MappingNode {
		*section = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}

	rawED2K, err := yaml.Marshal(&cfg)
	if err != nil {
		return err
	}
	var encoded yaml.Node
	if err := yaml.Unmarshal(rawED2K, &encoded); err != nil {
		return err
	}
	generated := &encoded
	if generated.Kind == yaml.DocumentNode && len(generated.Content) > 0 {
		generated = generated.Content[0]
	}
	if generated.Kind != yaml.MappingNode {
		return errors.New("ed2k config does not encode as a YAML mapping")
	}
	for i := 0; i+1 < len(generated.Content); i += 2 {
		key := generated.Content[i].Value
		value := generated.Content[i+1]
		replaced := false
		for j := 0; j+1 < len(section.Content); j += 2 {
			if section.Content[j].Value == key {
				section.Content[j+1] = value
				replaced = true
				break
			}
		}
		if !replaced {
			section.Content = append(section.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
		}
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".ed2k-config-*.yaml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
