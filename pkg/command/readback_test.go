// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"errors"
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
			"origin/main",
		)
	})

	// stubRead answers both views with the same content.
	//
	// The write executors read the working tree before writing (ReadTask) and the
	// verifier reads the committed ref afterwards (ReadTaskAt). A spec is about
	// the verifier's comparison, so both halves must agree unless the spec is
	// specifically about them disagreeing.
	stubRead := func(task *vaultapi.Task, err error) {
		fakeVault.ReadTaskReturns(task, err)
		fakeVault.ReadTaskAtReturns(task, err)
	}

	hashOf := func(fm map[string]any, body string) string {
		h, err := command.RecordHashForTest(fm, body)
		Expect(err).NotTo(HaveOccurred())
		return h
	}
	// accepts the write, and the record never moves. The store here acknowledges
	// every write and returns the same content forever — which is exactly what a
	// dropped write looks like from the caller's side.
	Context("when the record does not move", func() {
		It("counts a mismatch", func() {
			stubRead(&vaultapi.Task{
				ID:          "task-a",
				Title:       "task-a",
				Frontmatter: map[string]any{"phase": "planning", "status": "in_progress"},
			}, nil)

			verifier.Expect(ctx, command.ReadbackExpectation{
				TaskIdentifier: "task-a",
				Operation:      "update-frontmatter",
				BeforeHash: hashOf(
					map[string]any{"phase": "planning", "status": "in_progress"},
					"",
				),
			})
			command.VerifyDueForTest(verifier, ctx)

			Expect(testutil.ToFloat64(counter)).To(BeNumerically("==", 1))
			Expect(fakeMet.ReadbackMismatchTotalArgsForCall(0)).To(Equal("update-frontmatter"))
		})
	})

	// The defect this verifier exists for: the working tree already shows the
	// write while no commit carries it yet. A readback that trusts the working
	// tree reports success here; the committed read is the only one that can see
	// the gap, which is why the verifier asks for the ref.
	Context("when only the working tree carries the write", func() {
		It("counts a mismatch", func() {
			fakeVault.ReadTaskReturns(&vaultapi.Task{
				ID:          "task-c",
				Title:       "task-c",
				Frontmatter: map[string]any{"phase": "done"},
			}, nil)
			fakeVault.ReadTaskAtReturns(&vaultapi.Task{
				ID:          "task-c",
				Title:       "task-c",
				Frontmatter: map[string]any{"phase": "planning"},
			}, nil)

			verifier.Expect(ctx, command.ReadbackExpectation{
				TaskIdentifier: "task-c",
				Operation:      "update-frontmatter",
				BeforeHash:     hashOf(map[string]any{"phase": "planning"}, ""),
			})
			command.VerifyDueForTest(verifier, ctx)

			Expect(testutil.ToFloat64(counter)).To(BeNumerically("==", 1))
			Expect(fakeVault.ReadTaskAtCallCount()).To(Equal(1))
		})
	})

	Context("when the record moved", func() {
		It("counts nothing", func() {
			stubRead(&vaultapi.Task{
				ID:          "task-b",
				Title:       "task-b",
				Frontmatter: map[string]any{"phase": "done", "status": "completed"},
			}, nil)

			verifier.Expect(ctx, command.ReadbackExpectation{
				TaskIdentifier: "task-b",
				Operation:      "update-frontmatter",
				BeforeHash: hashOf(
					map[string]any{"phase": "planning", "status": "in_progress"},
					"",
				),
			})
			command.VerifyDueForTest(verifier, ctx)

			Expect(testutil.ToFloat64(counter)).To(BeNumerically("==", 0))
		})
	})

	// A create has no before-state: the question is whether the record became
	// readable at all, not whether it changed.
	Context("when a created record is still unreadable", func() {
		It("counts a mismatch", func() {
			stubRead(nil, vaultapi.ErrNotFound)

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
			stubRead(&vaultapi.Task{
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
			stubRead(&vaultapi.Task{
				ID:          "task-e",
				Title:       "task-e",
				Frontmatter: map[string]any{"phase": "planning"},
			}, nil)

			verifier.Expect(ctx, command.ReadbackExpectation{
				TaskIdentifier: "task-e",
				Operation:      "update-frontmatter",
				BeforeHash:     hashOf(map[string]any{"phase": "planning"}, ""),
				Deadline:       time.Now().Add(time.Hour),
			})
			command.VerifyDueForTest(verifier, ctx)

			Expect(testutil.ToFloat64(counter)).To(BeNumerically("==", 0))
			Expect(fakeVault.ReadTaskAtCallCount()).To(Equal(0))
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
			stubRead(unchanged, nil)
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

	// The healthy path the metric must stay silent on. `update-frontmatter` is
	// idempotent: re-publishing values the record already carries sends no PATCH,
	// so the record is SUPPOSED not to move. Recording an expectation for such a
	// command would count a no-op as a dropped write.
	Context("wired through the update-frontmatter executor with nothing to write", func() {
		It("records no expectation and counts nothing", func() {
			unchanged := &vaultapi.Task{
				ID:    "task-g",
				Title: "task-g",
				Frontmatter: map[string]any{
					"phase":        "planning",
					"status":       "in_progress",
					"target_vault": "openclaw",
				},
			}
			stubRead(unchanged, nil)

			executor := command.NewUpdateFrontmatterExecutor(
				fakeVault,
				"openclaw",
				libtime.NewCurrentDateTime(),
				metrics.New(),
				nil,
				verifier,
			)
			cmd := buildReadbackUpdateCmd(ctx, task.UpdateFrontmatterCommand{
				TaskIdentifier: lib.TaskIdentifier("task-g"),
				TargetVault:    "openclaw",
				Updates:        lib.TaskFrontmatter{"phase": "planning"},
			})
			_, _, err := executor.HandleCommand(ctx, nil, cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(fakeVault.PatchTaskCallCount()).To(Equal(0))

			command.VerifyDueForTest(verifier, ctx)

			Expect(testutil.ToFloat64(counter)).To(BeNumerically("==", 0))
			Expect(fakeMet.ReadbackMismatchTotalCallCount()).To(Equal(0))
		})
	})

	// A body-only write moves no frontmatter key, so a frontmatter-only
	// fingerprint would read it as "the record did not move". The body is half
	// the record and the comparison has to see it.
	Context("when only the body moved", func() {
		It("counts nothing", func() {
			stubRead(&vaultapi.Task{
				ID:          "task-h",
				Title:       "task-h",
				Frontmatter: map[string]any{"phase": "planning"},
				Body:        "## Failure\n\nnew section\n",
			}, nil)

			verifier.Expect(ctx, command.ReadbackExpectation{
				TaskIdentifier: "task-h",
				Operation:      "update-frontmatter",
				BeforeHash: hashOf(
					map[string]any{"phase": "planning"},
					"reply 'ok'\n",
				),
			})
			command.VerifyDueForTest(verifier, ctx)

			Expect(testutil.ToFloat64(counter)).To(BeNumerically("==", 0))
		})
	})

	// The create path passes no before-hash, so its expectation means "this
	// record must become readable". Wired through the real executor, a create
	// whose record never appears is a mismatch.
	Context("wired through the create-task executor", func() {
		It("counts a mismatch when the created record never appears", func() {
			stubRead(nil, vaultapi.ErrNotFound)
			fakeVault.CreateTaskReturns(&vaultapi.Task{
				ID:    "task-i",
				Title: "task-i",
			}, nil)

			executor := command.NewCreateTaskExecutor(
				fakeVault,
				"tasks",
				"openclaw",
				libtime.NewCurrentDateTime(),
				0,
				nil,
				verifier,
			)
			cmd := buildReadbackCreateCmd(ctx, task.CreateCommand{
				TaskIdentifier: lib.TaskIdentifier("task-i"),
				Title:          "task-i",
				TargetVault:    "openclaw",
				Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "next"},
			})
			_, _, err := executor.HandleCommand(ctx, nil, cmd)
			Expect(err).NotTo(HaveOccurred())

			command.VerifyDueForTest(verifier, ctx)

			Expect(testutil.ToFloat64(counter)).To(BeNumerically("==", 1))
			Expect(fakeMet.ReadbackMismatchTotalArgsForCall(0)).To(Equal("create-task"))
		})
	})

	// Shutdown is not a failure: every component in this repo runs through
	// service.Run, which treats a component error as fatal, so returning
	// ctx.Err() here would make an ordinary stop look like a crash.
	Context("when the context is cancelled", func() {
		It("returns nil instead of the context error", func() {
			cancelled, cancel := context.WithCancel(ctx)
			cancel()

			Expect(verifier.Run(cancelled)).NotTo(HaveOccurred())
		})
	})

	// An expectation that cannot name its record or its operation cannot be
	// verified. Dropping it is the honest outcome; counting it would invent a
	// mismatch from a caller bug.
	Context("when an expectation cannot be verified", func() {
		It("drops an expectation with no task identifier", func() {
			verifier.Expect(ctx, command.ReadbackExpectation{
				Operation: "update-frontmatter",
			})
			command.VerifyDueForTest(verifier, ctx)

			Expect(fakeVault.ReadTaskAtCallCount()).To(Equal(0))
			Expect(testutil.ToFloat64(counter)).To(BeNumerically("==", 0))
		})

		It("drops an expectation with no operation", func() {
			verifier.Expect(ctx, command.ReadbackExpectation{
				TaskIdentifier: "task-k",
			})
			command.VerifyDueForTest(verifier, ctx)

			Expect(fakeVault.ReadTaskAtCallCount()).To(Equal(0))
			Expect(testutil.ToFloat64(counter)).To(BeNumerically("==", 0))
		})
	})

	// A read that fails for a reason other than not-found says nothing about
	// whether the write landed, so it is logged and dropped. Counting it would
	// fire the detector on every write while vault-api is briefly unavailable.
	Context("when the readback read fails", func() {
		It("counts nothing", func() {
			stubRead(nil, errors.New("vault-api unreachable"))

			verifier.Expect(ctx, command.ReadbackExpectation{
				TaskIdentifier: "task-l",
				Operation:      "update-frontmatter",
				BeforeHash:     hashOf(map[string]any{"phase": "planning"}, ""),
			})
			command.VerifyDueForTest(verifier, ctx)

			Expect(testutil.ToFloat64(counter)).To(BeNumerically("==", 0))
			Expect(fakeMet.ReadbackMismatchTotalCallCount()).To(Equal(0))
		})
	})

	// The default ref must name the vault's DEFAULT branch, not a literal one.
	// Naming `origin/main` was wrong for every `master` vault: the ref does not
	// resolve, vault-api answers 500 read_failed, and a landed write logged
	// "cannot verify". origin/HEAD resolves to origin/main or origin/master
	// depending on the vault, so one default is correct for the whole fleet.
	Context("when no ref is configured", func() {
		It("reads the vault's default branch", func() {
			defaultRefVerifier := command.NewWriteVerifier(
				fakeVault,
				fakeMet,
				libtime.NewCurrentDateTime(),
				time.Nanosecond,
				time.Nanosecond,
				"",
			)
			stubRead(&vaultapi.Task{
				ID:          "task-m",
				Title:       "task-m",
				Frontmatter: map[string]any{"phase": "done"},
			}, nil)

			defaultRefVerifier.Expect(ctx, command.ReadbackExpectation{
				TaskIdentifier: "task-m",
				Operation:      "update-frontmatter",
				BeforeHash:     hashOf(map[string]any{"phase": "planning"}, ""),
			})
			command.VerifyDueForTest(defaultRefVerifier, ctx)

			Expect(fakeVault.ReadTaskAtCallCount()).To(Equal(1))
			_, _, ref := fakeVault.ReadTaskAtArgsForCall(0)
			Expect(ref).To(Equal("origin/HEAD"))
			Expect(testutil.ToFloat64(counter)).To(BeNumerically("==", 0))
		})
	})
})

// buildReadbackCreateCmd builds the create command object the executor consumes.
func buildReadbackCreateCmd(ctx context.Context, cmd task.CreateCommand) cdb.CommandObject {
	event, err := base.ParseEvent(ctx, cmd)
	Expect(err).NotTo(HaveOccurred())
	return cdb.CommandObject{
		Command: base.Command{
			RequestID: base.NewRequestID(),
			Operation: task.CreateCommandOperation,
			Initiator: "test",
			Data:      event,
		},
		SchemaID: cdb.SchemaID{Group: "agent", Kind: "task", Version: "v1"},
	}
}

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
