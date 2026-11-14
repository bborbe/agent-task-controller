// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"

	lib "github.com/bborbe/agent"
	"gopkg.in/yaml.v3"
)

// ParseTask parses a task file's frontmatter and body into a Task. Returns nil
// when the content has no parseable task frontmatter with a task_identifier.
// Unlike processFile it performs no hash tracking, no auto-injection, and no
// counter reset — a pure parse for read-only consumers (e.g. the redrive sweep).
func ParseTask(ctx context.Context, content []byte) *lib.Task {
	fmYAML, err := extractFrontmatter(ctx, content)
	if err != nil {
		return nil
	}
	dedupedYAML, _, dedupErr := DeduplicateFrontmatter(ctx, fmYAML)
	if dedupErr != nil {
		return nil
	}
	var fmMap map[string]interface{}
	if err := yaml.Unmarshal([]byte(dedupedYAML), &fmMap); err != nil {
		return nil
	}
	taskID, _ := fmMap["task_identifier"].(string)
	if taskID == "" {
		return nil
	}
	return ParseTaskFields(fmMap, extractBody(content))
}

// ParseTaskFields builds a Task from an already-split frontmatter map and body.
//
// It exists for callers that never hold the file's bytes: the typed vault API
// returns a task as frontmatter and body, so re-rendering it to `---\n<yaml>---`
// just to call ParseTask would round-trip YAML for every task on every pass —
// and the redrive sweep, which exists precisely to bypass the hash cache, walks
// the whole population each cycle.
//
// It shares ParseTask's field mapping by construction (ParseTask delegates here),
// so the two cannot drift: a field added to one is added to both. The
// frontmatter is already a map, so the duplicate-key repair ParseTask performs on
// YAML text has nothing left to do.
func ParseTaskFields(fm map[string]any, body string) *lib.Task {
	if fm == nil {
		return nil
	}
	taskID, _ := fm["task_identifier"].(string)
	if taskID == "" {
		return nil
	}
	return &lib.Task{
		TaskIdentifier: lib.TaskIdentifier(taskID),
		Frontmatter:    lib.TaskFrontmatter(fm),
		Content:        lib.TaskContent(body),
	}
}
