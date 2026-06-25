// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gitrestclient_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/gitrestclient"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
)

// zeroBackoff returns 0 duration so retry tests run instantly.
func zeroBackoff(_ int) time.Duration {
	return 0
}

// shortBackoff returns 1ms so context-cancellation tests run fast but still enter the select.
func shortBackoff(_ int) time.Duration {
	return time.Millisecond
}

var _ = Describe("exponentialBackoff", func() {
	It("returns correct doubling sequence", func() {
		Expect(gitrestclient.ExponentialBackoff(1)).To(Equal(1 * time.Second))
		Expect(gitrestclient.ExponentialBackoff(2)).To(Equal(2 * time.Second))
		Expect(gitrestclient.ExponentialBackoff(3)).To(Equal(4 * time.Second))
		Expect(gitrestclient.ExponentialBackoff(4)).To(Equal(8 * time.Second))
		Expect(gitrestclient.ExponentialBackoff(5)).To(Equal(16 * time.Second))
	})
})

var _ = Describe("GitRestClient", func() {
	var (
		ctx    context.Context
		server *httptest.Server
		client gitrestclient.GitRestClient
	)

	BeforeEach(func() {
		ctx = context.Background()
	})

	AfterEach(func() {
		if server != nil {
			server.Close()
		}
	})

	Describe("Get", func() {
		Context("200 response", func() {
			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						Expect(r.Method).To(Equal(http.MethodGet))
						Expect(r.URL.Path).To(Equal("/api/v1/files/tasks/foo.md"))
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte("---\nfoo: bar\n---\nbody"))
					}),
				)
				client = gitrestclient.NewGitRestClientForTest(server.URL, "", "", zeroBackoff)
			})

			It("returns the file bytes and nil error", func() {
				content, err := client.Get(ctx, "tasks/foo.md")
				Expect(err).To(BeNil())
				Expect(content).To(Equal([]byte("---\nfoo: bar\n---\nbody")))
			})
		})

		Context("404 response", func() {
			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.WriteHeader(http.StatusNotFound)
					}),
				)
				client = gitrestclient.NewGitRestClientForTest(server.URL, "", "", zeroBackoff)
			})

			It("returns a non-nil error", func() {
				content, err := client.Get(ctx, "tasks/missing.md")
				Expect(err).NotTo(BeNil())
				Expect(content).To(BeNil())
			})
		})

		Context("network error (stopped server)", func() {
			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
				)
				client = gitrestclient.NewGitRestClientForTest(server.URL, "", "", zeroBackoff)
				server.Close()
				server = nil
			})

			It("returns a non-nil error", func() {
				content, err := client.Get(ctx, "tasks/foo.md")
				Expect(err).NotTo(BeNil())
				Expect(content).To(BeNil())
			})
		})

		Context("relPath contains percent sign", func() {
			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						// Reaching this handler at all proves the URL was escaped — before
						// the fix, http.NewRequestWithContext failed with `invalid URL escape "%er"`.
						// r.URL.Path is the decoded path; verify it round-trips intact.
						Expect(
							r.URL.Path,
						).To(Equal("/api/v1/files/24 Tasks/Set up The 5%ers prop firm account.md"))
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte("ok"))
					}),
				)
				client = gitrestclient.NewGitRestClientForTest(server.URL, "", "", zeroBackoff)
			})

			It("escapes the path and succeeds", func() {
				content, err := client.Get(ctx, "24 Tasks/Set up The 5%ers prop firm account.md")
				Expect(err).To(BeNil())
				Expect(content).To(Equal([]byte("ok")))
			})
		})
	})

	Describe("NewGitRestClient (public constructor)", func() {
		It("returns a non-nil client that can perform requests", func() {
			server = httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}),
			)
			c := gitrestclient.NewGitRestClient(server.URL, "", "", metrics.New())
			Expect(c).NotTo(BeNil())
			ready, err := c.IsReady(ctx)
			Expect(err).To(BeNil())
			Expect(ready).To(BeTrue())
		})
	})

	Describe("Post", func() {
		Context("success on first attempt", func() {
			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						Expect(r.Method).To(Equal(http.MethodPost))
						Expect(r.URL.Path).To(Equal("/api/v1/files/tasks/new.md"))
						w.WriteHeader(http.StatusCreated)
						_, _ = w.Write([]byte(`{"ok":true}`))
					}),
				)
				client = gitrestclient.NewGitRestClientForTest(server.URL, "", "", zeroBackoff)
			})

			It("returns nil", func() {
				err := client.Post(ctx, "tasks/new.md", []byte("content"))
				Expect(err).To(BeNil())
			})
		})

		Context("success after 1 retry", func() {
			var callCount int

			BeforeEach(func() {
				callCount = 0
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						callCount++
						if callCount < 2 {
							w.WriteHeader(http.StatusServiceUnavailable)
							return
						}
						w.WriteHeader(http.StatusOK)
					}),
				)
				client = gitrestclient.NewGitRestClientForTest(server.URL, "", "", zeroBackoff)
			})

			It("returns nil and makes 2 requests", func() {
				err := client.Post(ctx, "tasks/new.md", []byte("content"))
				Expect(err).To(BeNil())
				Expect(callCount).To(Equal(2))
			})
		})

		Context("fail after 5 attempts", func() {
			var callCount int

			BeforeEach(func() {
				callCount = 0
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						callCount++
						w.WriteHeader(http.StatusServiceUnavailable)
					}),
				)
				client = gitrestclient.NewGitRestClientForTest(server.URL, "", "", zeroBackoff)
			})

			It("returns a non-nil error after 5 requests", func() {
				err := client.Post(ctx, "tasks/new.md", []byte("content"))
				Expect(err).NotTo(BeNil())
				Expect(callCount).To(Equal(5))
			})
		})

		Context("context cancelled during backoff", func() {
			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.WriteHeader(http.StatusServiceUnavailable)
					}),
				)
				// shortBackoff so the context.Done() branch is exercised in the select
				client = gitrestclient.NewGitRestClientForTest(server.URL, "", "", shortBackoff)
			})

			It("returns a non-nil error when context is pre-cancelled", func() {
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel()
				err := client.Post(cancelCtx, "tasks/new.md", []byte("content"))
				Expect(err).NotTo(BeNil())
			})
		})
	})

	Describe("Delete", func() {
		Context("success", func() {
			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						Expect(r.Method).To(Equal(http.MethodDelete))
						Expect(r.URL.Path).To(Equal("/api/v1/files/tasks/old.md"))
						w.WriteHeader(http.StatusOK)
					}),
				)
				client = gitrestclient.NewGitRestClientForTest(server.URL, "", "", zeroBackoff)
			})

			It("returns nil", func() {
				err := client.Delete(ctx, "tasks/old.md")
				Expect(err).To(BeNil())
			})
		})

		Context("fail after 5 attempts", func() {
			var callCount int

			BeforeEach(func() {
				callCount = 0
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						callCount++
						w.WriteHeader(http.StatusServiceUnavailable)
					}),
				)
				client = gitrestclient.NewGitRestClientForTest(server.URL, "", "", zeroBackoff)
			})

			It("returns a non-nil error after 5 requests", func() {
				err := client.Delete(ctx, "tasks/old.md")
				Expect(err).NotTo(BeNil())
				Expect(callCount).To(Equal(5))
			})
		})

		Context("context cancelled during backoff", func() {
			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.WriteHeader(http.StatusServiceUnavailable)
					}),
				)
				client = gitrestclient.NewGitRestClientForTest(server.URL, "", "", shortBackoff)
			})

			It("returns a non-nil error when context is pre-cancelled", func() {
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel()
				err := client.Delete(cancelCtx, "tasks/old.md")
				Expect(err).NotTo(BeNil())
			})
		})
	})

	Describe("List", func() {
		Context("2 paths returned", func() {
			var receivedGlob string

			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						Expect(r.Method).To(Equal(http.MethodGet))
						receivedGlob = r.URL.Query().Get("glob")
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte(`["tasks/foo.md","tasks/bar.md"]`))
					}),
				)
				client = gitrestclient.NewGitRestClientForTest(server.URL, "", "", zeroBackoff)
			})

			It("returns the paths and propagates the glob query param", func() {
				paths, err := client.List(ctx, "tasks/*.md")
				Expect(err).To(BeNil())
				Expect(paths).To(Equal([]string{"tasks/foo.md", "tasks/bar.md"}))
				Expect(receivedGlob).To(Equal("tasks/*.md"))
			})
		})

		Context("empty array", func() {
			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte(`[]`))
					}),
				)
				client = gitrestclient.NewGitRestClientForTest(server.URL, "", "", zeroBackoff)
			})

			It("returns a non-nil empty slice and nil error", func() {
				paths, err := client.List(ctx, "tasks/*.md")
				Expect(err).To(BeNil())
				Expect(paths).NotTo(BeNil())
				Expect(paths).To(BeEmpty())
			})
		})

		Context("malformed JSON", func() {
			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte(`not-json`))
					}),
				)
				client = gitrestclient.NewGitRestClientForTest(server.URL, "", "", zeroBackoff)
			})

			It("returns nil and a non-nil error", func() {
				paths, err := client.List(ctx, "tasks/*.md")
				Expect(err).NotTo(BeNil())
				Expect(paths).To(BeNil())
			})
		})

		Context("non-200 response", func() {
			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = w.Write([]byte("internal error"))
					}),
				)
				client = gitrestclient.NewGitRestClientForTest(server.URL, "", "", zeroBackoff)
			})

			It("returns nil and a non-nil error", func() {
				paths, err := client.List(ctx, "tasks/*.md")
				Expect(err).NotTo(BeNil())
				Expect(paths).To(BeNil())
			})
		})

		Context("network error (stopped server)", func() {
			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
				)
				client = gitrestclient.NewGitRestClientForTest(server.URL, "", "", zeroBackoff)
				server.Close()
				server = nil
			})

			It("returns nil and a non-nil error", func() {
				paths, err := client.List(ctx, "tasks/*.md")
				Expect(err).NotTo(BeNil())
				Expect(paths).To(BeNil())
			})
		})
	})

	Describe("IsReady", func() {
		Context("200 response", func() {
			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						Expect(r.URL.Path).To(Equal("/readiness"))
						w.WriteHeader(http.StatusOK)
					}),
				)
				client = gitrestclient.NewGitRestClientForTest(server.URL, "", "", zeroBackoff)
			})

			It("returns true, nil", func() {
				ready, err := client.IsReady(ctx)
				Expect(err).To(BeNil())
				Expect(ready).To(BeTrue())
			})
		})

		Context("503 response", func() {
			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.WriteHeader(http.StatusServiceUnavailable)
					}),
				)
				client = gitrestclient.NewGitRestClientForTest(server.URL, "", "", zeroBackoff)
			})

			It("returns false, nil (not an error)", func() {
				ready, err := client.IsReady(ctx)
				Expect(err).To(BeNil())
				Expect(ready).To(BeFalse())
			})
		})

		Context("network error (stopped server)", func() {
			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
				)
				client = gitrestclient.NewGitRestClientForTest(server.URL, "", "", zeroBackoff)
				server.Close()
				server = nil
			})

			It("returns false, error", func() {
				ready, err := client.IsReady(ctx)
				Expect(err).NotTo(BeNil())
				Expect(ready).To(BeFalse())
			})
		})

		Context("unexpected status code", func() {
			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.WriteHeader(http.StatusBadRequest)
					}),
				)
				client = gitrestclient.NewGitRestClientForTest(server.URL, "", "", zeroBackoff)
			})

			It("returns false, error", func() {
				ready, err := client.IsReady(ctx)
				Expect(err).NotTo(BeNil())
				Expect(ready).To(BeFalse())
			})
		})
	})

	Describe("Gateway auth header propagation", func() {
		Context("Get sends X-Gateway-Secret and X-Gateway-Initator when configured", func() {
			var capturedSecret, capturedInitiator string

			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						capturedSecret = r.Header.Get("X-Gateway-Secret")
						capturedInitiator = r.Header.Get("X-Gateway-Initator")
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte("body"))
					}),
				)
				client = gitrestclient.NewGitRestClient(
					server.URL,
					"test-secret",
					"test-caller",
					metrics.New(),
				)
			})

			It("sends both auth headers", func() {
				_, err := client.Get(ctx, "tasks/foo.md")
				Expect(err).To(BeNil())
				Expect(capturedSecret).To(Equal("test-secret"))
				Expect(capturedInitiator).To(Equal("test-caller"))
			})
		})

		Context("Post sends both headers on each retry", func() {
			var callCount int
			var capturedSecrets, capturedInitiators []string

			BeforeEach(func() {
				callCount = 0
				capturedSecrets = nil
				capturedInitiators = nil
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						callCount++
						capturedSecrets = append(capturedSecrets, r.Header.Get("X-Gateway-Secret"))
						capturedInitiators = append(
							capturedInitiators,
							r.Header.Get("X-Gateway-Initator"),
						)
						if callCount < 3 {
							w.WriteHeader(http.StatusServiceUnavailable)
							return
						}
						w.WriteHeader(http.StatusOK)
					}),
				)
				client = gitrestclient.NewGitRestClientForTest(
					server.URL,
					"test-secret",
					"test-caller",
					zeroBackoff,
				)
			})

			It("sends both auth headers on all 3 attempts", func() {
				err := client.Post(ctx, "tasks/new.md", []byte("content"))
				Expect(err).To(BeNil())
				Expect(callCount).To(Equal(3))
				for i := 0; i < 3; i++ {
					Expect(capturedSecrets[i]).To(Equal("test-secret"))
					Expect(capturedInitiators[i]).To(Equal("test-caller"))
				}
			})
		})

		Context("Delete sends both auth headers", func() {
			var capturedSecret, capturedInitiator string

			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						capturedSecret = r.Header.Get("X-Gateway-Secret")
						capturedInitiator = r.Header.Get("X-Gateway-Initator")
						w.WriteHeader(http.StatusOK)
					}),
				)
				client = gitrestclient.NewGitRestClientForTest(
					server.URL,
					"test-secret",
					"test-caller",
					zeroBackoff,
				)
			})

			It("sends both auth headers", func() {
				err := client.Delete(ctx, "tasks/old.md")
				Expect(err).To(BeNil())
				Expect(capturedSecret).To(Equal("test-secret"))
				Expect(capturedInitiator).To(Equal("test-caller"))
			})
		})

		Context("List sends both auth headers", func() {
			var capturedSecret, capturedInitiator string

			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						capturedSecret = r.Header.Get("X-Gateway-Secret")
						capturedInitiator = r.Header.Get("X-Gateway-Initator")
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte(`[]`))
					}),
				)
				client = gitrestclient.NewGitRestClientForTest(
					server.URL,
					"test-secret",
					"test-caller",
					zeroBackoff,
				)
			})

			It("sends both auth headers", func() {
				_, err := client.List(ctx, "tasks/*.md")
				Expect(err).To(BeNil())
				Expect(capturedSecret).To(Equal("test-secret"))
				Expect(capturedInitiator).To(Equal("test-caller"))
			})
		})

		Context("IsReady does NOT send auth headers", func() {
			var capturedSecret, capturedInitiator string

			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						capturedSecret = r.Header.Get("X-Gateway-Secret")
						capturedInitiator = r.Header.Get("X-Gateway-Initator")
						w.WriteHeader(http.StatusOK)
					}),
				)
				client = gitrestclient.NewGitRestClientForTest(
					server.URL,
					"test-secret",
					"test-caller",
					zeroBackoff,
				)
			})

			It("sends no auth headers to /readiness", func() {
				ready, err := client.IsReady(ctx)
				Expect(err).To(BeNil())
				Expect(ready).To(BeTrue())
				Expect(capturedSecret).To(BeEmpty())
				Expect(capturedInitiator).To(BeEmpty())
			})
		})

		Context("empty secret sends no auth headers (backward compat)", func() {
			var capturedSecret, capturedInitiator string

			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						capturedSecret = r.Header.Get("X-Gateway-Secret")
						capturedInitiator = r.Header.Get("X-Gateway-Initator")
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte("body"))
					}),
				)
				client = gitrestclient.NewGitRestClient(server.URL, "", "", metrics.New())
			})

			It("sends no auth headers when secret is empty", func() {
				_, err := client.Get(ctx, "tasks/foo.md")
				Expect(err).To(BeNil())
				Expect(capturedSecret).To(BeEmpty())
				Expect(capturedInitiator).To(BeEmpty())
			})
		})

		Context("server returns 401 (invalid secret)", func() {
			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.WriteHeader(http.StatusUnauthorized)
						_, _ = w.Write(
							[]byte(
								"secret in header 'X-Gateway-Secret' is invalid => access denied",
							),
						)
					}),
				)
				client = gitrestclient.NewGitRestClientForTest(
					server.URL,
					"wrong-secret",
					"test-caller",
					zeroBackoff,
				)
			})

			It("returns a non-nil error", func() {
				content, err := client.Get(ctx, "tasks/foo.md")
				Expect(err).NotTo(BeNil())
				Expect(content).To(BeNil())
			})
		})

		Context("server returns 500 (missing initiator header)", func() {
			BeforeEach(func() {
				server = httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = w.Write([]byte("header 'X-Gateway-Initator' missing"))
					}),
				)
				client = gitrestclient.NewGitRestClientForTest(
					server.URL,
					"test-secret",
					"",
					zeroBackoff,
				)
			})

			It("returns a non-nil error", func() {
				content, err := client.Get(ctx, "tasks/foo.md")
				Expect(err).NotTo(BeNil())
				Expect(content).To(BeNil())
			})
		})
	})
})
