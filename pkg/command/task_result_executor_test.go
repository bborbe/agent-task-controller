// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"errors"

	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	libtimetest "github.com/bborbe/time/test"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent/lib"
	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/command"
)

var _ = Describe("NewTaskResultExecutor", func() {
	var (
		ctx        context.Context
		fakeWriter *mocks.ResultWriter
		executor   cdb.CommandObjectExecutorTx
		schemaID   cdb.SchemaID
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeWriter = &mocks.ResultWriter{}
		executor = command.NewTaskResultExecutor(fakeWriter)
		schemaID = cdb.SchemaID{
			Group:   "agent",
			Kind:    "task",
			Version: "v1",
		}
	})

	Describe("CommandOperation", func() {
		It("returns update", func() {
			Expect(executor.CommandOperation()).To(Equal(base.CommandOperation("update")))
		})
	})

	Describe("SendResultEnabled", func() {
		It("returns true", func() {
			Expect(executor.SendResultEnabled()).To(BeTrue())
		})
	})

	Describe("HandleCommand", func() {
		buildCommandObject := func(event base.Event) cdb.CommandObject {
			return cdb.CommandObject{
				Command: base.Command{
					RequestID: base.NewRequestID(),
					Operation: command.TaskResultCommandOperation,
					Initiator: "test-user",
					Data:      event,
				},
				SchemaID: schemaID,
			}
		}

		Context("valid command", func() {
			It("calls WriteResult once with correct Task", func() {
				now := libtimetest.ParseDateTime("2026-01-15T10:00:00Z")
				task := lib.Task{
					Object: base.Object[base.Identifier]{
						Identifier: base.Identifier("event-uuid-test"),
						Created:    now,
						Modified:   now,
					},
					TaskIdentifier: lib.TaskIdentifier("24 Tasks/test-task.md"),
					Frontmatter: lib.TaskFrontmatter{
						"status": "done",
					},
					Content: lib.TaskContent("## Result\n\nTask completed successfully."),
				}
				event, err := base.ParseEvent(ctx, task)
				Expect(err).To(BeNil())

				cmdObj := buildCommandObject(event)
				fakeWriter.WriteResultReturns(nil)

				eventID, resultEvent, handleErr := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(handleErr).To(BeNil())
				Expect(eventID).NotTo(BeNil())
				Expect(string(*eventID)).To(Equal(string(task.TaskIdentifier)))
				Expect(resultEvent).NotTo(BeNil())
				Expect(fakeWriter.WriteResultCallCount()).To(Equal(1))

				_, gotTask := fakeWriter.WriteResultArgsForCall(0)
				Expect(gotTask.TaskIdentifier).To(Equal(task.TaskIdentifier))
				Expect(gotTask.Content).To(Equal(task.Content))
			})
		})

		Context("malformed JSON payload", func() {
			It("returns ErrCommandObjectSkipped and WriteResult is never called", func() {
				// A map containing a channel cannot be JSON-marshaled, triggering MarshalInto failure.
				event := base.Event{
					"channel": make(chan int),
				}
				cmdObj := buildCommandObject(event)

				eventID, resultEvent, handleErr := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(errors.Is(handleErr, cdb.ErrCommandObjectSkipped)).To(BeTrue())
				Expect(eventID).To(BeNil())
				Expect(resultEvent).To(BeNil())
				Expect(fakeWriter.WriteResultCallCount()).To(Equal(0))
			})
		})

		Context("invalid request — empty task ID", func() {
			It("returns ErrCommandObjectSkipped and WriteResult is never called", func() {
				task := lib.Task{
					TaskIdentifier: lib.TaskIdentifier(""),
					Frontmatter:    lib.TaskFrontmatter{},
					Content:        lib.TaskContent("some content"),
				}
				event, err := base.ParseEvent(ctx, task)
				Expect(err).To(BeNil())

				cmdObj := buildCommandObject(event)

				eventID, resultEvent, handleErr := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(errors.Is(handleErr, cdb.ErrCommandObjectSkipped)).To(BeTrue())
				Expect(eventID).To(BeNil())
				Expect(resultEvent).To(BeNil())
				Expect(fakeWriter.WriteResultCallCount()).To(Equal(0))
			})
		})

		Context("WriteResult returns error", func() {
			It("returns the error wrapped", func() {
				now := libtimetest.ParseDateTime("2026-01-15T10:00:00Z")
				task := lib.Task{
					Object: base.Object[base.Identifier]{
						Identifier: base.Identifier("event-uuid-error"),
						Created:    now,
						Modified:   now,
					},
					TaskIdentifier: lib.TaskIdentifier("24 Tasks/error-task.md"),
					Frontmatter:    lib.TaskFrontmatter{},
					Content:        lib.TaskContent("content"),
				}
				event, err := base.ParseEvent(ctx, task)
				Expect(err).To(BeNil())

				cmdObj := buildCommandObject(event)
				fakeWriter.WriteResultReturns(errors.New("disk full"))

				_, _, handleErr := executor.HandleCommand(ctx, nil, cmdObj)
				Expect(handleErr).NotTo(BeNil())
				Expect(handleErr.Error()).To(ContainSubstring("write result for task"))
			})
		})
	})
})
