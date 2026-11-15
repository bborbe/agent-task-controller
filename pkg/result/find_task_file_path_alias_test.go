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
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/testvault"
	"github.com/bborbe/agent-task-controller/pkg/result"
)

const aliasUUID = "33333333-3333-4333-8333-333333333333"

// newAliasResultGit returns a fake Git client backed by tmpDir, mirroring the
// real-filesystem stub used by the identifier-index tests.
func newAliasResultGit(tmpDir string) *testvault.Client {
	return &testvault.Client{Dir: tmpDir}
}

// aliasFrontmatterValue unmarshals the frontmatter of content and returns the
// task_identifier_previous value.
func aliasFrontmatterValue(content []byte) string {
	fm, err := result.ExtractFrontmatter(context.Background(), content)
	Expect(err).NotTo(HaveOccurred())
	var fmMap map[string]interface{}
	Expect(yaml.Unmarshal([]byte(fm), &fmMap)).To(Succeed())
	value, _ := fmMap["task_identifier_previous"].(string)
	return value
}

var _ = Describe("FindTaskFilePath task_identifier_previous alias (spec 021)", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("AC2: resolves a file by its recorded predecessor", func() {
		tmpDir, err := os.MkdirTemp("", "result-alias-resolve-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(os.RemoveAll(tmpDir)).To(Succeed()) }()
		Expect(os.MkdirAll(filepath.Join(tmpDir, "tasks"), 0750)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(tmpDir, "tasks", "alias.md"),
			[]byte(
				"---\ntask_identifier: "+aliasUUID+
					"\ntask_identifier_previous: my-slug\ntarget_vault: personal\n---\nbody\n",
			),
			0600,
		)).To(Succeed())

		matchedRelPath, _, err := result.FindTaskFilePath(
			ctx, newAliasResultGit(tmpDir), "tasks", "my-slug", nil,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(matchedRelPath).To(Equal("tasks/alias.md"))
	})

	It("AC2: writes a result to the task resolved through the recorded predecessor", func() {
		tmpDir, err := os.MkdirTemp("", "result-alias-ac2-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(os.RemoveAll(tmpDir)).To(Succeed()) }()
		Expect(os.MkdirAll(filepath.Join(tmpDir, "tasks"), 0750)).To(Succeed())

		taskFile := filepath.Join(tmpDir, "tasks", "alias.md")
		Expect(os.WriteFile(taskFile, []byte(
			"---\ntask_identifier: "+aliasUUID+
				"\ntask_identifier_previous: my-slug\ntarget_vault: personal\n"+
				"status: in_progress\n---\nOld content\n",
		), 0600)).To(Succeed())

		fakeGit := newAliasResultGit(tmpDir)
		fakeMetrics := &mocks.Metrics{}
		fakeMetrics.ResultsWrittenTotalReturns(prometheus.NewCounter(prometheus.CounterOpts{
			Name: "test_results_written_total_alias",
		}))
		fakeTime := &libtimemocks.CurrentDateTimeGetter{}
		fakeTime.NowReturns(libtime.DateTime(time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)))

		writer := result.NewResultWriter(
			fakeGit,
			"tasks",
			"openclaw",
			fakeTime,
			fakeMetrics,
			libtime.NewWaiterDuration(),
			nil,
			nil,
			nil,
		)

		Expect(writer.WriteResult(ctx, lib.Task{
			TaskIdentifier: lib.TaskIdentifier("my-slug"),
			Frontmatter: lib.TaskFrontmatter{
				"status": "done",
				"phase":  "done",
			},
			Content: lib.TaskContent("New content\n"),
		})).To(Succeed())

		Expect(fakeMetrics.ResultsWrittenTotalCallCount()).To(Equal(1))
		Expect(fakeMetrics.ResultsWrittenTotalArgsForCall(0)).To(Equal("success"))

		written, err := os.ReadFile(taskFile) // #nosec G304 -- test-only path
		Expect(err).NotTo(HaveOccurred())
		Expect(aliasFrontmatterValue(written)).To(Equal("my-slug"))
	})

	It("AC7: an alias match plus a current-key match is a duplicate error", func() {
		tmpDir, err := os.MkdirTemp("", "result-alias-duplicate-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(os.RemoveAll(tmpDir)).To(Succeed()) }()
		Expect(os.MkdirAll(filepath.Join(tmpDir, "tasks"), 0750)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(tmpDir, "tasks", "first.md"),
			[]byte("---\ntask_identifier: my-slug\n---\n"),
			0600,
		)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(tmpDir, "tasks", "second.md"),
			[]byte("---\ntask_identifier: "+aliasUUID+"\ntask_identifier_previous: my-slug\n---\n"),
			0600,
		)).To(Succeed())

		matchedRelPath, fm, err := result.FindTaskFilePath(
			ctx, newAliasResultGit(tmpDir), "tasks", "my-slug", nil,
		)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("duplicate task_identifier"))
		Expect(err.Error()).To(ContainSubstring("tasks/first.md"))
		Expect(err.Error()).To(ContainSubstring("tasks/second.md"))
		Expect(matchedRelPath).To(Equal(""))
		Expect(fm).To(BeNil())
	})

	It("AC7: WriteResult refuses the duplicate pair and never writes", func() {
		tmpDir, err := os.MkdirTemp("", "result-alias-ac7-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(os.RemoveAll(tmpDir)).To(Succeed()) }()
		Expect(os.MkdirAll(filepath.Join(tmpDir, "tasks"), 0750)).To(Succeed())

		Expect(os.WriteFile(
			filepath.Join(tmpDir, "tasks", "first.md"),
			[]byte("---\ntask_identifier: my-slug\nstatus: in_progress\n---\n"),
			0600,
		)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(tmpDir, "tasks", "second.md"),
			[]byte(
				"---\ntask_identifier: "+aliasUUID+
					"\ntask_identifier_previous: my-slug\nstatus: in_progress\n---\n",
			),
			0600,
		)).To(Succeed())

		fakeGit := newAliasResultGit(tmpDir)
		fakeMetrics := &mocks.Metrics{}
		fakeMetrics.ResultsWrittenTotalReturns(prometheus.NewCounter(prometheus.CounterOpts{
			Name: "test_results_written_total_alias_dup",
		}))
		fakeTime := &libtimemocks.CurrentDateTimeGetter{}
		fakeTime.NowReturns(libtime.DateTime(time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)))

		writer := result.NewResultWriter(
			fakeGit,
			"tasks",
			"openclaw",
			fakeTime,
			fakeMetrics,
			libtime.NewWaiterDuration(),
			nil,
			nil,
			nil,
		)

		err = writer.WriteResult(ctx, lib.Task{
			TaskIdentifier: lib.TaskIdentifier("my-slug"),
			Frontmatter:    lib.TaskFrontmatter{"status": "done"},
			Content:        lib.TaskContent("New content\n"),
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("duplicate task_identifier"))
		// "Never writes" is asserted on the vault, not on a call count: the typed
		// client has no write-call counter, and both files must still read exactly
		// as they were seeded.
		first, readErr := os.ReadFile(filepath.Join(tmpDir, "tasks", "first.md"))
		Expect(readErr).NotTo(HaveOccurred())
		Expect(string(first)).To(Equal("---\ntask_identifier: my-slug\nstatus: in_progress\n---\n"))
		second, readErr := os.ReadFile(filepath.Join(tmpDir, "tasks", "second.md"))
		Expect(readErr).NotTo(HaveOccurred())
		Expect(string(second)).To(ContainSubstring("task_identifier_previous: my-slug"))
	})

	It("AC8: an alias match is not recorded in the identifier index", func() {
		tmpDir, err := os.MkdirTemp("", "result-alias-index-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(os.RemoveAll(tmpDir)).To(Succeed()) }()
		Expect(os.MkdirAll(filepath.Join(tmpDir, "tasks"), 0750)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(tmpDir, "tasks", "alias.md"),
			[]byte(
				"---\ntask_identifier: "+aliasUUID+
					"\ntask_identifier_previous: my-slug\n---\n",
			),
			0600,
		)).To(Succeed())
		resolver := &mocks.TaskPathResolver{}
		resolver.ResolveReturns("", false, nil)

		matchedRelPath, _, err := result.FindTaskFilePath(
			ctx, newAliasResultGit(tmpDir), "tasks", "my-slug", resolver,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(matchedRelPath).To(Equal("tasks/alias.md"))
		Expect(resolver.RecordCallCount()).To(Equal(0))
	})
})
