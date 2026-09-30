package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

// WriteTargets replaces the targets section with an explicit include list of
// chat ids (titles become comments) and leaves every other section and its
// comments as they are.
func WriteTargets(path string, ids []int64, titles map[int64]string) error {
	if len(ids) == 0 {
		return errors.New("no chats selected")
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		data = []byte("archive_dir: ~/TelegramArchive\n")
	case err != nil:
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: top level is not a mapping", path)
	}
	targets := targetsNode(ids, titles)
	replaced := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "targets" {
			root.Content[i+1] = targets
			replaced = true
		}
	}
	if !replaced {
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "targets"}, targets)
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
	return replaceFile(path, buf.Bytes())
}

func targetsNode(ids []int64, titles map[int64]string) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, id := range ids {
		n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(id, 10)}
		if title := commentText(titles[id]); title != "" {
			n.LineComment = "# " + title
		}
		seq.Content = append(seq.Content, n)
	}
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "include"}, seq,
	}}
}

// commentText makes a chat title safe for a YAML line comment: newlines would
// break out of it, and control characters or invalid UTF-8 make the YAML
// invalid or crash the encoder. The zero-width joiner is kept for emoji.
func commentText(title string) string {
	title = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsGraphic(r) || r == '\u200d':
			return r
		}
		return -1
	}, strings.ToValidUTF8(title, ""))
	return strings.Join(strings.Fields(title), " ")
}

// replaceFile validates the new config from a temp file next to path and only
// then renames it over path, so a bad or half-written config never replaces a good one.
func replaceFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if _, err := Load(tmp.Name(), "/"); err != nil {
		return fmt.Errorf("refusing to write an invalid config: %w", err)
	}
	return os.Rename(tmp.Name(), path)
}
