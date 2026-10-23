// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gitrestclient_test

import (
	"context"
	stderrors "errors"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/gitrestclient"
)

// fastBackoff keeps the readiness-retry specs at millisecond scale; the real
// 2s/4s/8s/16s schedule is exercised in the cluster, not here.
func fastBackoff(attempt int) time.Duration {
	return time.Duration(attempt) * time.Millisecond
}

var _ = Describe("gitRestGitClientAdapter", func() {
	var (
		ctx        context.Context
		fakeClient *mocks.GitRestClient
		adapter    interface {
			EnsureCloned(context.Context) error
			Pull(context.Context) error
			CommitAndPush(context.Context, string) error
			Path() string
			AtomicWriteAndCommitPush(context.Context, string, []byte, string) error
			AtomicWriteIfAbsentAndCommitPush(context.Context, string, []byte, string) error
			AtomicReadModifyWriteAndCommitPush(context.Context, string, func([]byte) ([]byte, error), string) error
			ListFiles(context.Context, string) ([]string, error)
			ReadFile(context.Context, string) ([]byte, error)
			WriteFile(context.Context, string, []byte) error
		}
		basePath string
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeClient = &mocks.GitRestClient{}
		basePath = "/data/vault"
		adapter = gitrestclient.NewGitClientForTest(fakeClient, basePath, fastBackoff)
	})

	Describe("EnsureCloned", func() {
		It("returns nil without retrying when git-rest is ready", func() {
			fakeClient.IsReadyReturns(true, nil)
			Expect(adapter.EnsureCloned(ctx)).To(Succeed())
			Expect(fakeClient.IsReadyCallCount()).To(Equal(1))
		})

		It("retries a refused connection and succeeds once the endpoint answers", func() {
			refused := stderrors.New("dial tcp: connect: connection refused")
			fakeClient.IsReadyReturnsOnCall(0, false, refused)
			fakeClient.IsReadyReturnsOnCall(1, false, refused)
			fakeClient.IsReadyReturnsOnCall(2, true, nil)

			Expect(adapter.EnsureCloned(ctx)).To(Succeed())
			Expect(fakeClient.IsReadyCallCount()).To(Equal(3))
		})

		It("retries a not-ready (503) answer and succeeds once the endpoint answers", func() {
			fakeClient.IsReadyReturnsOnCall(0, false, nil)
			fakeClient.IsReadyReturnsOnCall(1, true, nil)

			Expect(adapter.EnsureCloned(ctx)).To(Succeed())
			Expect(fakeClient.IsReadyCallCount()).To(Equal(2))
		})

		It("warns on every refused attempt, quoting the attempt number and the failure", func() {
			refused := stderrors.New("dial tcp: connect: connection refused")
			fakeClient.IsReadyReturns(false, refused)

			var err error
			out := captureGlogWarnings(func() { err = adapter.EnsureCloned(ctx) })
			Expect(err).To(HaveOccurred())

			Expect(out).To(ContainSubstring("attempt 1 of 5"))
			Expect(out).To(ContainSubstring("attempt 5 of 5"))
			Expect(strings.Count(out, "git-rest readiness probe failed on attempt")).To(Equal(5))
			Expect(out).To(ContainSubstring("connection refused"))
		})

		It("warns with the 503 wording when the endpoint answers not-ready", func() {
			fakeClient.IsReadyReturns(false, nil)

			var err error
			out := captureGlogWarnings(func() { err = adapter.EnsureCloned(ctx) })
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring("git-rest not ready (503)"))
		})

		It("fails after exactly 5 attempts when the endpoint never answers", func() {
			fakeClient.IsReadyReturns(false, stderrors.New("connection refused"))

			err := adapter.EnsureCloned(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("probe git-rest readiness"))
			Expect(fakeClient.IsReadyCallCount()).To(Equal(5))
		})

		It("fails after exactly 5 attempts when the endpoint never becomes ready", func() {
			fakeClient.IsReadyReturns(false, nil)

			err := adapter.EnsureCloned(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not ready"))
			Expect(err.Error()).To(ContainSubstring("after 5 attempts"))
			Expect(fakeClient.IsReadyCallCount()).To(Equal(5))
		})

		It("gives each attempt its own deadline so a hang cannot outlive the budget", func() {
			var deadlines []time.Time
			fakeClient.IsReadyCalls(func(probeCtx context.Context) (bool, error) {
				deadline, ok := probeCtx.Deadline()
				Expect(ok).To(BeTrue())
				deadlines = append(deadlines, deadline)
				return false, stderrors.New("connection refused")
			})

			Expect(adapter.EnsureCloned(ctx)).To(HaveOccurred())
			Expect(deadlines).To(HaveLen(5))
			for _, deadline := range deadlines {
				Expect(time.Until(deadline)).To(BeNumerically("<=", 2*time.Second))
			}
		})

		It("stops retrying when the context is cancelled during backoff", func() {
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			fakeClient.IsReadyReturns(false, stderrors.New("connection refused"))

			err := adapter.EnsureCloned(cancelled)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cancelled"))
		})
	})

	Describe("Pull", func() {
		It("always returns nil (no-op)", func() {
			Expect(adapter.Pull(ctx)).To(Succeed())
		})
	})

	Describe("CommitAndPush", func() {
		It("always returns nil (no-op)", func() {
			Expect(adapter.CommitAndPush(ctx, "some message")).To(Succeed())
		})
	})

	Describe("Path", func() {
		It("returns the basePath passed to NewGitClient", func() {
			Expect(adapter.Path()).To(Equal(basePath))
		})
	})

	Describe("AtomicWriteAndCommitPush", func() {
		It("calls Post with the relative path and content", func() {
			content := []byte("hello")
			absPath := "/data/vault/tasks/foo.md"
			Expect(
				adapter.AtomicWriteAndCommitPush(ctx, absPath, content, "update foo"),
			).To(Succeed())
			Expect(fakeClient.PostCallCount()).To(Equal(1))
			_, relPath, postedContent := fakeClient.PostArgsForCall(0)
			Expect(relPath).To(Equal("tasks/foo.md"))
			Expect(postedContent).To(Equal(content))
		})

		It("returns an error when absPath is outside basePath", func() {
			err := adapter.AtomicWriteAndCommitPush(ctx, "/other/path/file.md", []byte("x"), "msg")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not under basePath"))
		})

		It("returns wrapped error when Post fails", func() {
			fakeClient.PostReturns(stderrors.New("server error"))
			err := adapter.AtomicWriteAndCommitPush(
				ctx,
				"/data/vault/tasks/foo.md",
				[]byte("x"),
				"msg",
			)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("AtomicWriteIfAbsentAndCommitPush", func() {
		It("calls PostIfAbsent with the relative path and content", func() {
			content := []byte("hello")
			absPath := "/data/vault/tasks/foo.md"
			Expect(
				adapter.AtomicWriteIfAbsentAndCommitPush(ctx, absPath, content, "create foo"),
			).To(Succeed())
			Expect(fakeClient.PostIfAbsentCallCount()).To(Equal(1))
			_, relPath, postedContent := fakeClient.PostIfAbsentArgsForCall(0)
			Expect(relPath).To(Equal("tasks/foo.md"))
			Expect(postedContent).To(Equal(content))
		})

		It("returns an error when absPath is outside basePath", func() {
			err := adapter.AtomicWriteIfAbsentAndCommitPush(
				ctx,
				"/other/path/file.md",
				[]byte("x"),
				"msg",
			)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not under basePath"))
		})

		It("returns ErrAlreadyExists unchanged when PostIfAbsent reports a conflict", func() {
			fakeClient.PostIfAbsentReturns(gitrestclient.ErrAlreadyExists)
			err := adapter.AtomicWriteIfAbsentAndCommitPush(
				ctx,
				"/data/vault/tasks/foo.md",
				[]byte("x"),
				"msg",
			)
			Expect(err).To(HaveOccurred())
			Expect(stderrors.Is(err, gitrestclient.ErrAlreadyExists)).To(BeTrue())
		})
	})

	Describe("AtomicReadModifyWriteAndCommitPush", func() {
		It("calls Get then Post; content passed to Post is modify's return value", func() {
			existing := []byte("original")
			modified := []byte("modified")
			fakeClient.GetReturns(existing, nil)

			absPath := "/data/vault/tasks/bar.md"
			err := adapter.AtomicReadModifyWriteAndCommitPush(
				ctx,
				absPath,
				func(current []byte) ([]byte, error) {
					Expect(current).To(Equal(existing))
					return modified, nil
				},
				"update bar",
			)
			Expect(err).To(Succeed())

			Expect(fakeClient.GetCallCount()).To(Equal(1))
			_, getRelPath := fakeClient.GetArgsForCall(0)
			Expect(getRelPath).To(Equal("tasks/bar.md"))

			Expect(fakeClient.PostCallCount()).To(Equal(1))
			_, postRelPath, postContent := fakeClient.PostArgsForCall(0)
			Expect(postRelPath).To(Equal("tasks/bar.md"))
			Expect(postContent).To(Equal(modified))
		})

		It("does not call Post when modify returns the content unchanged", func() {
			existing := []byte("same")
			fakeClient.GetReturns(existing, nil)
			err := adapter.AtomicReadModifyWriteAndCommitPush(
				ctx,
				"/data/vault/tasks/bar.md",
				func(current []byte) ([]byte, error) { return []byte("same"), nil },
				"msg",
			)
			Expect(err).To(Succeed())
			Expect(fakeClient.GetCallCount()).To(Equal(1))
			Expect(fakeClient.PostCallCount()).To(Equal(0))
		})

		It("returns error and does not call Post when Get fails", func() {
			fakeClient.GetReturns(nil, stderrors.New("not found"))
			err := adapter.AtomicReadModifyWriteAndCommitPush(
				ctx,
				"/data/vault/tasks/bar.md",
				func(current []byte) ([]byte, error) { return current, nil },
				"msg",
			)
			Expect(err).To(HaveOccurred())
			Expect(fakeClient.PostCallCount()).To(Equal(0))
		})

		It("returns error and does not call Post when modify returns error", func() {
			fakeClient.GetReturns([]byte("data"), nil)
			modifyErr := stderrors.New("modify failed")
			err := adapter.AtomicReadModifyWriteAndCommitPush(
				ctx,
				"/data/vault/tasks/bar.md",
				func(current []byte) ([]byte, error) { return nil, modifyErr },
				"msg",
			)
			Expect(err).To(HaveOccurred())
			Expect(fakeClient.PostCallCount()).To(Equal(0))
		})
	})

	Describe("ListFiles", func() {
		It("delegates to GitRestClient.List with the glob passed through", func() {
			expected := []string{"tasks/a.md", "tasks/b.md"}
			fakeClient.ListReturns(expected, nil)

			result, err := adapter.ListFiles(ctx, "tasks/*.md")
			Expect(err).To(Succeed())
			Expect(result).To(Equal(expected))

			Expect(fakeClient.ListCallCount()).To(Equal(1))
			_, glob := fakeClient.ListArgsForCall(0)
			Expect(glob).To(Equal("tasks/*.md"))
		})
	})

	Describe("ReadFile", func() {
		It("delegates to GitRestClient.Get with relPath passed through", func() {
			content := []byte("file content")
			fakeClient.GetReturns(content, nil)

			result, err := adapter.ReadFile(ctx, "tasks/foo.md")
			Expect(err).To(Succeed())
			Expect(result).To(Equal(content))

			Expect(fakeClient.GetCallCount()).To(Equal(1))
			_, relPath := fakeClient.GetArgsForCall(0)
			Expect(relPath).To(Equal("tasks/foo.md"))
		})
	})

	Describe("WriteFile", func() {
		It("delegates to GitRestClient.Post with relPath and content passed through", func() {
			content := []byte("new content")
			Expect(adapter.WriteFile(ctx, "tasks/foo.md", content)).To(Succeed())

			Expect(fakeClient.PostCallCount()).To(Equal(1))
			_, relPath, postedContent := fakeClient.PostArgsForCall(0)
			Expect(relPath).To(Equal("tasks/foo.md"))
			Expect(postedContent).To(Equal(content))
		})
	})

	Describe("Round-trip via fake (WriteFile then ReadFile)", func() {
		It("confirms argument plumbing is symmetric", func() {
			written := []byte("round-trip content")
			Expect(adapter.WriteFile(ctx, "tasks/rt.md", written)).To(Succeed())

			// Simulate the fake echoing back what was posted
			_, _, lastPosted := fakeClient.PostArgsForCall(0)
			fakeClient.GetReturns(lastPosted, nil)

			read, err := adapter.ReadFile(ctx, "tasks/rt.md")
			Expect(err).To(Succeed())
			Expect(read).To(Equal(written))
		})
	})
})
