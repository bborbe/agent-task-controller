// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"time"

	"github.com/bborbe/boltkv"
	"github.com/bborbe/cqrs/base"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/command"
)

var _ = Describe("command park", func() {
	var ctx context.Context
	var db libkv.DB
	var park command.CommandPark
	var now time.Time

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = boltkv.OpenTemp(ctx)
		Expect(err).To(Succeed())
		park = command.NewCommandPark(db)
		now = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	})

	AfterEach(func() {
		Expect(db.Close()).To(Succeed())
	})

	commandFor := func(requestID string, identifier string) base.Command {
		return base.Command{
			RequestID:   base.RequestID(requestID),
			RequestTime: now,
			Operation:   "create-task",
			Initiator:   "me",
			Data:        base.Event{"taskIdentifier": identifier},
		}
	}

	It("round-trips a parked command", func() {
		Expect(db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
			return park.Park(
				ctx,
				tx,
				commandFor("req-1", "abc12345"),
				libtime.DateTime(now),
				"abc12345",
			)
		})).To(Succeed())

		parked, err := park.List(ctx)
		Expect(err).To(Succeed())
		Expect(parked).To(HaveLen(1))
		Expect(parked[0].Key).To(Equal("req-1"))
		Expect(parked[0].TaskIdentifier).To(Equal("abc12345"))
		Expect(parked[0].ParkedAt.Time()).To(BeTemporally("==", now))
		Expect(string(parked[0].Command.Operation)).To(Equal("create-task"))
		Expect(parked[0].Command.Data["taskIdentifier"]).To(Equal("abc12345"))

		Expect(park.Delete(ctx, "req-1")).To(Succeed())
		parked, err = park.List(ctx)
		Expect(err).To(Succeed())
		Expect(parked).To(BeEmpty())
	})

	It("keeps one entry per requestID, so a re-published command cannot multiply", func() {
		later := now.Add(time.Minute)
		Expect(db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
			if err := park.Park(
				ctx,
				tx,
				commandFor("req-1", "abc12345"),
				libtime.DateTime(now),
				"abc12345",
			); err != nil {
				return err
			}
			return park.Park(
				ctx,
				tx,
				commandFor("req-1", "abc12345"),
				libtime.DateTime(later),
				"abc12345",
			)
		})).To(Succeed())

		parked, err := park.List(ctx)
		Expect(err).To(Succeed())
		Expect(parked).To(HaveLen(1))
		// A re-park refreshes the stamp; the entry does not multiply.
		Expect(parked[0].ParkedAt.Time()).To(BeTemporally("==", later))
	})

	It("reports an empty list before anything was parked", func() {
		parked, err := park.List(ctx)
		Expect(err).To(Succeed())
		Expect(parked).To(BeEmpty())
	})

	It("deletes a key that is not parked without failing", func() {
		Expect(park.Delete(ctx, "never-parked")).To(Succeed())
	})
})
