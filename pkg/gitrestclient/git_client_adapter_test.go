// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gitrestclient_test

import (
	"context"
	stderrors "errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/gitrestclient"
)

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
		adapter = gitrestclient.NewGitClient(fakeClient, basePath)
	})

	Describe("EnsureCloned", func() {
		It("returns nil when git-rest is ready", func() {
			fakeClient.IsReadyReturns(true, nil)
			Expect(adapter.EnsureCloned(ctx)).To(Succeed())
		})

		It("returns error when git-rest returns not-ready (503)", func() {
			fakeClient.IsReadyReturns(false, nil)
			err := adapter.EnsureCloned(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not ready"))
		})

		It("returns wrapped error on network failure", func() {
			someErr := stderrors.New("connection refused")
			fakeClient.IsReadyReturns(false, someErr)
			err := adapter.EnsureCloned(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("probe git-rest readiness"))
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
