// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package testvault

// Client is a file-backed vaultapi.Client for tests that need a real
// vault rather than canned answers: they seed markdown in a temp dir and assert on
// what was read and written. It lives here rather than in a _test.go file because
// more than one package's tests need it and test files are not importable, and
// it lives in its own package because `make precommit` regenerates mocks/ and
// would delete a hand-written file there.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

// Client serves vaultapi.Client from a directory of markdown files.
type Client struct {
	Dir string

	// Call counters, so a spec can prove an operation was not reached — the
	// property the old GitClient double's *CallCount assertions carried.
	//
	// mu guards them because the walk reads the vault concurrently: without it
	// every `-race` run of a multi-task spec reports a data race on these three
	// fields, which is the double's problem rather than the caller's — the real
	// vaultapi client is an http.Client and is safe.
	mu         sync.Mutex
	listCalls  int
	readCalls  int
	patchCalls int

	// Only restricts the listing to these vault-relative paths when non-empty.
	// Specs use it to model an index that sees a narrower set than the whole dir.
	Only []string
	// ListErr makes the listing fail, for specs that assert an error path.
	ListErr error
	// ReadErrOn makes a read of the named task fail, for specs that model a
	// transient failure the caller must not mistake for absence.
	ReadErrOn map[string]error
	// PatchErr makes every typed write fail, for specs that assert the caller
	// swallows a write error.
	PatchErr error
}

// Seed writes a path→content map into Dir so a typed read finds it.
func (f *Client) Seed(files map[string][]byte) error {
	for relPath, content := range files {
		path := filepath.Join(f.Dir, relPath)
		if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
			return err
		}
		if err := os.WriteFile(path, content, 0600); err != nil { // #nosec G306
			return err
		}
	}
	return nil
}

// ListTasksCallCount reports how many listings were issued.
func (f *Client) ListTasksCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listCalls
}

// ReadTaskCallCount reports how many single-task reads were issued.
func (f *Client) ReadTaskCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readCalls
}

// PatchTaskWithBodyCallCount reports how many typed writes were issued.
func (f *Client) PatchTaskWithBodyCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.patchCalls
}

func (f *Client) tasks() ([]vaultapi.Task, error) {
	var tasks []vaultapi.Task
	allowed := make(map[string]struct{}, len(f.Only))
	for _, relPath := range f.Only {
		allowed[filepath.Clean(relPath)] = struct{}{}
	}
	err := filepath.Walk(f.Dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		if len(allowed) > 0 {
			rel, relErr := filepath.Rel(f.Dir, path)
			if relErr != nil {
				return nil
			}
			if _, ok := allowed[filepath.Clean(rel)]; !ok {
				return nil
			}
		}
		task, readErr := f.taskAt(path)
		if readErr != nil {
			return nil
		}
		tasks = append(tasks, task)
		return nil
	})
	return tasks, err
}

func (f *Client) taskAt(path string) (vaultapi.Task, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- test-only path
	if err != nil {
		return vaultapi.Task{}, err
	}
	fields, body, err := splitFileVaultContent(raw)
	if err != nil {
		return vaultapi.Task{}, err
	}
	id, _ := fields["task_identifier"].(string)
	return vaultapi.Task{
		ID:          id,
		Title:       strings.TrimSuffix(filepath.Base(path), ".md"),
		Frontmatter: fields,
		Body:        body,
	}, nil
}

func splitFileVaultContent(raw []byte) (map[string]any, string, error) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return map[string]any{}, text, nil
	}
	rest := text[len("---\n"):]
	idx := strings.Index(rest, "\n---")
	if idx == -1 {
		return map[string]any{}, text, nil
	}
	var fields map[string]any
	if err := yaml.Unmarshal([]byte(rest[:idx]), &fields); err != nil {
		return nil, "", err
	}
	if fields == nil {
		fields = map[string]any{}
	}
	return fields, strings.TrimPrefix(rest[idx+len("\n---"):], "\n"), nil
}

func (f *Client) pathFor(id string) (string, bool, error) {
	var found string
	err := filepath.Walk(f.Dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		task, readErr := f.taskAt(path)
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

// ListTasks returns every task under Dir.
func (f *Client) ListTasks(
	_ context.Context,
	_ vaultapi.Filter,
) ([]vaultapi.Task, error) {
	f.mu.Lock()
	f.listCalls++
	f.mu.Unlock()
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	return f.tasks()
}

// ReadTask returns the task addressed by identifier or title.
func (f *Client) ReadTask(_ context.Context, id string) (*vaultapi.Task, error) {
	f.mu.Lock()
	f.readCalls++
	f.mu.Unlock()
	if f.ReadErrOn != nil {
		if err, ok := f.ReadErrOn[id]; ok {
			return nil, err
		}
	}
	path, found, err := f.pathFor(id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, vaultapi.ErrNotFound
	}
	task, err := f.taskAt(path)
	if err != nil {
		return nil, err
	}
	return &task, nil
}

// ReadTaskAt returns the task addressed by identifier or title.
//
// The in-memory vault has no commits, so the committed view and the working tree
// are the same state here; a spec that needs them to differ stubs the client
// instead of using this one.
func (f *Client) ReadTaskAt(ctx context.Context, id string, _ string) (*vaultapi.Task, error) {
	return f.ReadTask(ctx, id)
}

func (f *Client) write(path string, fields map[string]any, body string) error {
	if fields == nil {
		fields = map[string]any{}
	}
	rendered, err := yaml.Marshal(fields)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte("---\n"+string(rendered)+"---\n"+body), 0600) // #nosec G306
}

func (f *Client) patch(
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
	current, err := f.taskAt(path)
	if err != nil {
		return nil, err
	}
	for key, value := range fields {
		current.Frontmatter[key] = value
	}
	nextBody := current.Body
	if body != nil {
		nextBody = *body
	}
	if err := f.write(path, current.Frontmatter, nextBody); err != nil {
		return nil, err
	}
	return f.ReadTask(context.Background(), id)
}

// PatchTask replaces the named frontmatter fields.
func (f *Client) PatchTask(
	_ context.Context,
	id string,
	fields map[string]any,
) (*vaultapi.Task, error) {
	return f.patch(id, fields, nil)
}

// PatchTaskWithBody replaces the named fields and the body in one write.
func (f *Client) PatchTaskWithBody(
	_ context.Context,
	id string,
	fields map[string]any,
	body string,
) (*vaultapi.Task, error) {
	f.mu.Lock()
	f.patchCalls++
	f.mu.Unlock()
	if f.PatchErr != nil {
		return nil, f.PatchErr
	}
	return f.patch(id, fields, &body)
}

// PutBody replaces the body.
func (f *Client) PutBody(_ context.Context, id string, body string) error {
	_, err := f.patch(id, map[string]any{}, &body)
	return err
}

// CreateTask writes a new task file named after the title.
func (f *Client) CreateTask(
	_ context.Context,
	task vaultapi.Task,
	_ vaultapi.CreateOptions,
) (*vaultapi.Task, error) {
	path := filepath.Join(f.Dir, task.Title+".md")
	if _, err := os.Stat(path); err == nil {
		return nil, vaultapi.ErrAlreadyExists
	}
	if err := f.write(path, task.Frontmatter, task.Body); err != nil {
		return nil, err
	}
	return f.ReadTask(context.Background(), task.Title)
}

// ReaddressTask moves the task to a new identifier, recording the previous one.
func (f *Client) ReaddressTask(
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

var _ vaultapi.Client = new(Client)
