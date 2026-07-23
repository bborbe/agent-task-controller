// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

// Integration coverage for the controller planning-retry seam. Unlike
// planning_retry_test.go (which stubs GitClient + PRCommenter with
// counterfeiter fakes), this wires the REAL retry components together —
// real GitRestClient + git-rest adapter, real PRCommenter, real
// FindTaskFilePath + AtomicReadModifyWriteAndCommitPush modify closures —
// and fakes only the two true external boundaries as httptest servers:
//
//   - git-rest HTTP  → an in-memory path->bytes store (GET/POST /api/v1/files/…, /readiness)
//   - GitHub  HTTP   → captures the POST /repos/…/pulls/N/reviews body
//
// Both client constructors already take injectable base URLs, so no
// production source change is needed to reach this fidelity. The point is
// to exercise the real yaml frontmatter round-trip, the real read-modify-
// write merge, and the real GitHub COMMENT POST end-to-end — the pieces the
// mock-based unit test cannot prove interoperate.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"

	lib "github.com/bborbe/agent"
	libtime "github.com/bborbe/time"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/pkg/command"
	"github.com/bborbe/agent-task-controller/pkg/gitrestclient"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/prcomment"
	"github.com/bborbe/agent-task-controller/pkg/result"
)

// inMemGitRest is an in-memory stand-in for the git-rest /api/v1/files REST
// API. It is an honest protocol fake — it serves the real endpoints the
// GitRestClient calls, not a behavioural stub of the client itself.
type inMemGitRest struct {
	mu    sync.Mutex
	files map[string][]byte
}

func newInMemGitRest() *inMemGitRest {
	return &inMemGitRest{files: map[string][]byte{}}
}

func (s *inMemGitRest) get(rel string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.files[rel]
	return b, ok
}

func (s *inMemGitRest) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/readiness", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/v1/files/", func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/api/v1/files/")
		switch r.Method {
		case http.MethodGet:
			if rel == "" {
				s.serveList(w, r.URL.Query().Get("glob"))
				return
			}
			b, ok := s.get(rel)
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(b)
		case http.MethodPost:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			s.mu.Lock()
			s.files[rel] = body
			s.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	return mux
}

func (s *inMemGitRest) serveList(w http.ResponseWriter, glob string) {
	s.mu.Lock()
	matched := make([]string, 0, len(s.files))
	for k := range s.files {
		if ok, _ := path.Match(glob, k); ok {
			matched = append(matched, k)
		}
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(matched)
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
		store    *inMemGitRest
		github   *githubReviewFake
		gitSrv   *httptest.Server
		ghSrv    *httptest.Server
		gate     command.PlanningRetryGate
		taskDir  string
		basePath string
	)

	// seedFile builds a task file body with the given frontmatter. retryCount
	// < 0 omits the planning_retry_count key. withPR adds the repository +
	// pull_request_number the escalation COMMENT resolves against.
	seedFile := func(taskID, phase string, retryCount int, withPR bool) []byte {
		fm := map[string]interface{}{
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
		fmBytes, err := yaml.Marshal(fm)
		Expect(err).To(BeNil())
		return []byte("---\n" + string(fmBytes) + "\n---\n## Objective\n\nreview the PR\n")
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

	// storedFrontmatter reads a file back out of the fake store and parses its frontmatter.
	storedFrontmatter := func(rel string) (lib.TaskFrontmatter, string) {
		raw, ok := store.get(rel)
		Expect(ok).To(BeTrue())
		fmStr, err := result.ExtractFrontmatter(ctx, raw)
		Expect(err).To(BeNil())
		var fm lib.TaskFrontmatter
		Expect(yaml.Unmarshal([]byte(fmStr), &fm)).To(BeNil())
		return fm, string(raw)
	}

	BeforeEach(func() {
		ctx = context.Background()
		store = newInMemGitRest()
		github = &githubReviewFake{}
		gitSrv = httptest.NewServer(store.handler())
		ghSrv = httptest.NewServer(github.handler())
		taskDir = "tasks"
		basePath = "/repo"

		restClient := gitrestclient.NewGitRestClient(
			gitSrv.URL,
			"",
			"integration-test",
			metrics.New(),
		)
		gitClient := gitrestclient.NewGitClient(restClient, basePath)
		commenter := prcomment.NewPRCommenter(http.DefaultClient, ghSrv.URL, "test-token")
		clock := libtime.NewCurrentDateTime()
		gate = command.NewPlanningRetryGate(gitClient, taskDir, clock, commenter, metrics.New())
	})

	AfterEach(func() {
		gitSrv.Close()
		ghSrv.Close()
	})

	Context("malformed planning failure below the cap", func() {
		It(
			"bumps the counter, appends a ## Progress line, assigns a fresh task_identifier, and posts NO comment",
			func() {
				store.files["tasks/pr-123.md"] = seedFile("pr-123", "planning", -1, true)
				req := buildFailedPlanningTask(
					"pr-123",
					"minimax B-case: plan JSON started with B not {",
				)

				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())

				fm, raw := storedFrontmatter("tasks/pr-123.md")

				count, _ := fm.Int("planning_retry_count")
				Expect(count).To(Equal(1))

				phase, _ := fm.String("phase")
				Expect(phase).To(Equal("planning"))

				newID, _ := fm.String("task_identifier")
				Expect(newID).NotTo(Equal("pr-123"))
				_, parseErr := uuid.Parse(newID)
				Expect(parseErr).To(BeNil())

				Expect(raw).To(ContainSubstring("## Progress"))
				Expect(raw).To(ContainSubstring("retry 1/3:"))

				// No PR is in silent limbo yet — below the cap, nothing is posted.
				Expect(github.count()).To(Equal(0))
			},
		)
	})

	Context("planning failure at the retry cap", func() {
		It(
			"escalates to human_review, clears the assignee, and posts exactly one COMMENT review to GitHub",
			func() {
				store.files["tasks/pr-124.md"] = seedFile("pr-124", "planning", 3, true)
				req := buildFailedPlanningTask("pr-124", "still malformed after three tries")

				handled, err := gate.Handle(ctx, req)
				Expect(err).To(BeNil())
				Expect(handled).To(BeTrue())

				fm, raw := storedFrontmatter("tasks/pr-124.md")

				phase, _ := fm.String("phase")
				Expect(phase).To(Equal("human_review"))

				assignee, _ := fm.String("assignee")
				Expect(assignee).To(Equal(""))

				Expect(raw).To(ContainSubstring("retry 3/3:"))

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
