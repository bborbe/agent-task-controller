// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"regexp"
	"strings"

	"github.com/bborbe/errors"
	"github.com/google/uuid"
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

// taskIdentifierKeyLine matches any frontmatter line whose key resolves to
// task_identifier under the spellings YAML accepts: bare, double-quoted,
// single-quoted, or with whitespace before the colon.
var taskIdentifierKeyLine = regexp.MustCompile(`^\s*['"]?task_identifier['"]?\s*:`)

// removeTaskIdentifier removes every task_identifier key line from the
// frontmatter region of content, together with the full indentation span of
// each key's value (block sequences, block mappings, and block scalars), so
// injectAndStore can safely prepend a fresh value. Lines outside the frontmatter
// region — including a body line beginning task_identifier: — are preserved
// byte-for-byte.
func removeTaskIdentifier(content []byte) []byte {
	lines := strings.Split(string(content), "\n")
	closing := frontmatterClosingIndex(lines)
	if closing == -1 {
		return content
	}
	remove := make([]bool, len(lines))
	for i := 1; i < closing; i++ {
		line := strings.TrimRight(lines[i], "\r")
		if !taskIdentifierKeyLine.MatchString(line) {
			continue
		}
		remove[i] = true
		markValueSpan(lines, i+1, closing, leadingWhitespaceLen(line), remove)
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
