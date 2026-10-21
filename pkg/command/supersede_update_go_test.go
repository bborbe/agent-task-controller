// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/command"
)

var _ = Describe("IsOpenUpdateGoTaskForRepo", func() {
	build := func(taskType, repo, status string) []byte {
		return []byte(
			"---\n" +
				"task_type: " + taskType + "\n" +
				"repo: " + repo + "\n" +
				"status: " + status + "\n" +
				"---\n\nbody\n",
		)
	}

	It("accepts a still-open update task for the same repo", func() {
		Expect(command.IsOpenUpdateGoTaskForRepo(
			context.Background(),
			build("github-update-go", "bborbe/alert", "in_progress"),
			"bborbe/alert",
		)).To(BeTrue())
	})

	It("rejects a task for a different repo", func() {
		// The abort decision must never cross repos: two update tasks for two
		// repos are independent work, not a duplicate.
		Expect(command.IsOpenUpdateGoTaskForRepo(
			context.Background(),
			build("github-update-go", "bborbe/vault-cli", "in_progress"),
			"bborbe/alert",
		)).To(BeFalse())
	})

	It("rejects an already-aborted task", func() {
		Expect(command.IsOpenUpdateGoTaskForRepo(
			context.Background(),
			build("github-update-go", "bborbe/alert", "aborted"),
			"bborbe/alert",
		)).To(BeFalse())
	})

	It("rejects a decision task, which is an operator question and not a run", func() {
		Expect(command.IsOpenUpdateGoTaskForRepo(
			context.Background(),
			build("github-update-go-decision", "bborbe/alert", "in_progress"),
			"bborbe/alert",
		)).To(BeFalse())
	})

	It("rejects content with no frontmatter", func() {
		Expect(command.IsOpenUpdateGoTaskForRepo(
			context.Background(),
			[]byte("not a task file\n"),
			"bborbe/alert",
		)).To(BeFalse())
	})
})

var _ = Describe("ListUpdateGoCandidateTitles", func() {
	// Filenames exactly as the emitter writes them: "Update Go <owner>-<repo> <sha>.md".
	// The repo slug is only the MIDDLE token, so a glob built from the slug alone
	// matches nothing — that shipped silently in v0.13.0-v0.14.1 and left the
	// supersede hook a no-op on every create.
	vaultFiles := []string{
		"tasks/Update Go bborbe-alert 1111111.md",
		"tasks/Update Go bborbe-alert 2222222.md",
		"tasks/Update Go bborbe-alert-controller 3333333.md",
		"tasks/Update Go bborbe-vault-cli 4444444.md",
		"tasks/PR Review github - bborbe-alert - 1 - a52b3fa4 - update-go-module-dependencies.md",
	}

	// newGlobFakeGit applies real glob semantics over vaultFiles, so a glob that
	// matches nothing fails the test instead of silently returning an empty slice.
	newGlobFakeGit := func(globSeen *string) *mocks.GitClient {
		fakeGit := &mocks.GitClient{}
		fakeGit.ListFilesStub = func(_ context.Context, glob string) ([]string, error) {
			*globSeen = glob
			var matched []string
			for _, path := range vaultFiles {
				if ok, err := filepath.Match(glob, path); err == nil && ok {
					matched = append(matched, path)
				}
			}
			return matched, nil
		}
		return fakeGit
	}

	It("scopes the glob to the repo's update tasks, not to every task", func() {
		var globSeen string
		fakeGit := newGlobFakeGit(&globSeen)

		titles, err := command.ListUpdateGoCandidateTitles(
			context.Background(),
			fakeGit,
			"tasks",
			"bborbe/alert",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(globSeen).To(Equal("tasks/Update Go bborbe-alert *.md"))
		Expect(titles).To(ConsistOf(
			"Update Go bborbe-alert 1111111",
			"Update Go bborbe-alert 2222222",
		))
	})

	It("falls back to listing every task file when the repo carries a glob metacharacter", func() {
		var globSeen string
		fakeGit := newGlobFakeGit(&globSeen)

		_, err := command.ListUpdateGoCandidateTitles(
			context.Background(),
			fakeGit,
			"tasks",
			"bborbe/we*ird",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(globSeen).To(Equal("tasks/*.md"))
	})
})
