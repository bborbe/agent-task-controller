// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sync_test

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent/lib"
	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/scanner"
	pkgsync "github.com/bborbe/agent-task-controller/pkg/sync"
)

var _ = Describe("SyncLoop", func() {
	var (
		ctx           context.Context
		cancel        context.CancelFunc
		fakeScanner   *mocks.VaultScanner
		fakePublisher *mocks.TaskPublisher
		resultsCh     chan scanner.ScanResult
		syncLoop      pkgsync.SyncLoop
		runErr        chan error
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		fakeScanner = &mocks.VaultScanner{}
		fakePublisher = &mocks.TaskPublisher{}
		resultsCh = make(chan scanner.ScanResult, 10)
		ch := resultsCh // capture by value to avoid race with next BeforeEach

		fakeScanner.RunStub = func(ctx context.Context, results chan<- scanner.ScanResult) error {
			for {
				select {
				case <-ctx.Done():
					return nil
				case r := <-ch:
					results <- r
				}
			}
		}

		syncLoop = pkgsync.NewSyncLoop(
			fakeScanner,
			fakePublisher,
			make(chan struct{}, 1),
			metrics.New(),
		)
		runErr = make(chan error, 1)
		sl := syncLoop
		errCh := runErr
		go func() {
			errCh <- sl.Run(ctx)
		}()
	})

	AfterEach(func() {
		cancel()
	})

	It("calls PublishChanged for a changed task in scan result", func() {
		task := lib.Task{
			TaskIdentifier: lib.TaskIdentifier("test-uuid"),
			Frontmatter:    lib.TaskFrontmatter{"status": "next"},
			Content:        lib.TaskContent("# Test"),
		}
		fakePublisher.PublishChangedReturns(nil)
		resultsCh <- scanner.ScanResult{Changed: []lib.Task{task}}

		Eventually(fakePublisher.PublishChangedCallCount, time.Second).Should(Equal(1))
		_, publishedTask := fakePublisher.PublishChangedArgsForCall(0)
		Expect(publishedTask.TaskIdentifier).To(Equal(task.TaskIdentifier))
	})

	It("calls PublishDeleted for a deleted identifier in scan result", func() {
		id := lib.TaskIdentifier("24 Tasks/deleted.md")
		fakePublisher.PublishDeletedReturns(nil)
		resultsCh <- scanner.ScanResult{Deleted: []lib.TaskIdentifier{id}}

		Eventually(fakePublisher.PublishDeletedCallCount, time.Second).Should(Equal(1))
		_, publishedID := fakePublisher.PublishDeletedArgsForCall(0)
		Expect(publishedID).To(Equal(id))
	})

	It("returns error when PublishChanged fails", func() {
		task := lib.Task{
			TaskIdentifier: lib.TaskIdentifier("test-uuid"),
		}
		fakePublisher.PublishChangedReturns(errors.New("publish failed"))
		resultsCh <- scanner.ScanResult{Changed: []lib.Task{task}}

		Eventually(
			runErr,
			time.Second,
		).Should(Receive(MatchError(ContainSubstring("publish failed"))))
	})

	It("returns error when PublishDeleted fails", func() {
		id := lib.TaskIdentifier("24 Tasks/deleted.md")
		fakePublisher.PublishDeletedReturns(errors.New("publish failed"))
		resultsCh <- scanner.ScanResult{Deleted: []lib.TaskIdentifier{id}}

		Eventually(
			runErr,
			time.Second,
		).Should(Receive(MatchError(ContainSubstring("publish failed"))))
	})

	It("returns nil when context is cancelled", func() {
		cancel()
		Eventually(runErr, time.Second).Should(Receive(BeNil()))
	})

	Describe("Trigger", func() {
		It("does not block when called", func() {
			syncLoop.Trigger()
			syncLoop.Trigger() // second call should also not block (buffered channel, non-blocking send)
		})
	})
})
