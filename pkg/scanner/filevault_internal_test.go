// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

// fileVault is a file-backed vaultapi.Client for the internal tests, which seed
// markdown files in a temp dir and assert on what the scanner read and wrote. It
// is a test-only helper; production reads through pkg/vaultapi.

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

type fileVault struct {
	dir string
}

func (f fileVault) tasks() ([]vaultapi.Task, error) {
	var tasks []vaultapi.Task
	err := filepath.Walk(f.dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		raw, readErr := os.ReadFile(path) // #nosec G304 -- test-only path
		if readErr != nil {
			return nil
		}
		fields, body, splitErr := splitRendered(context.Background(), raw)
		if splitErr != nil {
			return nil
		}
		id, _ := fields["task_identifier"].(string)
		tasks = append(tasks, vaultapi.Task{
			ID:          id,
			Title:       strings.TrimSuffix(filepath.Base(path), ".md"),
			Frontmatter: fields,
			Body:        body,
		})
		return nil
	})
	return tasks, err
}

func (f fileVault) pathFor(id string) (string, bool, error) {
	tasks, err := f.tasks()
	if err != nil {
		return "", false, err
	}
	for _, task := range tasks {
		if task.ID == id || task.Title == id {
			return filepath.Join(f.dir, task.Title+".md"), true, nil
		}
	}
	return "", false, nil
}

func (f fileVault) ListTasks(_ context.Context, _ vaultapi.Filter) ([]vaultapi.Task, error) {
	return f.tasks()
}

func (f fileVault) ReadTask(_ context.Context, id string) (*vaultapi.Task, error) {
	path, found, err := f.pathFor(id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, vaultapi.ErrNotFound
	}
	raw, readErr := os.ReadFile(path) // #nosec G304 -- test-only path
	if readErr != nil {
		return nil, readErr
	}
	fields, body, splitErr := splitRendered(context.Background(), raw)
	if splitErr != nil {
		return nil, splitErr
	}
	id2, _ := fields["task_identifier"].(string)
	return &vaultapi.Task{
		ID:          id2,
		Title:       strings.TrimSuffix(filepath.Base(path), ".md"),
		Frontmatter: fields,
		Body:        body,
	}, nil
}

func (f fileVault) CreateTask(
	_ context.Context,
	task vaultapi.Task,
	_ vaultapi.CreateOptions,
) (*vaultapi.Task, error) {
	path := filepath.Join(f.dir, task.Title+".md")
	if _, err := os.Stat(path); err == nil {
		return nil, vaultapi.ErrAlreadyExists
	}
	return f.writeTask(path, task.Frontmatter, task.Body)
}

func (f fileVault) PatchTask(
	_ context.Context,
	id string,
	fields map[string]any,
) (*vaultapi.Task, error) {
	return f.patch(id, fields, nil)
}

func (f fileVault) PatchTaskWithBody(
	_ context.Context,
	id string,
	fields map[string]any,
	body string,
) (*vaultapi.Task, error) {
	return f.patch(id, fields, &body)
}

func (f fileVault) patch(
	id string,
	fields map[string]any,
	body *string,
) (*vaultapi.Task, error) {
	path, found, err := f.pathFor(id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, vaultapi.ErrNotFound
	}
	raw, readErr := os.ReadFile(path) // #nosec G304 -- test-only path
	if readErr != nil {
		return nil, readErr
	}
	current, currentBody, splitErr := splitRendered(context.Background(), raw)
	if splitErr != nil {
		return nil, splitErr
	}
	for key, value := range fields {
		current[key] = value
	}
	nextBody := currentBody
	if body != nil {
		nextBody = *body
	}
	return f.writeTask(path, current, nextBody)
}

func (f fileVault) PutBody(_ context.Context, id string, body string) error {
	_, err := f.patch(id, map[string]any{}, &body)
	return err
}

func (f fileVault) ReaddressTask(
	_ context.Context,
	id string,
	next string,
	previous string,
) (*vaultapi.Task, error) {
	fields := map[string]any{"task_identifier": next}
	if previous != "" {
		fields["task_identifier_previous"] = previous
	}
	return f.patch(id, fields, nil)
}

func (f fileVault) writeTask(
	path string,
	fields map[string]any,
	body string,
) (*vaultapi.Task, error) {
	raw, err := renderTaskContentForTest(fields, body)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, raw, 0600); err != nil { // #nosec G306 -- test-only path
		return nil, err
	}
	id, _ := fields["task_identifier"].(string)
	return &vaultapi.Task{
		ID:          id,
		Title:       strings.TrimSuffix(filepath.Base(path), ".md"),
		Frontmatter: fields,
		Body:        body,
	}, nil
}

func renderTaskContentForTest(fields map[string]any, body string) ([]byte, error) {
	return renderTask(context.Background(), vaultapi.Task{
		Frontmatter: fields,
		Body:        body,
	})
}
