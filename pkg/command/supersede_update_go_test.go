// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	lib "github.com/bborbe/agent"
	task "github.com/bborbe/agent/command/task"
	libtimemocks "github.com/bborbe/time/mocks"
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

		titles, err := command.ListUpdateGoCandidateTitles(
			context.Background(),
			fakeGit,
			"tasks",
			"bborbe/we*ird",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(globSeen).To(Equal("tasks/*.md"))
		// The fallback lists EVERY task file, so the in-memory HasPrefix filter is
		// the only thing keeping another repo's task out of the abort set. Assert
		// its result, not just the glob: a regression that drops the filter would
		// otherwise read — and potentially abort — unrelated repos' tasks silently.
		Expect(titles).To(BeEmpty())
	})
})

var _ = Describe("SupersedePriorUpdateGoTasks", func() {
	// A repo carries up to 66 historical update-go files (measured 2026-10-21) and
	// their names end in a commit SHA, so listing order is not recency order. The
	// look-back budget must bound how many OPEN priors get aborted, never how many
	// files get read — spending it on closed rows starved the hook of exactly the
	// work it exists to do.
	It("still aborts the open prior when closed rows fill the listing ahead of it", func() {
		const k = 7
		const taskDir = "tasks"
		const repo = "bborbe/alert"

		listed := make([]string, 0, 13)
		closed := map[string]bool{}
		for i := range 12 {
			title := fmt.Sprintf("Update Go bborbe-alert %06d", i)
			listed = append(listed, taskDir+"/"+title+".md")
			closed[title] = true
		}
		openTitle := "Update Go bborbe-alert zzzzzzz"
		listed = append(listed, taskDir+"/"+openTitle+".md")

		fakeGit := &mocks.GitClient{}
		fakeGit.ListFilesReturns(listed, nil)
		fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
			title := strings.TrimSuffix(filepath.Base(relPath), ".md")
			status := "in_progress"
			if closed[title] {
				status = "aborted"
			}
			return []byte(
				"---\ntask_type: github-update-go\nrepo: " + repo +
					"\nstatus: " + status + "\n---\nbody\n",
			), nil
		}

		cmd := task.CreateCommand{
			TaskIdentifier: lib.TaskIdentifier("new-id"),
			Frontmatter: lib.TaskFrontmatter{
				"task_type": "github-update-go",
				"repo":      repo,
			},
		}

		command.SupersedePriorUpdateGoTasks(
			context.Background(),
			fakeGit,
			taskDir,
			&libtimemocks.CurrentDateTimeGetter{},
			k,
			cmd,
			taskDir+"/Update Go bborbe-alert newsha0.md",
		)

		Expect(fakeGit.AtomicReadModifyWriteAndCommitPushCallCount()).To(Equal(1))
		_, absPath, _, _ := fakeGit.AtomicReadModifyWriteAndCommitPushArgsForCall(0)
		Expect(absPath).To(HaveSuffix(openTitle + ".md"))
	})
})
