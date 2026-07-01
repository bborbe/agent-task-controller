// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package prcomment_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	lib "github.com/bborbe/agent"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/prcomment"
)

var _ = Describe("PRCommenter", func() {
	var (
		server    *httptest.Server
		client    *http.Client
		baseURL   string
		commenter prcomment.PRCommenter
	)

	BeforeEach(func() {
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			path := r.URL.Path
			if !strings.HasPrefix(path, "/repos/") {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			parts := strings.Split(strings.TrimPrefix(path, "/repos/"), "/")
			if len(parts) < 5 || parts[2] != "pulls" || parts[4] != "reviews" {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			owner, repo := parts[0], parts[1]

			auth := r.Header.Get("Authorization")
			if auth == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			if auth != "Bearer test-token" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}

			accept := r.Header.Get("Accept")
			if accept != "application/vnd.github+json" {
				http.Error(w, "invalid accept", http.StatusBadRequest)
				return
			}

			var reqBody struct {
				Event string `json:"event"`
				Body  string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}

			if reqBody.Event != "COMMENT" {
				http.Error(w, "event must be COMMENT", http.StatusBadRequest)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"id": 123, "owner": %q, "repo": %q}`, owner, repo)
		}))
		baseURL = server.URL
		client = server.Client()
	})

	AfterEach(func() {
		server.Close()
	})

	Describe("PostComment", func() {
		Context("happy path with repository + pull_request_number", func() {
			It("posts a COMMENT review and returns nil", func() {
				commenter = prcomment.NewPRCommenter(client, baseURL, "test-token")
				fm := lib.TaskFrontmatter{
					"repository":          "bborbe/maintainer",
					"pull_request_number": 62,
				}
				err := commenter.PostComment(
					context.Background(),
					fm,
					"Automated pr-review planning failed after 3 controller retries.",
				)
				Expect(err).To(BeNil())
			})
		})

		Context("happy path with pr_url", func() {
			It("parses pr_url and posts a COMMENT review", func() {
				commenter = prcomment.NewPRCommenter(client, baseURL, "test-token")
				fm := lib.TaskFrontmatter{
					"pr_url": "https://github.com/bborbe/maintainer/pull/62",
				}
				err := commenter.PostComment(
					context.Background(),
					fm,
					"Automated pr-review planning failed after 3 controller retries.",
				)
				Expect(err).To(BeNil())
			})
		})

		Context("missing token", func() {
			It("returns an error containing the frozen substring", func() {
				commenter = prcomment.NewPRCommenter(client, baseURL, "")
				fm := lib.TaskFrontmatter{
					"repository":          "bborbe/maintainer",
					"pull_request_number": 62,
				}
				err := commenter.PostComment(context.Background(), fm, "test body")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("github token missing"))
			})
		})

		Context("unresolvable PR - no pr_url or repository", func() {
			It("returns an error containing the frozen substring", func() {
				commenter = prcomment.NewPRCommenter(client, baseURL, "test-token")
				fm := lib.TaskFrontmatter{}
				err := commenter.PostComment(context.Background(), fm, "test body")
				Expect(err).To(HaveOccurred())
				Expect(
					err.Error(),
				).To(ContainSubstring("planning-retry: cannot resolve PR from task:"))
			})
		})

		Context("invalid repo slug", func() {
			It("returns an error containing the frozen substring", func() {
				commenter = prcomment.NewPRCommenter(client, baseURL, "test-token")
				fm := lib.TaskFrontmatter{
					"repository":          "not a slug!!",
					"pull_request_number": 62,
				}
				err := commenter.PostComment(context.Background(), fm, "test body")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("invalid repository slug"))
			})
		})

		Context("non-2xx response", func() {
			It("returns an error containing the frozen substring and status", func() {
				badServer := httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.WriteHeader(http.StatusInternalServerError)
					}),
				)
				defer badServer.Close()
				badClient := badServer.Client()

				commenter = prcomment.NewPRCommenter(badClient, badServer.URL, "test-token")
				fm := lib.TaskFrontmatter{
					"repository":          "bborbe/maintainer",
					"pull_request_number": 62,
				}
				err := commenter.PostComment(context.Background(), fm, "test body")
				Expect(err).To(HaveOccurred())
				Expect(
					err.Error(),
				).To(ContainSubstring("planning-retry: github COMMENT post failed:"))
				Expect(err.Error()).To(ContainSubstring("status 500"))
			})
		})

		Context("transport error", func() {
			It("returns an error containing the frozen substring", func() {
				badClient := &http.Client{Timeout: 1 * time.Nanosecond}
				commenter = prcomment.NewPRCommenter(badClient, "http://127.0.0.1:1", "test-token")
				fm := lib.TaskFrontmatter{
					"repository":          "bborbe/maintainer",
					"pull_request_number": 62,
				}
				err := commenter.PostComment(context.Background(), fm, "test body")
				Expect(err).To(HaveOccurred())
				Expect(
					err.Error(),
				).To(ContainSubstring("planning-retry: github COMMENT post failed:"))
			})
		})
	})
})
