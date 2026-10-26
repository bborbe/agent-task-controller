// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vaultapi

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

//counterfeiter:generate -o ../../mocks/vault_api_client.go --fake-name VaultAPIClient . Client

// ErrNotFound is returned when the addressed task does not exist. It is the
// ordinary answer to "has this task been created yet", not a failure.
var ErrNotFound = stderrors.New("task not found")

// ErrAlreadyExists is returned by CreateTask when a task of that identifier is
// already present. It replaces git-rest's 409 on PostIfAbsent: the same benign
// conflict, expressed by the typed API.
var ErrAlreadyExists = stderrors.New("task already exists")

// ErrAmbiguous is returned when a name addresses more than one task. The typed
// API refuses rather than guessing, and so does this client.
var ErrAmbiguous = stderrors.New("name is ambiguous")

// requestTimeout bounds every call. It is deliberately not derived from the
// caller's context: a caller whose deadline has already expired is exactly the
// case a read-back has to survive, and the transport must not inherit that.
const requestTimeout = 30 * time.Second

// Task is the typed view of one task. There is no path: the identifier is the
// address, and the title is what a human sees. Body and Frontmatter are the two
// halves a write touches, and they are separate routes because they are separate
// mutations — PATCH replaces named frontmatter fields, PUT replaces the body.
type Task struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Frontmatter map[string]any `json:"frontmatter,omitempty"`
	Body        string         `json:"body,omitempty"`
}

// Filter is a typed listing question. Zero fields are not sent, so an empty
// Filter asks for everything — the caller's own gate decides whether that is a
// sane question to ask.
//
// TaskTypes is a union of the values; every other field intersects. The API
// distinguishes an absent parameter from an empty one, and so must a caller that
// means "any type" rather than "no type": leave the slice nil.
type Filter struct {
	Assignee  string
	Status    string
	Phase     string
	Goal      string
	Cluster   string
	TaskTypes []string
}

// Client is the controller's persistence model, addressed by identifier.
//
// It replaces gitrestclient.GitClient, and the methods that are absent are the
// point: there is no EnsureCloned, Pull, CommitAndPush or Path, because vault-api
// commits server-side and holds the working copy. A caller that wants to know
// where a file is has the wrong model; a caller that wants to know what a task
// says asks for the task.
type Client interface {
	// ReadTask returns the task with the given identifier, or ErrNotFound.
	ReadTask(ctx context.Context, id string) (*Task, error)
	// CreateTask creates a task and returns it with the identifier the API
	// minted. ErrAlreadyExists when one is already present.
	CreateTask(ctx context.Context, task Task) (*Task, error)
	// PatchTask replaces the named frontmatter fields and returns the task as it
	// now stands. It is the atomic mutation: the API serialises per task.
	PatchTask(ctx context.Context, id string, fields map[string]any) (*Task, error)
	// PutBody replaces the task's body. It does not touch frontmatter.
	PutBody(ctx context.Context, id string, body string) error
	// ListTasks returns the tasks matching filter. It refuses rather than returns
	// when the API reports a total the response does not carry, so a caller never
	// mistakes a partial set for the whole population.
	ListTasks(ctx context.Context, filter Filter) ([]Task, error)
}

type client struct {
	baseURL          string
	gatewaySecret    string
	gatewayInitiator string
	httpClient       *http.Client
}

// NewClient builds a Client against baseURL. An empty gatewaySecret sends no
// auth headers, which is how a local vault-api is addressed; every deployed
// instance requires it and answers 401 without it.
func NewClient(baseURL, gatewaySecret, gatewayInitiator string) Client {
	return &client{
		baseURL:          strings.TrimRight(baseURL, "/"),
		gatewaySecret:    gatewaySecret,
		gatewayInitiator: gatewayInitiator,
		httpClient:       &http.Client{Timeout: requestTimeout},
	}
}

// do issues one request and returns the status and body. It is the only place
// the gateway headers are set, so no route can forget them.
func (c *client) do(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	body any,
) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, errors.Wrapf(ctx, err, "marshal %s %s", method, path)
		}
		reader = bytes.NewReader(raw)
	}
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, nil, errors.Wrapf(ctx, err, "build %s %s", method, path)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.gatewaySecret != "" {
		req.Header.Set("X-Gateway-Secret", c.gatewaySecret)
		req.Header.Set("X-Gateway-Initator", c.gatewayInitiator)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, errors.Wrapf(ctx, err, "%s %s", method, path)
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return resp.StatusCode, nil, errors.Wrapf(ctx, readErr, "read %s %s", method, path)
	}
	return resp.StatusCode, raw, nil
}

// taskPath addresses one task by identifier, escaping it so an identifier that
// carries a slash or a space cannot become a different route.
func taskPath(id string, suffix string) string {
	return "/api/v1/tasks/" + url.PathEscape(id) + suffix
}

// decodeTask converts one response body into a Task, mapping the API's error
// vocabulary onto this package's sentinels so callers never parse a status code.
func decodeTask(ctx context.Context, status int, raw []byte, what string) (*Task, error) {
	switch status {
	case http.StatusOK, http.StatusCreated:
	case http.StatusNotFound:
		return nil, ErrNotFound
	case http.StatusConflict:
		return nil, conflictError(ctx, raw, what)
	case http.StatusUnprocessableEntity:
		return nil, errors.Wrapf(ctx, ErrAmbiguous, "%s", what)
	default:
		return nil, errors.Errorf(ctx, "%s: unexpected status %d: %s", what, status, snippet(raw))
	}
	var task Task
	if err := json.Unmarshal(raw, &task); err != nil {
		return nil, errors.Wrapf(ctx, err, "decode %s", what)
	}
	if task.ID == "" {
		return nil, errors.Errorf(ctx, "%s: response carries no id", what)
	}
	return &task, nil
}

// conflictError distinguishes "already exists" from "ambiguous name" — both are
// 409, and a caller treats them differently: the first is benign, the second
// means the caller's addressing is wrong.
func conflictError(ctx context.Context, raw []byte, what string) error {
	var envelope struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Error == "ambiguous_name" {
		return errors.Wrapf(ctx, ErrAmbiguous, "%s", what)
	}
	return errors.Wrapf(ctx, ErrAlreadyExists, "%s", what)
}

// snippet bounds an unexpected body so an error message cannot carry a whole
// page into a log line.
func snippet(raw []byte) string {
	const limit = 200
	if len(raw) <= limit {
		return string(raw)
	}
	return string(raw[:limit]) + "…"
}

// ReadTask returns one task by identifier.
func (c *client) ReadTask(ctx context.Context, id string) (*Task, error) {
	status, raw, err := c.do(ctx, http.MethodGet, taskPath(id, ""), nil, nil)
	if err != nil {
		return nil, err
	}
	return decodeTask(ctx, status, raw, "read task "+id)
}

// CreateTask creates a task and returns the stored form.
func (c *client) CreateTask(ctx context.Context, task Task) (*Task, error) {
	status, raw, err := c.do(ctx, http.MethodPost, "/api/v1/tasks", nil, task)
	if err != nil {
		return nil, err
	}
	return decodeTask(ctx, status, raw, "create task")
}

// PatchTask replaces the named frontmatter fields.
func (c *client) PatchTask(
	ctx context.Context,
	id string,
	fields map[string]any,
) (*Task, error) {
	status, raw, err := c.do(ctx, http.MethodPatch, taskPath(id, ""), nil, fields)
	if err != nil {
		return nil, err
	}
	return decodeTask(ctx, status, raw, "patch task "+id)
}

// PutBody replaces the body.
func (c *client) PutBody(ctx context.Context, id string, body string) error {
	payload := map[string]string{"body": body}
	status, raw, err := c.do(ctx, http.MethodPut, taskPath(id, "/content"), nil, payload)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		return ErrNotFound
	default:
		return errors.Errorf(ctx, "put body %s: unexpected status %d: %s", id, status, snippet(raw))
	}
}

// ListTasks returns every task matching filter, following paging to exhaustion.
func (c *client) ListTasks(ctx context.Context, filter Filter) ([]Task, error) {
	query := filter.query()
	status, raw, err := c.do(ctx, http.MethodGet, "/api/v1/tasks", query, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, errors.Errorf(
			ctx, "list tasks: unexpected status %d: %s", status, snippet(raw),
		)
	}
	var page struct {
		Items []Task `json:"items"`
		Total int    `json:"total"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, errors.Wrapf(ctx, err, "decode task listing")
	}
	// ⚠️ A short page is not proof of a complete set, and a caller cannot tell a
	// truncated answer from a whole one by counting rows — the same trap the
	// collector's dedup check documents. Refuse rather than return a partial set
	// as if it were the population.
	if page.Total != len(page.Items) {
		return nil, errors.Errorf(
			ctx,
			"list tasks: server reported total=%d but returned %d items; "+
				"refusing to present a partial set as the whole population",
			page.Total,
			len(page.Items),
		)
	}
	glog.V(3).Infof("vault-api list tasks: %d of total %d", len(page.Items), page.Total)
	return page.Items, nil
}

// query renders the filter as the API's parameter set. Nil TaskTypes is omitted
// deliberately: the API distinguishes "absent" from "empty", and an empty list
// would ask for tasks of no type at all.
func (f Filter) query() url.Values {
	q := url.Values{}
	for key, value := range map[string]string{
		"assignee": f.Assignee,
		"status":   f.Status,
		"phase":    f.Phase,
		"goal":     f.Goal,
		"cluster":  f.Cluster,
	} {
		if value != "" {
			q.Set(key, value)
		}
	}
	for _, taskType := range f.TaskTypes {
		q.Add("task_type", taskType)
	}
	return q
}
