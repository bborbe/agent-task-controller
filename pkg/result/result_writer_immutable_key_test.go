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

	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/result"
	"github.com/bborbe/agent-task-controller/pkg/testvault"
)

// A result in flight while its task is re-addressed carries the superseded
// task_identifier. The patch builder used to emit it — every differing key went
// into the body — and vault-api refuses that key on PATCH, so the whole
// read-modify-write failed and the result was dropped. These specs pin the
// exclusion at the builder (what it selects) and at the write (what survives).
var _ = Describe("ResultWriter", func() {
	var (
		ctx      context.Context
		tmpDir   string
		taskDir  string
		fakeGit  *testvault.Client
		fakeTime *libtimemocks.CurrentDateTimeGetter
		writer   result.ResultWriter
	)

	const (
		// The identifiers the dev incident recorded: the re-address replaced
		// 0183525d-… with 0f369aff-… at 20:28:07Z, and the result that arrived
		// afterwards still named the old one.
		supersededID = lib.TaskIdentifier("0183525d-8117-4fb8-9293-31b18d9e176c")
		currentID    = lib.TaskIdentifier("0f369aff-32d6-40b6-84a4-36edc6c677a0")
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tmpDir, err = os.MkdirTemp("", "result-writer-immutable-key-*")
		Expect(err).NotTo(HaveOccurred())

		taskDir = "tasks"
		Expect(os.MkdirAll(filepath.Join(tmpDir, taskDir), 0750)).To(Succeed())

		fakeGit = &testvault.Client{Dir: tmpDir}

		fakeTime = &libtimemocks.CurrentDateTimeGetter{}
		fakeTime.NowReturns(libtime.DateTime(time.Date(2026, 11, 19, 20, 28, 55, 0, time.UTC)))

		writer = result.NewResultWriter(
			fakeGit,
			taskDir,
			"openclaw",
			fakeTime,
			metrics.New(),
			libtime.NewWaiterDuration(),
			nil,
			nil,
			nil,
		)
	})

	AfterEach(func() {
		Expect(os.RemoveAll(tmpDir)).To(Succeed())
	})

	writeTaskFile := func(name, content string) {
		Expect(os.WriteFile(
			filepath.Join(tmpDir, taskDir, name),
			[]byte(content),
			0600,
		)).To(Succeed())
	}

	Describe("WriteResult", func() {
		Context("the task was re-addressed while its result was in flight", func() {
			It("leaves task_identifier out of the patch and still writes the result", func() {
				// The state a re-address leaves behind: the file carries the new
				// identifier and records the old one as the predecessor, which is
				// what lets the in-flight result resolve to this file at all.
				writeTaskFile("re-addressed.md",
					"---\n"+
						"task_identifier: "+string(currentID)+"\n"+
						"task_identifier_previous: "+string(supersededID)+"\n"+
						"status: in_progress\n"+
						"phase: execution\n"+
						"---\n"+
						"old body\n",
				)
				// vault-api refuses any PATCH naming task_identifier and fails the
				// whole write. Without the exclusion this is the dev incident.
				fakeGit.RejectPatchFields = map[string]bool{"task_identifier": true}

				Expect(writer.WriteResult(ctx, lib.Task{
					TaskIdentifier: supersededID,
					Frontmatter: lib.TaskFrontmatter{
						"task_identifier": string(supersededID),
						"status":          "completed",
						"phase":           "done",
					},
					Content: lib.TaskContent("## Result\ndone\n"),
				})).To(Succeed())

				Expect(fakeGit.LastPatchFields()).NotTo(HaveKey("task_identifier"))
				// The mutable keys still ride the same write — the exclusion must not
				// turn into "drop the whole patch".
				Expect(fakeGit.LastPatchFields()).To(HaveKey("status"))
				Expect(fakeGit.LastPatchFields()).To(HaveKey("phase"))

				written, err := os.ReadFile(filepath.Join(tmpDir, taskDir, "re-addressed.md"))
				Expect(err).NotTo(HaveOccurred())
				Expect(
					string(written),
				).To(ContainSubstring("task_identifier: " + string(currentID)))
				Expect(string(written)).To(ContainSubstring("status: completed"))
				Expect(string(written)).To(ContainSubstring("## Result"))
			})
		})
	})

	Describe("changedFieldsForWrite", func() {
		DescribeTable(
			"never selects a PATCH-immutable key",
			func(current, next map[string]any, absent, present, wantExcluded []string) {
				fields, excluded := result.ChangedFieldsForWriteForTest(current, next)
				for _, key := range absent {
					Expect(fields).NotTo(HaveKey(key))
				}
				for _, key := range present {
					Expect(fields).To(HaveKey(key))
				}
				// The drop must be reportable: SC3's log line is built from this set,
				// and an empty set on a write that dropped nothing is what keeps the
				// line absent in the negative case.
				Expect(excluded).To(ConsistOf(wantExcluded))
			},
			Entry(
				"drops a differing task_identifier and keeps the differing mutable keys",
				map[string]any{"task_identifier": string(currentID), "status": "in_progress"},
				map[string]any{"task_identifier": string(supersededID), "status": "completed"},
				[]string{"task_identifier", "approved_by", "approved_at"},
				[]string{"status"},
				[]string{"task_identifier"},
			),
			Entry(
				"drops the approval-record keys PATCH refuses by rule",
				map[string]any{"status": "in_progress"},
				map[string]any{"status": "completed", "approved_by": "operator"},
				[]string{"approved_by", "approved_at", "task_identifier"},
				[]string{"status"},
				[]string{"approved_by"},
			),
			Entry(
				"emits nothing when only immutable keys differ",
				map[string]any{"task_identifier": string(currentID), "status": "in_progress"},
				map[string]any{"task_identifier": string(supersededID), "status": "in_progress"},
				[]string{"task_identifier"},
				nil,
				[]string{"task_identifier"},
			),
			Entry(
				"reports no exclusion when no immutable key differs",
				map[string]any{"task_identifier": string(currentID), "status": "in_progress"},
				map[string]any{"task_identifier": string(currentID), "status": "completed"},
				[]string{"task_identifier"},
				[]string{"status"},
				nil,
			),
		)
	})
})
