// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"time"

	"github.com/bborbe/boltkv"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/command"
	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

var _ = Describe("parked command re-drive", func() {
	const (
		expireDuration = 60 * time.Minute
		redriveDelay   = 30 * time.Second
		interval       = 30 * time.Second
	)

	var ctx context.Context
	var db libkv.DB
	var park command.CommandPark
	var now time.Time
	var currentDateTime libtime.CurrentDateTimeGetter
	var schemaID cdb.SchemaID
	var sent []cdb.ResultObject
	var sender cdb.ResultObjectSender
	var expiredCounter prometheus.Counter
	var fakeMetrics *mocks.Metrics

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = boltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
		park = command.NewCommandPark(db)
		now = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
		currentDateTime = libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
			return libtime.DateTime(now)
		})
		schemaID = cdb.SchemaID{Group: "agent", Kind: "task", Version: "v1"}
		sent = make([]cdb.ResultObject, 0)
		sender = cdb.ResultObjectSenderFunc(
			func(_ context.Context, resultObject cdb.ResultObject) error {
				sent = append(sent, resultObject)
				return nil
			},
		)
		expiredCounter = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "test_parked_expired_total",
			Help: "test counter",
		})
		fakeMetrics = &mocks.Metrics{}
		fakeMetrics.CommandExpiredTotalReturns(expiredCounter)
	})

	AfterEach(func() {
		Expect(db.Close()).To(Succeed())
	})

	parkCommand := func(parkedAt time.Time) {
		Expect(db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
			return park.Park(
				ctx,
				tx,
				base.Command{
					RequestID:   "req-1",
					RequestTime: parkedAt,
					Operation:   "create-task",
					Initiator:   "me",
					Data:        base.Event{"taskIdentifier": "abc12345"},
				},
				parkedAt,
				"abc12345",
			)
		})).To(Succeed())
	}

	newRedriver := func(executor cdb.CommandObjectExecutorTx) command.ParkRedriver {
		return command.NewParkRedriver(
			db,
			park,
			executor,
			schemaID,
			sender,
			expireDuration,
			redriveDelay,
			interval,
			currentDateTime,
			fakeMetrics,
		)
	}

	executorReturning := func(err error) cdb.CommandObjectExecutorTx {
		return cdb.CommandObjectExecutorTxFunc(
			"create-task",
			true,
			func(
				_ context.Context,
				_ libkv.Tx,
				_ cdb.CommandObject,
			) (*base.EventID, base.Event, error) {
				return nil, nil, err
			},
		)
	}

	It("re-executes a parked command, publishes its success and clears the park", func() {
		parkCommand(now.Add(-time.Minute))
		redriver := newRedriver(executorReturning(nil))

		Expect(redriver.RedriveOnce(ctx)).To(Succeed())

		Expect(sent).To(HaveLen(1))
		Expect(sent[0].Result.Success).To(BeTrue())
		parked, err := park.List(ctx)
		Expect(err).To(BeNil())
		Expect(parked).To(BeEmpty())
	})

	It("keeps the command parked while the vault still fails transiently", func() {
		parkCommand(now.Add(-time.Minute))
		redriver := newRedriver(executorReturning(
			errors.Wrapf(ctx, vaultapi.ErrTransient, "create task: unexpected status 503"),
		))

		Expect(redriver.RedriveOnce(ctx)).To(Succeed())

		// No result: the command is not decided yet, and a Failure here would tell
		// every result consumer otherwise.
		Expect(sent).To(BeEmpty())
		parked, err := park.List(ctx)
		Expect(err).To(BeNil())
		Expect(parked).To(HaveLen(1))
	})

	It("publishes the failure and clears the park for a permanent error", func() {
		parkCommand(now.Add(-time.Minute))
		redriver := newRedriver(
			executorReturning(errors.Errorf(ctx, "validate task_identifier failed")),
		)

		Expect(redriver.RedriveOnce(ctx)).To(Succeed())

		Expect(sent).To(HaveLen(1))
		Expect(sent[0].Result.Success).To(BeFalse())
		parked, err := park.List(ctx)
		Expect(err).To(BeNil())
		Expect(parked).To(BeEmpty())
	})

	It("leaves a freshly parked command alone until the re-drive delay has passed", func() {
		parkCommand(now.Add(-time.Second))
		executed := 0
		redriver := newRedriver(
			cdb.CommandObjectExecutorTxFunc(
				"create-task",
				true,
				func(
					_ context.Context,
					_ libkv.Tx,
					_ cdb.CommandObject,
				) (*base.EventID, base.Event, error) {
					executed++
					return nil, nil, nil
				},
			),
		)

		Expect(redriver.RedriveOnce(ctx)).To(Succeed())
		Expect(executed).To(Equal(0))
	})

	It("drops a command whose re-drive window has closed, visibly", func() {
		parkCommand(now.Add(-2 * expireDuration))
		redriver := newRedriver(executorReturning(nil))

		Expect(redriver.RedriveOnce(ctx)).To(Succeed())

		Expect(sent).To(BeEmpty())
		parked, err := park.List(ctx)
		Expect(err).To(BeNil())
		Expect(parked).To(BeEmpty())
		Expect(fakeMetrics.CommandExpiredTotalCallCount()).To(Equal(1))
		Expect(fakeMetrics.CommandExpiredTotalArgsForCall(0)).To(Equal("create-task"))
		Expect(testutil.ToFloat64(expiredCounter)).To(Equal(1.0))
	})
})
