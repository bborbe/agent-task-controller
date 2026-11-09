// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"fmt"

	lib "github.com/bborbe/agent"
	task "github.com/bborbe/agent/command/task"
	libtimemocks "github.com/bborbe/time/mocks"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/command"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

// newUpdateGoTask builds one listing row shaped the way the emitter writes it.
func newUpdateGoTask(id, title, repo, status string) vaultapi.Task {
	return vaultapi.Task{
		ID:    id,
		Title: title,
		Frontmatter: map[string]any{
			"task_type": "github-update-go",
			"repo":      repo,
			"status":    status,
		},
	}
}

var _ = Describe("SupersedePriorUpdateGoTasks", func() {
	// The typed listing is the candidate set. status and task_type are pushed to
	// the API — the scan never reads a closed row, which is what the old glob scan
	// spent its whole look-back budget on before reaching the first open prior.
	It("asks the API for open update-go tasks and aborts the prior for the repo", func() {
		const repo = "bborbe/alert"
		const newRelPath = "tasks/Update Go bborbe-alert newsha0.md"

		fakeClient := &mocks.VaultAPIClient{}
		fakeClient.ListTasksReturns([]vaultapi.Task{
			newUpdateGoTask("new-id", "Update Go bborbe-alert newsha0", repo, "in_progress"),
			newUpdateGoTask("prior-1", "Update Go bborbe-alert 1111111", repo, "in_progress"),
			// A different repo's task: same type, same title shape, must never be
			// aborted — two repos' update queues are independent work.
			newUpdateGoTask(
				"other-1",
				"Update Go bborbe-vault-cli 2222222",
				"bborbe/vault-cli",
				"in_progress",
			),
		}, nil)

		command.SupersedePriorUpdateGoTasks(
			context.Background(),
			fakeClient,
			&libtimemocks.CurrentDateTimeGetter{},
			7,
			task.CreateCommand{
				TaskIdentifier: lib.TaskIdentifier("new-id"),
				Frontmatter: lib.TaskFrontmatter{
					"task_type": "github-update-go",
					"repo":      repo,
				},
			},
			newRelPath,
		)

		_, filter := fakeClient.ListTasksArgsForCall(0)
		Expect(filter.Status).To(Equal("in_progress"))
		Expect(filter.TaskTypes).To(ConsistOf("github-update-go"))

		Expect(fakeClient.PatchTaskCallCount()).To(Equal(1))
		_, id, fields := fakeClient.PatchTaskArgsForCall(0)
		Expect(id).To(Equal("prior-1"))
		Expect(fields["status"]).To(Equal("aborted"))
		Expect(fields["phase"]).To(Equal("done"))
		Expect(fields["completed_date"]).To(BeAssignableToTypeOf(""))
		Expect(fields["completed_date"]).NotTo(BeEmpty())
		Expect(fields["superseded_by"]).To(Equal(newRelPath))
		// Attribution must stay truthful: an update-go prior was emitted by the
		// update-go watcher, so stamping created_by: recurring-task-creator (as the
		// recurring hook does) would replace its real attribution with a false one.
		Expect(fields).NotTo(HaveKey("created_by"))
	})

	// The budget bounds how many OPEN priors get aborted. Without this spec,
	// deleting the `openFound >= k` break would leave the other spec green.
	It("stops after k open priors and leaves the rest open", func() {
		const repo = "bborbe/alert"
		const k = 2

		tasks := make([]vaultapi.Task, 0, 4)
		tasks = append(
			tasks,
			newUpdateGoTask("new-id", "Update Go bborbe-alert newsha0", repo, "in_progress"),
		)
		for _, sha := range []string{"aaaaaaa", "bbbbbbb", "ccccccc"} {
			tasks = append(
				tasks,
				newUpdateGoTask("prior-"+sha, "Update Go bborbe-alert "+sha, repo, "in_progress"),
			)
		}

		fakeClient := &mocks.VaultAPIClient{}
		fakeClient.ListTasksReturns(tasks, nil)

		command.SupersedePriorUpdateGoTasks(
			context.Background(),
			fakeClient,
			&libtimemocks.CurrentDateTimeGetter{},
			k,
			task.CreateCommand{
				TaskIdentifier: lib.TaskIdentifier("new-id"),
				Frontmatter: lib.TaskFrontmatter{
					"task_type": "github-update-go",
					"repo":      repo,
				},
			},
			"tasks/Update Go bborbe-alert newsha0.md",
		)

		Expect(fakeClient.PatchTaskCallCount()).To(Equal(k))
		aborted := make(map[string]bool, k)
		for i := range k {
			_, id, _ := fakeClient.PatchTaskArgsForCall(i)
			aborted[id] = true
		}
		Expect(aborted).To(HaveKey("prior-aaaaaaa"))
		Expect(aborted).To(HaveKey("prior-bbbbbbb"))
		Expect(aborted).NotTo(HaveKey("prior-ccccccc"))
	})

	It("does nothing when the command carries no repo", func() {
		fakeClient := &mocks.VaultAPIClient{}

		command.SupersedePriorUpdateGoTasks(
			context.Background(),
			fakeClient,
			&libtimemocks.CurrentDateTimeGetter{},
			7,
			task.CreateCommand{
				TaskIdentifier: lib.TaskIdentifier("new-id"),
				Frontmatter: lib.TaskFrontmatter{
					"task_type": "github-update-go",
				},
			},
			"tasks/Update Go x.md",
		)

		Expect(fakeClient.ListTasksCallCount()).To(Equal(0))
		Expect(fakeClient.PatchTaskCallCount()).To(Equal(0))
	})

	It("does nothing when a listing error is reported", func() {
		fakeClient := &mocks.VaultAPIClient{}
		fakeClient.ListTasksReturns(nil, fmt.Errorf("boom"))

		command.SupersedePriorUpdateGoTasks(
			context.Background(),
			fakeClient,
			&libtimemocks.CurrentDateTimeGetter{},
			7,
			task.CreateCommand{
				TaskIdentifier: lib.TaskIdentifier("new-id"),
				Frontmatter: lib.TaskFrontmatter{
					"task_type": "github-update-go",
					"repo":      "bborbe/alert",
				},
			},
			"tasks/Update Go bborbe-alert newsha0.md",
		)

		Expect(fakeClient.PatchTaskCallCount()).To(Equal(0))
	})
})
