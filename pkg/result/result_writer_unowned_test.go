// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package result_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	lib "github.com/bborbe/agent"
	libtime "github.com/bborbe/time"
	libtimemocks "github.com/bborbe/time/mocks"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/result"
)

// A result reaches every controller when its frontmatter carries no target_vault,
// because routing.ShouldProcessResult falls through to true so the owning vault gets
// a chance to find and heal the file. The controllers that do not own it miss by
// construction; counting that as `not_found` reported routine fan-out as data loss
// and made AgentControllerResultNotFound fire on normal fleet traffic.
var _ = Describe("ResultWriter task-file miss outcome", func() {
	var (
		ctx         context.Context
		tmpDir      string
		fakeGit     *mocks.GitClient
		fakeMetrics *mocks.Metrics
		writer      result.ResultWriter
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tmpDir, err = os.MkdirTemp("", "result-writer-unowned-*")
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(filepath.Join(tmpDir, "tasks"), 0750)).To(Succeed())

		fakeGit = &mocks.GitClient{}
		fakeGit.PathReturns(tmpDir)
		// No task file matches, so every lookup attempt misses.
		fakeGit.ListFilesReturns(nil, nil)

		fakeMetrics = &mocks.Metrics{}
		fakeMetrics.ResultsWrittenTotalReturns(prometheus.NewCounter(prometheus.CounterOpts{
			Name: "test_results_written_total",
		}))

		fakeTime := &libtimemocks.CurrentDateTimeGetter{}
		fakeTime.NowReturns(libtime.DateTime(time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)))

		fakeWaiter := &libtimemocks.WaiterDuration{}

		writer = result.NewResultWriter(
			fakeGit,
			"tasks",
			"openclaw",
			fakeTime,
			fakeMetrics,
			fakeWaiter,
		)
	})

	AfterEach(func() {
		Expect(os.RemoveAll(tmpDir)).To(Succeed())
	})

	It("counts a miss on an unstamped result as unowned, not not_found", func() {
		err := writer.WriteResult(ctx, lib.Task{
			TaskIdentifier: lib.TaskIdentifier("unstamped-task-uuid"),
			Frontmatter:    lib.TaskFrontmatter{"status": "in_progress"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeMetrics.ResultsWrittenTotalCallCount()).To(Equal(1))
		Expect(fakeMetrics.ResultsWrittenTotalArgsForCall(0)).To(Equal("unowned"))
	})

	It("counts a miss on a result routed to this vault as not_found", func() {
		err := writer.WriteResult(ctx, lib.Task{
			TaskIdentifier: lib.TaskIdentifier("stamped-task-uuid"),
			Frontmatter: lib.TaskFrontmatter{
				"status":       "in_progress",
				"target_vault": "openclaw",
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeMetrics.ResultsWrittenTotalCallCount()).To(Equal(1))
		Expect(fakeMetrics.ResultsWrittenTotalArgsForCall(0)).To(Equal("not_found"))
	})
})
