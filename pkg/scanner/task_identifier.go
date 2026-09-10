// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"strings"

	"github.com/bborbe/errors"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// isValidUUID returns true if s can be parsed as a valid UUID.
func isValidUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

// isIdentifierUnique returns true if no other file in v.hashes uses the same task identifier.
func (v *vaultScanner) isIdentifierUnique(id string, relPath string) bool {
	for path, entry := range v.hashes {
		if path != relPath && string(entry.taskIdentifier) == id {
			return false
		}
	}
	return true
}

// taskIdentifierKeyLines parses the frontmatter region (content lines
// 1..closing-1) with yaml.v3 into a node tree and returns the content line index
// of every top-level key whose parsed Value is task_identifier.
//
// The boolean result is false — and the caller must leave content unchanged —
// when the region cannot be parsed, when the top-level node is not a mapping, or
// when the top-level mapping is in flow style: a flow mapping carries sibling
// keys on the same line, so removing that line would over-delete, and the
// spec-009 convergence guard bounds that shape instead.
//
// The region is parsed with trailing CR stripped per line so CRLF files resolve
// the same as LF files; stripping never removes a line, so a key node's 1-based
// yaml.v3 Line still equals its content line index (body line 1 == content index
// 1). Decoding into a yaml.Node keeps duplicate keys in the node's Content, so
// every spelling of a repeated key is reported.
func taskIdentifierKeyLines(lines []string, closing int) ([]int, bool) {
	body := make([]string, 0, closing-1)
	for i := 1; i < closing; i++ {
		body = append(body, strings.TrimRight(lines[i], "\r"))
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(strings.Join(body, "\n")), &doc); err != nil {
		return nil, false
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, false
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode || root.Style&yaml.FlowStyle != 0 {
		return nil, false
	}
	var out []int
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i]
		if key.Value != "task_identifier" {
			continue
		}
		out = append(out, key.Line)
	}
	return out, true
}

// removeTaskIdentifier removes every task_identifier key line from the
// frontmatter region of content, together with the full indentation span of
// each key's value (block sequences, block mappings, and block scalars), so
// injectAndStore can safely prepend a fresh value. Lines outside the frontmatter
// region — including a body line beginning task_identifier: — are preserved
// byte-for-byte.
//
// Keys are resolved by parsing the frontmatter region with yaml.v3, the same
// way the read path resolves them, rather than by matching literal key text:
// every spelling YAML accepts — bare, double-quoted, single-quoted,
// whitespace-before-colon, and escaped characters inside a quoted key, e.g.
// "task_identifier" — resolves to the same parsed key and is removed.
func removeTaskIdentifier(content []byte) []byte {
	lines := strings.Split(string(content), "\n")
	closing := frontmatterClosingIndex(lines)
	if closing == -1 {
		return content
	}
	keyLines, ok := taskIdentifierKeyLines(lines, closing)
	if !ok {
		return content
	}
	remove := make([]bool, len(lines))
	for _, i := range keyLines {
		remove[i] = true
		markValueSpan(lines, i+1, closing, leadingWhitespaceLen(lines[i]), remove)
	}
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		if remove[i] {
			continue
		}
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n"))
}

// frontmatterClosingIndex returns the index of the frontmatter's closing ---
// line, or -1 when content has no opening delimiter or is unterminated.
func frontmatterClosingIndex(lines []string) int {
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r") != "---" {
		return -1
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == "---" {
			return i
		}
	}
	return -1
}

// markValueSpan marks every line of a block value belonging to a key indented
// by indent, starting at from and stopping before closing.
//
// A blank line must never terminate the span: yaml.v3 emits genuinely empty
// lines for blanks inside | and > block scalars and between block-sequence
// items, and a zero-length line would otherwise fail the indentation test and
// orphan the rest of the value. Blank lines are therefore held as pending and
// only committed to the removal set once a more-indented non-blank line
// follows. Pending blanks left over when the span ends are trimmed back out,
// so a separator blank line sitting before the next top-level key survives.
func markValueSpan(lines []string, from int, closing int, indent int, remove []bool) {
	var pending []int
	for j := from; j < closing; j++ {
		line := strings.TrimRight(lines[j], "\r")
		if isBlankLine(line) {
			pending = append(pending, j)
			continue
		}
		if leadingWhitespaceLen(line) <= indent {
			return
		}
		for _, p := range pending {
			remove[p] = true
		}
		pending = pending[:0]
		remove[j] = true
	}
}

// isBlankLine returns true if line is empty or contains only spaces and tabs.
func isBlankLine(line string) bool {
	return strings.TrimLeft(line, " \t") == ""
}

// leadingWhitespaceLen returns the number of leading spaces/tabs in line.
func leadingWhitespaceLen(line string) int {
	n := 0
	for n < len(line) && (line[n] == ' ' || line[n] == '\t') {
		n++
	}
	return n
}

// InjectTaskIdentifier injects a task_identifier into the frontmatter of content.
func InjectTaskIdentifier(ctx context.Context, content []byte, id string) ([]byte, error) {
	s := string(content)
	if strings.HasPrefix(s, "---\r\n") {
		return []byte("---\r\ntask_identifier: " + id + "\r\n" + s[5:]), nil
	}
	if strings.HasPrefix(s, "---\n") {
		return []byte("---\ntask_identifier: " + id + "\n" + s[4:]), nil
	}
	return nil, errors.Errorf(
		ctx,
		"content does not start with frontmatter delimiter",
	)
}
