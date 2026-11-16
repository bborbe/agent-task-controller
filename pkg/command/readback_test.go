// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"time"

	lib "github.com/bborbe/agent"
	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/command"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

var _ = Describe("WriteVerifier", func() {
	var (
		ctx       context.Context
		fakeVault *mocks.VaultAPIClient
		fakeMet   *mocks.Metrics
		counter   prometheus.Counter
		verifier  command.WriteVerifier
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeVault = &mocks.VaultAPIClient{}
		counter = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "readback_mismatch_total_test",
			Help: "test counter",
		})
		fakeMet = &mocks.Metrics{}
		fakeMet.ReadbackMismatchTotalReturns(counter)
		// A nanosecond deadline makes the expectation due on the first pass, so
		// the spec asserts the comparison itself rather than the ticker.
		verifier = command.NewWriteVerifier(
			fakeVault,
			fakeMet,
			libtime.NewCurrentDateTime(),
			time.Nanosecond,
			time.Nanosecond,
		)
	})

	hashOf := func(fm map[string]any) string {
		h, err := command.FrontmatterHashForTest(fm)
		Expect(err).NotTo(HaveOccurred())
		return h
	}
	// accepts the write, and the record never moves. The store here acknowledges
	// every write and returns the same content forever — which is exactly what a
	// dropped write looks like from the caller's side.
	Context("when the record does not move", func() {
		It("counts a mismatch", func() {
			fakeVault.ReadTaskReturns(&vaultapi.Task{
				ID:          "task-a",
				Title:       "task-a",
				Frontmatter: map[string]any{"phase": "planning", "status": "in_progress"},
			}, nil)

			verifier.Expect(ctx, command.ReadbackExpectation{
				TaskIdentifier: "task-a",
				Operation:      "update-frontmatter",
				BeforeHash:     hashOf(map[string]any{"phase": "planning", "status": "in_progress"}),
			})
			command.VerifyDueForTest(verifier, ctx)

			Expect(testutil.ToFloat64(counter)).To(BeNumerically("==", 1))
			Expect(fakeMet.ReadbackMismatchTotalArgsForCall(0)).To(Equal("update-frontmatter"))
		})
	})

	Context("when the record moved", func() {
		It("counts nothing", func() {
			fakeVault.ReadTaskReturns(&vaultapi.Task{
				ID:          "task-b",
				Title:       "task-b",
				Frontmatter: map[string]any{"phase": "done", "status": "completed"},
			}, nil)

			verifier.Expect(ctx, command.ReadbackExpectation{
				TaskIdentifier: "task-b",
				Operation:      "update-frontmatter",
				BeforeHash:     hashOf(map[string]any{"phase": "planning", "status": "in_progress"}),
			})
			command.VerifyDueForTest(verifier, ctx)

			Expect(testutil.ToFloat64(counter)).To(BeNumerically("==", 0))
		})
	})

	// A create has no before-state: the question is whether the record became
	// readable at all, not whether it changed.
	Context("when a created record is still unreadable", func() {
		It("counts a mismatch", func() {
			fakeVault.ReadTaskReturns(nil, vaultapi.ErrNotFound)

			verifier.Expect(ctx, command.ReadbackExpectation{
				TaskIdentifier: "task-c",
				Operation:      "create-task",
			})
			command.VerifyDueForTest(verifier, ctx)

			Expect(testutil.ToFloat64(counter)).To(BeNumerically("==", 1))
			Expect(fakeMet.ReadbackMismatchTotalArgsForCall(0)).To(Equal("create-task"))
		})
	})

	Context("when a created record is readable", func() {
		It("counts nothing", func() {
			fakeVault.ReadTaskReturns(&vaultapi.Task{
				ID:          "task-d",
				Title:       "task-d",
				Frontmatter: map[string]any{"status": "in_progress"},
			}, nil)

			verifier.Expect(ctx, command.ReadbackExpectation{
				TaskIdentifier: "task-d",
				Operation:      "create-task",
			})
			command.VerifyDueForTest(verifier, ctx)

			Expect(testutil.ToFloat64(counter)).To(BeNumerically("==", 0))
		})
	})

	// The whole point of deferring: the ack precedes the commit by up to one
	// git-rest window, so an expectation that is not yet due must not be
	// evaluated. This is what stops the detector firing on every ordinary write.
	Context("when the deadline has not passed", func() {
		It("leaves the expectation pending", func() {
			fakeVault.ReadTaskReturns(&vaultapi.Task{
				ID:          "task-e",
				Title:       "task-e",
				Frontmatter: map[string]any{"phase": "planning"},
			}, nil)

			verifier.Expect(ctx, command.ReadbackExpectation{
				TaskIdentifier: "task-e",
				Operation:      "update-frontmatter",
				BeforeHash:     hashOf(map[string]any{"phase": "planning"}),
				Deadline:       time.Now().Add(time.Hour),
			})
			command.VerifyDueForTest(verifier, ctx)

			Expect(testutil.ToFloat64(counter)).To(BeNumerically("==", 0))
			Expect(fakeVault.ReadTaskCallCount()).To(Equal(0))
		})
	})

	// End to end through the real executor: the store acknowledges the patch and
	// keeps returning the pre-write frontmatter. The executor reports success and
	// the readback is what exposes that the record never moved.
	Context("wired through the update-frontmatter executor", func() {
		It("counts a mismatch after a reported success", func() {
			unchanged := &vaultapi.Task{
				ID:          "task-f",
				Title:       "task-f",
				Frontmatter: map[string]any{"phase": "planning", "status": "in_progress"},
			}
			fakeVault.ReadTaskReturns(unchanged, nil)
			fakeVault.PatchTaskReturns(unchanged, nil)

			executor := command.NewUpdateFrontmatterExecutor(
				fakeVault,
				"openclaw",
				libtime.NewCurrentDateTime(),
				metrics.New(),
				nil,
				verifier,
			)
			cmd := buildReadbackUpdateCmd(ctx, task.UpdateFrontmatterCommand{
				TaskIdentifier: lib.TaskIdentifier("task-f"),
				TargetVault:    "openclaw",
				Updates:        lib.TaskFrontmatter{"phase": "done"},
			})
			_, _, err := executor.HandleCommand(ctx, nil, cmd)
			Expect(err).NotTo(HaveOccurred())

			command.VerifyDueForTest(verifier, ctx)

			Expect(testutil.ToFloat64(counter)).To(BeNumerically("==", 1))
		})
	})
})

// buildReadbackUpdateCmd builds the command object the executor consumes. It is
// local to this file because the sibling specs each define their own closure and
// a shared helper would have to change their scope.
func buildReadbackUpdateCmd(
	ctx context.Context,
	cmd task.UpdateFrontmatterCommand,
) cdb.CommandObject {
	event, err := base.ParseEvent(ctx, cmd)
	Expect(err).NotTo(HaveOccurred())
	return cdb.CommandObject{
		Command: base.Command{
			RequestID: base.NewRequestID(),
			Operation: command.UpdateFrontmatterCommandOperation,
			Initiator: "test",
			Data:      event,
		},
		SchemaID: cdb.SchemaID{Group: "agent", Kind: "task", Version: "v1"},
	}
}
