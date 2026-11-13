// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

// Integration coverage for the controller planning-retry seam. Unlike
// planning_retry_test.go (which backs the typed client with a temp directory),
// this wires the REAL retry components together — the real vaultapi.Client
// talking HTTP, the real PRCommenter, the real PATCH request/response round
// trip — and fakes only the two true external boundaries as httptest servers:
//
//   - vault-api HTTP → an in-memory identifier->task store (GET/PATCH /api/v1/tasks/{id})
//   - GitHub HTTP    → captures the POST /repos/…/pulls/N/reviews body
//
// Both client constructors already take injectable base URLs, so no production
// source change is needed to reach this fidelity. The point is to exercise the
// real typed read, the real single-request field+body PATCH, and the real
// GitHub COMMENT POST end-to-end — the pieces the mock-based unit test cannot
// prove interoperate.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	lib "github.com/bborbe/agent"
	libtime "github.com/bborbe/time"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/pkg/command"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/prcomment"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

// inMemVaultAPI is an in-memory stand-in for the vault-api typed task surface.
// It is an honest protocol fake — it serves the real routes the typed client
// calls and answers in the real response shape, not a behavioural stub of the
// client itself.
type inMemVaultAPI struct {
	mu    sync.Mutex
	tasks map[string]*vaultapi.Task
}

func newInMemVaultAPI() *inMemVaultAPI {
	return &inMemVaultAPI{tasks: map[string]*vaultapi.Task{}}
}

func (s *inMemVaultAPI) get(id string) (*vaultapi.Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return nil, false
	}
	copied := *t
	return &copied, true
}

func (s *inMemVaultAPI) seed(id string, fm map[string]any, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[id] = &vaultapi.Task{ID: id, Title: id, Frontmatter: fm, Body: body}
}

func (s *inMemVaultAPI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/tasks/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/")
		switch r.Method {
		case http.MethodGet:
			t, ok := s.get(id)
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeTask(w, t)
		case http.MethodPatch:
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			var payload map[string]any
			if decodeErr := json.Unmarshal(raw, &payload); decodeErr != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			body, _ := payload["body"].(string)
			delete(payload, "body")

			s.mu.Lock()
			t, ok := s.tasks[id]
			if !ok {
				s.mu.Unlock()
				w.WriteHeader(http.StatusNotFound)
				return
			}
			merged := map[string]any{}
			for key, value := range t.Frontmatter {
				merged[key] = value
			}
			for key, value := range payload {
				merged[key] = value
			}
			// The real API writes YAML to disk and reads it back, so an integer
			// arrives as an integer rather than a JSON float. Round-trip here so
			// the fake reports the same types the production surface does.
			normalized := normalizeFrontmatter(merged)
			updated := &vaultapi.Task{ID: id, Title: t.Title, Frontmatter: normalized, Body: body}
			s.tasks[id] = updated
			copied := *updated
			s.mu.Unlock()
			writeTask(w, &copied)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	return mux
}

// normalizeFrontmatter round-trips a map through YAML, which is what turns a
// JSON-decoded float64 back into the int the vault actually stores.
func normalizeFrontmatter(fm map[string]any) map[string]any {
	raw, err := yaml.Marshal(fm)
	if err != nil {
		return fm
	}
	normalized := map[string]any{}
	if unmarshalErr := yaml.Unmarshal(raw, &normalized); unmarshalErr != nil {
		return fm
	}
	return normalized
}

func writeTask(w http.ResponseWriter, t *vaultapi.Task) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(t)
}

// capturedReview records one GitHub review POST the fake received.
type capturedReview struct {
	path  string
	event string
	body  string
}

// githubReviewFake captures POST …/reviews calls and answers 201.
type githubReviewFake struct {
	mu      sync.Mutex
	reviews []capturedReview
}

func (g *githubReviewFake) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.reviews)
}

func (g *githubReviewFake) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/reviews") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var rr struct {
			Event string `json:"event"`
			Body  string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&rr)
		g.mu.Lock()
		g.reviews = append(
			g.reviews,
			capturedReview{path: r.URL.Path, event: rr.Event, body: rr.Body},
		)
		g.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	})
}

var _ = Describe("PlanningRetryGate (integration)", func() {
	var (
		ctx      context.Context
		store    *inMemVaultAPI
		github   *githubReviewFake
		vaultSrv *httptest.Server
		ghSrv    *httptest.Server
		gate     command.PlanningRetryGate
		taskDir  string
	)

	// seedFrontmatter builds the frontmatter map for a task file. retryCount < 0
	// omits the planning_retry_count key. withPR adds the repository +
	// pull_request_number the escalation COMMENT resolves against.
	seedFrontmatter := func(taskID, phase string, retryCount int, withPR bool) map[string]any {
		fm := map[string]any{
			"task_identifier": taskID,
			"task_type":       "pr-review",
			"assignee":        "pr-reviewer-agent",
			"status":          "in_progress",
			"phase":           phase,
		}
		if retryCount >= 0 {
			fm["planning_retry_count"] = retryCount
		}
		if withPR {
			fm["repository"] = "bborbe/maintainer"
			fm["pull_request_number"] = 62
		}
		return fm
	}

	buildFailedPlanningTask := func(taskID, message string) lib.Task {
		return lib.Task{
			TaskIdentifier: lib.TaskIdentifier(taskID),
			Frontmatter: lib.TaskFrontmatter{
				"task_type": "pr-review",
				"phase":     "planning",
				"assignee":  "pr-reviewer-agent",
			},
			Content: lib.TaskContent("## Result\nStatus: failed\nMessage: " + message + "\n"),
		}
	}

	// storedTask reads a task back out of the fake store.
	storedTask := func(id string) *vaultapi.Task {
		t, ok := store.get(id)
		Expect(ok).To(BeTrue())
		return t
	}

	BeforeEach(func() {
		ctx = context.Background()
		store = newInMemVaultAPI()
		github = &githubReviewFake{}
		vaultSrv = httptest.NewServer(store.handler())
		ghSrv = httptest.NewServer(github.handler())
		taskDir = "tasks"

		vaultClient := vaultapi.NewClient(vaultSrv.URL, "", "integration-test")
		commenter := prcomment.NewPRCommenter(http.DefaultClient, ghSrv.URL, "test-token")
		clock := libtime.NewCurrentDateTime()
		gate = command.NewPlanningRetryGate(
			vaultClient,
			taskDir,
			"integration-test",
			clock,
			commenter,
			metrics.New(),
			nil,
		)
	})

	AfterEach(func() {
		vaultSrv.Close()
		ghSrv.Close()
	})

	Context("malformed planning failure below the cap", func() {
		It(
			"bumps the counter, appends a ## Progress line, assigns a fresh task_identifier, and posts NO comment",
			func() {
				store.seed(
					"pr-123",
					seedFrontmatter("pr-123", "planning", -1, true),
					"## Objective\n\nreview the PR\n",
				)
				req := buildFailedPlanningTask(
					"pr-123",
					"minimax B-case: plan JSON started with B not {",
				)

				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())

				task := storedTask("pr-123")
				fm := lib.TaskFrontmatter(task.Frontmatter)

				count, _ := fm.Int("planning_retry_count")
				Expect(count).To(Equal(1))

				phase, _ := fm.String("phase")
				Expect(phase).To(Equal("planning"))

				newID, _ := fm.String("task_identifier")
				Expect(newID).NotTo(Equal("pr-123"))
				_, parseErr := uuid.Parse(newID)
				Expect(parseErr).To(BeNil())

				Expect(task.Body).To(ContainSubstring("## Progress"))
				Expect(task.Body).To(ContainSubstring("retry 1/3:"))

				// No PR is in silent limbo yet — below the cap, nothing is posted.
				Expect(github.count()).To(Equal(0))
			},
		)
	})

	Context("planning failure at the retry cap", func() {
		It(
			"escalates to human_review, clears the assignee, and posts exactly one COMMENT review to GitHub",
			func() {
				store.seed(
					"pr-124",
					seedFrontmatter("pr-124", "planning", 3, true),
					"## Objective\n\nreview the PR\n",
				)
				req := buildFailedPlanningTask("pr-124", "still malformed after three tries")

				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())

				task := storedTask("pr-124")
				fm := lib.TaskFrontmatter(task.Frontmatter)

				phase, _ := fm.String("phase")
				Expect(phase).To(Equal("human_review"))

				assignee, _ := fm.String("assignee")
				Expect(assignee).To(Equal(""))

				Expect(task.Body).To(ContainSubstring("retry 3/3:"))

				Expect(github.count()).To(Equal(1))
				review := github.reviews[0]
				Expect(review.path).To(Equal("/repos/bborbe/maintainer/pulls/62/reviews"))
				Expect(review.event).To(Equal("COMMENT"))
				Expect(
					review.body,
				).To(ContainSubstring("Automated pr-review planning failed after 3 controller retries"))
				Expect(review.body).To(ContainSubstring("tasks/pr-124.md"))
			},
		)
	})
})
