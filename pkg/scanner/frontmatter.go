// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"strings"

	"github.com/bborbe/errors"
	"gopkg.in/yaml.v3"
)

// DeduplicateFrontmatter removes duplicate keys from YAML frontmatter, keeping the last value for each key.
func DeduplicateFrontmatter(ctx context.Context, fmYAML string) (string, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(fmYAML), &doc); err != nil {
		return fmYAML, false, errors.Wrapf(ctx, err, "parse frontmatter for deduplication")
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return fmYAML, false, nil
	}
	mappingNode := doc.Content[0]
	if mappingNode.Kind != yaml.MappingNode {
		return fmYAML, false, nil
	}
	seen := make(map[string]int)
	var newContent []*yaml.Node
	hasDuplicates := false
	for i := 0; i+1 < len(mappingNode.Content); i += 2 {
		keyNode := mappingNode.Content[i]
		valNode := mappingNode.Content[i+1]
		key := keyNode.Value
		if idx, ok := seen[key]; ok {
			hasDuplicates = true
			newContent[idx+1] = valNode
		} else {
			seen[key] = len(newContent)
			newContent = append(newContent, keyNode, valNode)
		}
	}
	if !hasDuplicates {
		return fmYAML, false, nil
	}
	mappingNode.Content = newContent
	out, err := yaml.Marshal(mappingNode)
	if err != nil {
		return fmYAML, false, errors.Wrapf(ctx, err, "re-marshal deduplicated frontmatter")
	}
	return string(out), true, nil
}

func extractFrontmatter(ctx context.Context, content []byte) (string, error) {
	s := string(content)
	const delim = "---"
	if !strings.HasPrefix(s, delim) {
		return "", errors.Errorf(ctx, "no frontmatter delimiter found")
	}
	rest := strings.TrimPrefix(s, delim)
	if strings.HasPrefix(rest, "\r\n") {
		rest = rest[2:]
	} else if strings.HasPrefix(rest, "\n") {
		rest = rest[1:]
	}
	idx := strings.Index(rest, "\n---")
	if idx == -1 {
		return "", errors.Errorf(ctx, "frontmatter not closed")
	}
	return rest[:idx], nil
}

func extractBody(content []byte) string {
	s := string(content)
	const delim = "---"
	if !strings.HasPrefix(s, delim) {
		return s
	}
	rest := strings.TrimPrefix(s, delim)
	if strings.HasPrefix(rest, "\r\n") {
		rest = rest[2:]
	} else if strings.HasPrefix(rest, "\n") {
		rest = rest[1:]
	}
	idx := strings.Index(rest, "\n---")
	if idx == -1 {
		return s
	}
	after := rest[idx+4:] // skip "\n---"
	if strings.HasPrefix(after, "\r\n") {
		after = after[2:]
	} else if strings.HasPrefix(after, "\n") {
		after = after[1:]
	}
	return after
}
