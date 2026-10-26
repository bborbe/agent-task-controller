// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
	"gopkg.in/yaml.v3"
)

// newFSTaskClient returns a vaultapi.Client backed by the same temp directory
// the git fake used, so every existing assertion about file content keeps
// working: the client addresses tasks by identifier, but the bytes on disk are
// what the specs read back.
//
// Only ReadTask and PatchTask are wired — they are the two calls the increment
// executor makes. The rest panic rather than silently returning zero values,
// so a ported call site that starts using them fails loudly instead of quietly
// behaving as if the vault were empty.
func newFSTaskClient(root, taskDir string) *mocks.VaultAPIClient {
	fake := &mocks.VaultAPIClient{}

	findPath := func(id string) (string, bool) {
		entries, err := os.ReadDir(filepath.Join(root, taskDir))
		if err != nil {
			return "", false
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			path := filepath.Join(root, taskDir, entry.Name())
			raw, readErr := os.ReadFile(path) // #nosec G304 -- test helper
			if readErr != nil {
				continue
			}
			fm, _, splitErr := splitTaskFile(string(raw))
			if splitErr != nil {
				continue
			}
			if got, _ := fm["task_identifier"].(string); got == id {
				return path, true
			}
		}
		return "", false
	}

	fake.ReadTaskStub = func(_ context.Context, id string) (*vaultapi.Task, error) {
		path, ok := findPath(id)
		if !ok {
			return nil, vaultapi.ErrNotFound
		}
		raw, err := os.ReadFile(path) // #nosec G304 -- test helper
		if err != nil {
			return nil, err
		}
		fm, body, err := splitTaskFile(string(raw))
		if err != nil {
			return nil, err
		}
		title, _ := fm["title"].(string)
		return &vaultapi.Task{ID: id, Title: title, Frontmatter: fm, Body: body}, nil
	}

	fake.PatchTaskStub = func(
		_ context.Context,
		id string,
		fields map[string]any,
	) (*vaultapi.Task, error) {
		path, ok := findPath(id)
		if !ok {
			return nil, vaultapi.ErrNotFound
		}
		raw, err := os.ReadFile(path) // #nosec G304 -- test helper
		if err != nil {
			return nil, err
		}
		fm, body, err := splitTaskFile(string(raw))
		if err != nil {
			return nil, err
		}
		for key, value := range fields {
			fm[key] = value
		}
		marshaled, err := yaml.Marshal(fm)
		if err != nil {
			return nil, err
		}
		updated := "---\n" + string(marshaled) + "---\n" + body
		if err := os.WriteFile(path, []byte(updated), 0600); err != nil { // #nosec G306 -- test helper
			return nil, err
		}
		title, _ := fm["title"].(string)
		return &vaultapi.Task{ID: id, Title: title, Frontmatter: fm, Body: body}, nil
	}

	fake.CreateTaskStub = func(context.Context, vaultapi.Task) (*vaultapi.Task, error) {
		panic("newFSTaskClient: CreateTask is not wired for this executor")
	}
	fake.PutBodyStub = func(context.Context, string, string) error {
		panic("newFSTaskClient: PutBody is not wired for this executor")
	}
	fake.ListTasksStub = func(context.Context, vaultapi.Filter) ([]vaultapi.Task, error) {
		panic("newFSTaskClient: ListTasks is not wired for this executor")
	}

	return fake
}

// splitTaskFile separates a task file into its frontmatter map and its body,
// matching the `---\n<yaml>---\n<body>` shape the controller writes.
func splitTaskFile(content string) (map[string]any, string, error) {
	if !strings.HasPrefix(content, "---\n") {
		return nil, "", vaultapi.ErrNotFound
	}
	rest := content[4:]
	head, body, found := strings.Cut(rest, "---\n")
	if !found {
		return nil, "", vaultapi.ErrNotFound
	}
	fm := map[string]any{}
	if err := yaml.Unmarshal([]byte(head), &fm); err != nil {
		return nil, "", err
	}
	return fm, body, nil
}
