package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

// Selected is what setup writes into the targets section.
type Selected struct {
	Chats         []int64
	ChatTitles    map[int64]string
	ExcludeTopics map[int64][]int
	TopicTitles   map[int64]map[int]string
}

// WriteTargets replaces the targets section with an explicit include list of
// chat ids and topic exclusions (titles become comments) and leaves every
// other section and its comments as they are.
func WriteTargets(path string, sel Selected) error {
	if len(sel.Chats) == 0 {
		return errors.New("no chats selected")
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
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
	targets := targetsNode(sel)
	replaced := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "targets" {
			root.Content[i+1] = targets
			replaced = true
		}
	}
	if !replaced {
		root.Content = append(root.Content, strNode("targets"), targets)
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

func targetsNode(sel Selected) *yaml.Node {
	include := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	excluded := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, id := range sel.Chats {
		n := intNode(id)
		if title := commentText(sel.ChatTitles[id]); title != "" {
			n.LineComment = "# " + title
		}
		include.Content = append(include.Content, n)

		topics := slices.Sorted(slices.Values(sel.ExcludeTopics[id]))
		if len(topics) == 0 {
			continue
		}
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
		var names []string
		for _, tp := range topics {
			seq.Content = append(seq.Content, intNode(int64(tp)))
			if name := commentText(sel.TopicTitles[id][tp]); name != "" {
				names = append(names, name)
			}
		}
		// The comment goes on the sequence: yaml.v3 drops line comments on mapping keys.
		if chat := commentText(sel.ChatTitles[id]); chat != "" || len(names) > 0 {
			seq.LineComment = "# " + strings.TrimPrefix(chat+": "+strings.Join(names, ", "), ": ")
		}
		excluded.Content = append(excluded.Content, intNode(id), seq)
	}
	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{strNode("include"), include}}
	if len(excluded.Content) > 0 {
		out.Content = append(out.Content, strNode("exclude_topics"), excluded)
	}
	return out
}

func intNode(v int64) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(v, 10)}
}

func strNode(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }

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
	if err := tmp.Sync(); err != nil {
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
