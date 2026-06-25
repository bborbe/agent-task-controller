// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package publisher_test

import (
	"context"
	"errors"

	lib "github.com/bborbe/agent"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	cqrsmocks "github.com/bborbe/cqrs/mocks"
	libtime "github.com/bborbe/time"
	libtimetest "github.com/bborbe/time/test"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/publisher"
)

var _ = Describe("TaskPublisher", func() {
	var (
		ctx             context.Context
		fakeSender      *cqrsmocks.CDBEventObjectSender
		schemaID        cdb.SchemaID
		tp              publisher.TaskPublisher
		currentDateTime libtime.CurrentDateTime
		fixedTime       libtime.DateTime
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeSender = &cqrsmocks.CDBEventObjectSender{}
		schemaID = cdb.SchemaID{
			Group:   "agent",
			Kind:    "task",
			Version: "v1",
		}
		currentDateTime = libtime.NewCurrentDateTime()
		fixedTime = libtimetest.ParseDateTime("2026-01-15T10:00:00Z")
		currentDateTime.SetNow(fixedTime)
		tp = publisher.NewTaskPublisher(fakeSender, schemaID, currentDateTime)
	})

	Describe("PublishChanged", func() {
		It("calls SendUpdate with correct EventObject", func() {
			fakeSender.SendUpdateReturns(nil)
			task := lib.Task{
				TaskIdentifier: lib.TaskIdentifier("test-uuid-1234"),
				Frontmatter: lib.TaskFrontmatter{
					"status":   "next",
					"assignee": "user@example.com",
				},
				Content: lib.TaskContent("# Test"),
			}

			err := tp.PublishChanged(ctx, task)
			Expect(err).To(BeNil())
			Expect(fakeSender.SendUpdateCallCount()).To(Equal(1))

			_, eventObject := fakeSender.SendUpdateArgsForCall(0)
			Expect(eventObject.SchemaID).To(Equal(schemaID))
			Expect(eventObject.ID).To(Equal(base.EventID("test-uuid-1234")))
			Expect(eventObject.Event).NotTo(BeNil())

			var publishedTask lib.Task
			Expect(eventObject.Event.MarshalInto(ctx, &publishedTask)).To(Succeed())
			Expect(publishedTask.Object.Created).To(Equal(fixedTime))
			Expect(publishedTask.Object.Modified).To(Equal(fixedTime))
		})

		It("returns an error when SendUpdate fails", func() {
			fakeSender.SendUpdateReturns(errors.New("kafka down"))
			task := lib.Task{
				TaskIdentifier: lib.TaskIdentifier("test-uuid-1234"),
			}

			err := tp.PublishChanged(ctx, task)
			Expect(err).NotTo(BeNil())
		})
	})

	Describe("PublishDeleted", func() {
		It("calls SendDelete with correct EventObject", func() {
			fakeSender.SendDeleteReturns(nil)
			id := lib.TaskIdentifier("24 Tasks/deleted.md")

			err := tp.PublishDeleted(ctx, id)
			Expect(err).To(BeNil())
			Expect(fakeSender.SendDeleteCallCount()).To(Equal(1))

			_, eventObject := fakeSender.SendDeleteArgsForCall(0)
			Expect(eventObject.SchemaID).To(Equal(schemaID))
			Expect(eventObject.ID).To(Equal(base.EventID("24 Tasks/deleted.md")))
		})

		It("returns an error when SendDelete fails", func() {
			fakeSender.SendDeleteReturns(errors.New("kafka down"))
			id := lib.TaskIdentifier("24 Tasks/deleted.md")

			err := tp.PublishDeleted(ctx, id)
			Expect(err).NotTo(BeNil())
		})
	})
})
