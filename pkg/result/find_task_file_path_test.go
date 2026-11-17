// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package result_test

import (
	"context"
	stdtime "time"

	lib "github.com/bborbe/agent"
	libtime "github.com/bborbe/time"
	libtimemocks "github.com/bborbe/time/mocks"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/result"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

// task builds the typed view FindTaskFilePath matches against. The path a task
// resolves to is composed from its title, so Title is what the specs assert on.
func task(id, title string) vaultapi.Task {
	return vaultapi.Task{
		ID:          id,
		Title:       title,
		Frontmatter: map[string]any{"task_identifier": id},
	}
}

// listed builds a listing item as the vault API returns it: a flat projection
// with an id and a title and no frontmatter.
func listed(id, title string) vaultapi.Task {
	return vaultapi.Task{ID: id, Title: title}
}

// readStub serves per-task reads by the title-derived address the walk uses,
// the way the real API resolves a task.
func readStub(
	byAddress map[string]vaultapi.Task,
) func(context.Context, string) (*vaultapi.Task, error) {
	return func(_ context.Context, address string) (*vaultapi.Task, error) {
		t, ok := byAddress[address]
		if !ok {
			return nil, vaultapi.ErrNotFound
		}
		return &t, nil
	}
}

var _ = Describe("FindTaskFilePath", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("lists the vault once and matches by identifier", func() {
		fakeVault := &mocks.VaultAPIClient{}
		fakeVault.ListTasksReturns([]vaultapi.Task{listed("foo", "a"), listed("bar", "b")}, nil)
		fakeVault.ReadTaskStub = readStub(map[string]vaultapi.Task{
			"a": task("foo", "a"),
			"b": task("bar", "b"),
		})

		matchedRelPath, _, err := result.FindTaskFilePath(ctx, fakeVault, "tasks", "bar", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(matchedRelPath).To(Equal("tasks/b.md"))
		// The listing is a flat projection that carries no frontmatter, so each
		// listed task is read once to match on its own frontmatter.
		Expect(fakeVault.ListTasksCallCount()).To(Equal(1))
		Expect(fakeVault.ReadTaskCallCount()).To(Equal(2))
	})

	// Regression: 2026-08-31. Two Schedule CRs sharing a name across the dev and
	// prod fleets minted the same UUID5, so two task files carried one
	// task_identifier. The match loop had no break and no duplicate check, so it
	// silently kept the LAST path from an unsorted listing — a result was written
	// onto `Sentry Alert Fan-Out - 2026-08-31` and marked it done/completed for a
	// run no executor performed. Picking either file is wrong; the only safe
	// outcome is a loud error.
	It("returns an error when two tasks share one task_identifier", func() {
		fakeVault := &mocks.VaultAPIClient{}
		fakeVault.ListTasksReturns(
			[]vaultapi.Task{listed("dup", "first"), listed("dup", "second")}, nil,
		)
		fakeVault.ReadTaskStub = readStub(map[string]vaultapi.Task{
			"first":  task("dup", "first"),
			"second": task("dup", "second"),
		})

		matchedRelPath, fm, err := result.FindTaskFilePath(ctx, fakeVault, "tasks", "dup", nil)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("duplicate task_identifier"))
		Expect(err.Error()).To(ContainSubstring("tasks/first.md"))
		Expect(err.Error()).To(ContainSubstring("tasks/second.md"))
		Expect(matchedRelPath).To(Equal(""))
		Expect(fm).To(BeNil())
	})

	It("still resolves when a duplicate exists for a DIFFERENT identifier", func() {
		fakeVault := &mocks.VaultAPIClient{}
		fakeVault.ListTasksReturns(
			[]vaultapi.Task{listed("dup", "a"), listed("dup", "b"), listed("target", "c")}, nil,
		)
		fakeVault.ReadTaskStub = readStub(map[string]vaultapi.Task{
			"a": task("dup", "a"),
			"b": task("dup", "b"),
			"c": task("target", "c"),
		})

		matchedRelPath, _, err := result.FindTaskFilePath(ctx, fakeVault, "tasks", "target", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(matchedRelPath).To(Equal("tasks/c.md"))
	})

	It("returns empty path when no task matches", func() {
		fakeVault := &mocks.VaultAPIClient{}
		fakeVault.ListTasksReturns([]vaultapi.Task{listed("other", "a")}, nil)
		fakeVault.ReadTaskStub = readStub(map[string]vaultapi.Task{
			"a": task("other", "a"),
		})

		matchedRelPath, fm, err := result.FindTaskFilePath(ctx, fakeVault, "tasks", "missing", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(matchedRelPath).To(Equal(""))
		Expect(fm).To(BeNil())
	})

	It("matches through the recorded predecessor", func() {
		fakeVault := &mocks.VaultAPIClient{}
		repaired := task("new-id", "good")
		repaired.Frontmatter["task_identifier_previous"] = "target"
		fakeVault.ListTasksReturns([]vaultapi.Task{listed("new-id", "good")}, nil)
		fakeVault.ReadTaskStub = readStub(map[string]vaultapi.Task{
			"good": repaired,
		})

		matchedRelPath, _, err := result.FindTaskFilePath(ctx, fakeVault, "tasks", "target", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(matchedRelPath).To(Equal("tasks/good.md"))
	})

	// Regression: the listing carries no frontmatter, so a walk that matched on the
	// listing item alone dropped every result whose identifier missed the index.
	// The walk must read each listed task and match on the frontmatter that read
	// returns.
	It("resolves an identifier the index misses through a per-task read", func() {
		fakeVault := &mocks.VaultAPIClient{}
		fakeVault.ListTasksReturns([]vaultapi.Task{listed("x", "a")}, nil)
		fakeVault.ReadTaskStub = readStub(map[string]vaultapi.Task{
			"a": task("target", "a"),
		})

		matchedRelPath, _, err := result.FindTaskFilePath(ctx, fakeVault, "tasks", "target", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(matchedRelPath).To(Equal("tasks/a.md"))
		Expect(fakeVault.ReadTaskCallCount()).To(BeNumerically(">=", 1))
		// The fake returns its stub whatever the argument is, so a call count alone
		// cannot prove the walk addressed the task the real API would resolve.
		_, address := fakeVault.ReadTaskArgsForCall(0)
		Expect(address).To(Equal("a"))
	})

	It("resolves a task_identifier_previous match through the per-task read", func() {
		fakeVault := &mocks.VaultAPIClient{}
		fakeVault.ListTasksReturns([]vaultapi.Task{listed("new-id", "good")}, nil)
		repaired := task("new-id", "good")
		repaired.Frontmatter["task_identifier_previous"] = "target"
		fakeVault.ReadTaskStub = readStub(map[string]vaultapi.Task{
			"good": repaired,
		})

		matchedRelPath, _, err := result.FindTaskFilePath(ctx, fakeVault, "tasks", "target", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(matchedRelPath).To(Equal("tasks/good.md"))
	})

	It("skips a listed task whose read reports not found and matches the next", func() {
		fakeVault := &mocks.VaultAPIClient{}
		fakeVault.ListTasksReturns(
			[]vaultapi.Task{listed("gone", "gone"), listed("kept-id", "kept")}, nil,
		)
		match := task("target", "kept")
		fakeVault.ReadTaskReturnsOnCall(0, nil, vaultapi.ErrNotFound)
		fakeVault.ReadTaskReturnsOnCall(1, &match, nil)

		matchedRelPath, fm, err := result.FindTaskFilePath(ctx, fakeVault, "tasks", "target", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(matchedRelPath).To(Equal("tasks/kept.md"))
		Expect(fm).NotTo(BeNil())
		Expect(fakeVault.ReadTaskCallCount()).To(Equal(2))
	})

	It("returns an error when the listing fails", func() {
		fakeVault := &mocks.VaultAPIClient{}
		fakeVault.ListTasksReturns(nil, errTest)

		_, _, err := result.FindTaskFilePath(ctx, fakeVault, "tasks", "any", nil)
		Expect(err).To(HaveOccurred())
	})
})

// Bounded retry on a not-found task. The controller reads the vault over the typed
// API rather than a local clone, so a result can arrive before the task it belongs
// to is visible; without a retry that result is dropped for good. Retrying
// in-process (rather than returning an error) is deliberate — the consumer runs
// SkipCorruptBatches:false, so erroring on a permanently-missing task would block
// the partition.
var _ = Describe("WriteResult not-found retry", func() {
	var (
		ctx       context.Context
		fakeVault *mocks.VaultAPIClient
		fakeTime  *libtimemocks.CurrentDateTimeGetter
		fakeWait  *libtimemocks.WaiterDuration
		writer    result.ResultWriter
		taskDir   string
		req       lib.Task
	)

	BeforeEach(func() {
		ctx = context.Background()
		taskDir = "tasks"
		fakeVault = &mocks.VaultAPIClient{}
		fakeTime = &libtimemocks.CurrentDateTimeGetter{}
		fakeTime.NowReturns(libtime.DateTime(stdtime.Date(2026, 8, 31, 12, 0, 0, 0, stdtime.UTC)))
		fakeWait = &libtimemocks.WaiterDuration{}
		writer = result.NewResultWriter(
			fakeVault,
			taskDir,
			"openclaw",
			fakeTime,
			metrics.New(),
			fakeWait,
			nil,
			nil,
			nil,
		)
		req = lib.Task{TaskIdentifier: "late-arrival"}
	})

	It("retries and resolves when the task appears on a later attempt", func() {
		// First sweep sees nothing (vault-api has not pulled yet), second sees it.
		late := task("late-arrival", "late")
		fakeVault.ListTasksReturnsOnCall(0, []vaultapi.Task{}, nil)
		fakeVault.ListTasksReturnsOnCall(1, []vaultapi.Task{late}, nil)
		fakeVault.ReadTaskReturns(&late, nil)
		fakeVault.PatchTaskWithBodyReturns(&late, nil)

		err := writer.WriteResult(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeVault.ListTasksCallCount()).To(Equal(2))
		Expect(fakeWait.WaitCallCount()).To(Equal(1))
	})

	It("gives up after the attempt budget without returning an error", func() {
		// A permanently missing task must NOT error — that would block the partition.
		fakeVault.ListTasksReturns([]vaultapi.Task{}, nil)

		err := writer.WriteResult(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeVault.ListTasksCallCount()).To(Equal(3))
		Expect(fakeWait.WaitCallCount()).To(Equal(2))
	})

	It("propagates a cancelled context from the wait instead of spinning", func() {
		fakeVault.ListTasksReturns([]vaultapi.Task{}, nil)
		fakeWait.WaitReturns(context.Canceled)

		err := writer.WriteResult(ctx, req)
		Expect(err).To(HaveOccurred())
		Expect(fakeVault.ListTasksCallCount()).To(Equal(1))
	})
})
