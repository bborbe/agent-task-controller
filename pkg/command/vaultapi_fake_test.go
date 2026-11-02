// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

// newFSTaskClient returns a vaultapi.Client backed by the same temp directory
// the git fake used, so every existing assertion about file content keeps
// working: the client addresses tasks by identifier, but the bytes on disk are
// what the specs read back.
//
// ReadTask, PatchTask and PutBody are wired — the three calls the ported
// frontmatter executors make. The rest panic rather than silently returning zero
// values, so a ported call site that starts using them fails loudly instead of
// quietly behaving as if the vault were empty.
func newFSTaskClient(root, taskDir string) *mocks.VaultAPIClient {
	fake := &mocks.VaultAPIClient{}
	dir := filepath.Join(root, taskDir)

	fake.ReadTaskStub = func(_ context.Context, id string) (*vaultapi.Task, error) {
		return readFSTask(dir, id)
	}
	fake.PatchTaskStub = func(
		_ context.Context,
		id string,
		fields map[string]any,
	) (*vaultapi.Task, error) {
		return patchFSTask(dir, id, fields)
	}
	fake.CreateTaskStub = func(
		_ context.Context,
		t vaultapi.Task,
		opts vaultapi.CreateOptions,
	) (*vaultapi.Task, error) {
		return createFSTask(dir, t, opts)
	}
	fake.PutBodyStub = func(_ context.Context, id string, body string) error {
		return putFSBody(dir, id, body)
	}
	fake.ListTasksStub = func(context.Context, vaultapi.Filter) ([]vaultapi.Task, error) {
		panic("newFSTaskClient: ListTasks is not wired for this executor")
	}
	fake.ReaddressTaskStub = func(context.Context, string, string) (*vaultapi.Task, error) {
		panic("newFSTaskClient: ReaddressTask is not wired for this executor")
	}

	return fake
}

// createFSTask mirrors the API's create semantics over the same temp dir: the
// title names the file, an occupant is a conflict unless it is terminal and the
// caller asked to reopen, and a reopen REPLACES the frontmatter and body rather
// than merging them.
func createFSTask(
	dir string,
	t vaultapi.Task,
	opts vaultapi.CreateOptions,
) (*vaultapi.Task, error) {
	path := filepath.Join(dir, t.Title+".md")
	reopened := false
	if existing, err := os.ReadFile(path); err == nil { // #nosec G304 -- test helper
		fm, _, splitErr := splitTaskFile(string(existing))
		var status, identifier string
		if splitErr == nil {
			status, _ = fm["status"].(string)
			identifier, _ = fm["task_identifier"].(string)
		}
		// An occupant the server cannot parse holds the path (fail closed), so an
		// unreadable file reports as a conflict with no status rather than as a
		// free slot.
		if !opts.ReopenTerminal || !vaultapi.IsTerminalStatus(status) {
			return nil, &vaultapi.AlreadyExistsError{
				Code:           "task_exists",
				TaskIdentifier: identifier,
				Status:         status,
			}
		}
		reopened = true
	}
	marshaled, err := yaml.Marshal(map[string]any(t.Frontmatter))
	if err != nil {
		return nil, err
	}
	content := "---\n" + string(marshaled) + "---\n" + t.Body
	if err := os.WriteFile(path, []byte(content), 0600); err != nil { // #nosec G306 -- test helper
		return nil, err
	}
	created := taskFromFrontmatter(
		fmt.Sprint(t.Frontmatter["task_identifier"]),
		map[string]any(t.Frontmatter),
		t.Body,
	)
	created.Title = t.Title
	created.Reopened = reopened
	return created, nil
}

// readFSTask finds the file whose frontmatter carries id and returns it.
func readFSTask(dir, id string) (*vaultapi.Task, error) {
	path, ok := findFSTaskPath(dir, id)
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
	return taskFromFrontmatter(id, fm, body), nil
}

// patchFSTask merges fields into the task's frontmatter and rewrites the file
// in the same `---\n<yaml>---\n<body>` shape the controller writes.
func patchFSTask(dir, id string, fields map[string]any) (*vaultapi.Task, error) {
	path, ok := findFSTaskPath(dir, id)
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
	return taskFromFrontmatter(id, fm, body), nil
}

// putFSBody replaces the task's body, leaving frontmatter untouched — the same
// split the typed surface makes between PATCH (fields) and PUT (body).
func putFSBody(dir, id, body string) error {
	path, ok := findFSTaskPath(dir, id)
	if !ok {
		return vaultapi.ErrNotFound
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- test helper
	if err != nil {
		return err
	}
	fm, _, err := splitTaskFile(string(raw))
	if err != nil {
		return err
	}
	marshaled, err := yaml.Marshal(fm)
	if err != nil {
		return err
	}
	updated := "---\n" + string(marshaled) + "---\n" + body
	return os.WriteFile(path, []byte(updated), 0600) // #nosec G306 -- test helper
}

// findFSTaskPath returns the file whose task_identifier equals id.
func findFSTaskPath(dir, id string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if taskIdentifierAt(path) == id {
			return path, true
		}
	}
	return "", false
}

// taskIdentifierAt reads one file's task_identifier, or "" when it cannot.
func taskIdentifierAt(path string) string {
	raw, err := os.ReadFile(path) // #nosec G304 -- test helper
	if err != nil {
		return ""
	}
	fm, _, err := splitTaskFile(string(raw))
	if err != nil {
		return ""
	}
	identifier, _ := fm["task_identifier"].(string)
	return identifier
}

// taskFromFrontmatter builds the client's view of one task.
func taskFromFrontmatter(id string, fm map[string]any, body string) *vaultapi.Task {
	title, _ := fm["title"].(string)
	return &vaultapi.Task{ID: id, Title: title, Frontmatter: fm, Body: body}
}

// splitTaskFile separates a task file into its frontmatter map and its body.
func splitTaskFile(content string) (map[string]any, string, error) {
	if !strings.HasPrefix(content, "---\n") {
		return nil, "", vaultapi.ErrNotFound
	}
	head, body, found := strings.Cut(content[4:], "---\n")
	if !found {
		return nil, "", vaultapi.ErrNotFound
	}
	fm := map[string]any{}
	if err := yaml.Unmarshal([]byte(head), &fm); err != nil {
		return nil, "", err
	}
	return fm, body, nil
}
