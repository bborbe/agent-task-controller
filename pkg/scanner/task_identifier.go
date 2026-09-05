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
	s := string(content)
	lines := strings.Split(s, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r") != "---" {
		return content
	}
	closing := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == "---" {
			closing = i
			break
		}
	}
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
		indent := leadingWhitespaceLen(line)
		for j := i + 1; j < closing && leadingWhitespaceLen(strings.TrimRight(lines[j], "\r")) > indent; j++ {
			remove[j] = true
		}
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
