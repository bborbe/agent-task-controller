// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner_test

// NOTE: testVaultClient is a hand-written, file-backed vaultapi.Client rather
// than a Counterfeiter mock. The scanner tests seed real markdown files and
// assert on what the scanner read and wrote, so the double has to behave like a
// vault rather than return canned values. It also avoids the import cycle that
// importing mocks would create: mocks/ imports scanner for scanner.ScanResult.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

type testVaultClient struct {
	path string
	// listErr, readErr and patchErr let a test make one operation fail without
	// standing up a whole fake.
	listErr  error
	readErr  error
	patchErr error
}

func splitTaskContent(raw []byte) (map[string]any, string, error) {
	text := string(raw)
	if !strings.HasPrefix(text, "---\n") {
		return map[string]any{}, text, nil
	}
	rest := text[len("---\n"):]
	idx := strings.Index(rest, "\n---")
	if idx == -1 {
		return map[string]any{}, text, nil
	}
	frontmatter := rest[:idx]
	body := strings.TrimPrefix(rest[idx+len("\n---"):], "\n")
	var fields map[string]any
	if err := yaml.Unmarshal([]byte(frontmatter), &fields); err != nil {
		return nil, "", err
	}
	if fields == nil {
		fields = map[string]any{}
	}
	return fields, body, nil
}

func renderTaskContent(fields map[string]any, body string) ([]byte, error) {
	if fields == nil {
		fields = map[string]any{}
	}
	rendered, err := yaml.Marshal(fields)
	if err != nil {
		return nil, err
	}
	return []byte("---\n" + string(rendered) + "---\n" + body), nil
}

func (t *testVaultClient) taskAt(absPath string) (vaultapi.Task, error) {
	raw, err := os.ReadFile(absPath) // #nosec G304 -- test-only path
	if err != nil {
		return vaultapi.Task{}, err
	}
	fields, body, err := splitTaskContent(raw)
	if err != nil {
		return vaultapi.Task{}, err
	}
	id, _ := fields["task_identifier"].(string)
	title := strings.TrimSuffix(filepath.Base(absPath), ".md")
	return vaultapi.Task{ID: id, Title: title, Frontmatter: fields, Body: body}, nil
}

func (t *testVaultClient) tasks() ([]vaultapi.Task, error) {
	var tasks []vaultapi.Task
	err := filepath.Walk(t.path, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		task, readErr := t.taskAt(path)
		if readErr != nil {
			// A file the scanner could never parse is not a task; the real index
			// skips it too.
			return nil
		}
		tasks = append(tasks, task)
		return nil
	})
	return tasks, err
}

// pathFor returns the file the task actually lives in. It must walk rather than
// reconstruct the path from the title: a task file may sit in a subdirectory the
// title does not name, and a reconstructed path writes a second file beside it.
func (t *testVaultClient) pathFor(id string) (string, bool, error) {
	var found string
	err := filepath.Walk(t.path, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		task, readErr := t.taskAt(path)
		if readErr != nil {
			return nil
		}
		if task.ID == id || task.Title == id {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, filepath.SkipAll) {
		return "", false, err
	}
	return found, found != "", nil
}

func (t *testVaultClient) ListTasks(
	_ context.Context,
	_ vaultapi.Filter,
) ([]vaultapi.Task, error) {
	if t.listErr != nil {
		return nil, t.listErr
	}
	return t.tasks()
}

func (t *testVaultClient) ReadTask(_ context.Context, id string) (*vaultapi.Task, error) {
	if t.readErr != nil {
		return nil, t.readErr
	}
	path, found, err := t.pathFor(id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, vaultapi.ErrNotFound
	}
	task, err := t.taskAt(path)
	if err != nil {
		return nil, err
	}
	return &task, nil
}

func (t *testVaultClient) write(absPath string, fields map[string]any, body string) error {
	raw, err := renderTaskContent(fields, body)
	if err != nil {
		return err
	}
	return os.WriteFile(absPath, raw, 0600) // #nosec G306 -- test-only path
}

func (t *testVaultClient) PatchTask(
	_ context.Context,
	id string,
	fields map[string]any,
) (*vaultapi.Task, error) {
	if t.patchErr != nil {
		return nil, t.patchErr
	}
	path, found, err := t.pathFor(id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, vaultapi.ErrNotFound
	}
	current, err := t.taskAt(path)
	if err != nil {
		return nil, err
	}
	for key, value := range fields {
		current.Frontmatter[key] = value
	}
	// A fields-only patch leaves the body alone, which is the real route's
	// contract: only PATCH carrying a body key replaces it.
	if err := t.write(path, current.Frontmatter, current.Body); err != nil {
		return nil, err
	}
	return t.ReadTask(context.Background(), id)
}

func (t *testVaultClient) PatchTaskWithBody(
	_ context.Context,
	id string,
	fields map[string]any,
	body string,
) (*vaultapi.Task, error) {
	if t.patchErr != nil {
		return nil, t.patchErr
	}
	path, found, err := t.pathFor(id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, vaultapi.ErrNotFound
	}
	current, err := t.taskAt(path)
	if err != nil {
		return nil, err
	}
	for key, value := range fields {
		current.Frontmatter[key] = value
	}
	if err := t.write(path, current.Frontmatter, body); err != nil {
		return nil, err
	}
	return t.ReadTask(context.Background(), id)
}

func (t *testVaultClient) PutBody(_ context.Context, id string, body string) error {
	path, found, err := t.pathFor(id)
	if err != nil {
		return err
	}
	if !found {
		return vaultapi.ErrNotFound
	}
	current, err := t.taskAt(path)
	if err != nil {
		return err
	}
	return t.write(path, current.Frontmatter, body)
}

func (t *testVaultClient) CreateTask(
	_ context.Context,
	task vaultapi.Task,
	_ vaultapi.CreateOptions,
) (*vaultapi.Task, error) {
	path := filepath.Join(t.path, task.Title+".md")
	if _, err := os.Stat(path); err == nil {
		return nil, vaultapi.ErrAlreadyExists
	}
	if err := t.write(path, task.Frontmatter, task.Body); err != nil {
		return nil, err
	}
	return t.ReadTask(context.Background(), task.Title)
}

func (t *testVaultClient) ReaddressTask(
	_ context.Context,
	id string,
	next string,
	previous string,
) (*vaultapi.Task, error) {
	path, found, err := t.pathFor(id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, vaultapi.ErrNotFound
	}
	current, err := t.taskAt(path)
	if err != nil {
		return nil, err
	}
	current.Frontmatter["task_identifier"] = next
	if previous != "" {
		current.Frontmatter["task_identifier_previous"] = previous
	}
	if err := t.write(path, current.Frontmatter, current.Body); err != nil {
		return nil, err
	}
	return t.ReadTask(context.Background(), next)
}

var _ vaultapi.Client = new(testVaultClient)
