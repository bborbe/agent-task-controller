// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
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

// removeTaskIdentifier removes any existing task_identifier line(s) from the
// frontmatter so that injectAndStore can safely prepend a fresh value.
func removeTaskIdentifier(content []byte) []byte {
	s := string(content)
	const prefix = "task_identifier:"
	var out []string
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimRight(line, "\r")
		if strings.HasPrefix(trimmed, prefix) {
			continue
		}
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n"))
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
