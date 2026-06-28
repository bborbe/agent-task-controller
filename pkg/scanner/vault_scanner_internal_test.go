// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	lib "github.com/bborbe/agent"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
)

var _ = Describe("injectAndStore", func() {
	counterValue := func(reason string) float64 {
		mfs, err := prometheus.DefaultGatherer.Gather()
		Expect(err).NotTo(HaveOccurred())
		for _, mf := range mfs {
			if mf.GetName() != "agent_controller_vault_scanner_skipped_files_total" {
				continue
			}
			for _, m := range mf.GetMetric() {
				for _, lp := range m.GetLabel() {
					if lp.GetName() == "reason" && lp.GetValue() == reason {
						return m.GetCounter().GetValue()
					}
				}
			}
		}
		return 0
	}

	It(
		"increments inject_task_identifier_failed counter when InjectTaskIdentifier returns error",
		func() {
			v := &vaultScanner{
				metrics: metrics.New(),
				ops: fileOps{
					readFile: func(_ context.Context, _ string) ([]byte, error) {
						return nil, nil
					},
					writeFile: func(_ context.Context, _ string, _ []byte) error {
						return nil
					},
				},
				autoInject: true,
			}

			initial := counterValue(metrics.ReasonInjectTaskIdentifierFailed)
			initialInvalid := counterValue(metrics.ReasonInvalidFrontmatter)
			initialDupInvalid := counterValue(metrics.ReasonDuplicateFrontmatterInvalid)
			initialEmptyStatus := counterValue(metrics.ReasonEmptyStatus)
			initialReadFailed := counterValue(metrics.ReasonReadFailed)

			// Content without frontmatter delimiter causes InjectTaskIdentifier to fail
			task, written, werr := v.injectAndStore(
				context.Background(),
				[]byte("no frontmatter at all"),
				"rel.md",
				"",
			)
			Expect(task).To(BeNil())
			Expect(written).To(Equal(""))
			Expect(werr).To(BeFalse())
			Expect(counterValue(metrics.ReasonInjectTaskIdentifierFailed)).To(Equal(initial + 1))

			// Other reason labels must not tick (compared to initial values)
			Expect(
				counterValue(metrics.ReasonInvalidFrontmatter),
			).To(BeNumerically("==", initialInvalid))
			Expect(
				counterValue(metrics.ReasonDuplicateFrontmatterInvalid),
			).To(BeNumerically("==", initialDupInvalid))
			Expect(
				counterValue(metrics.ReasonEmptyStatus),
			).To(BeNumerically("==", initialEmptyStatus))
			Expect(
				counterValue(metrics.ReasonReadFailed),
			).To(BeNumerically("==", initialReadFailed))
		},
	)
})

var _ = Describe("auto-inject flag gate (spec 001)", func() {
	counterValue := func(reason string) float64 {
		mfs, err := prometheus.DefaultGatherer.Gather()
		Expect(err).NotTo(HaveOccurred())
		for _, mf := range mfs {
			if mf.GetName() != "agent_controller_vault_scanner_skipped_files_total" {
				continue
			}
			for _, m := range mf.GetMetric() {
				for _, lp := range m.GetLabel() {
					if lp.GetName() == "reason" && lp.GetValue() == reason {
						return m.GetCounter().GetValue()
					}
				}
			}
		}
		return 0
	}

	It("skips the empty-task_identifier site without writing when autoInject=false", func() {
		ctx := context.Background()
		var writeCount int
		v := &vaultScanner{
			metrics: metrics.New(),
			hashes:  make(map[string]fileEntry),
			ops: fileOps{
				readFile: func(_ context.Context, _ string) ([]byte, error) {
					return []byte("---\nstatus: in_progress\nassignee: claude\n---\n# body\n"), nil
				},
				writeFile: func(_ context.Context, _ string, _ []byte) error {
					writeCount++
					return nil
				},
			},
			autoInject: false,
		}
		before := counterValue(metrics.ReasonAutoInjectDisabled)

		task, written, werr := v.processFile(ctx, "empty-id.md")

		Expect(task).To(BeNil())
		Expect(written).To(Equal(""))
		Expect(werr).To(BeFalse())
		Expect(writeCount).To(Equal(0))
		Expect(counterValue(metrics.ReasonAutoInjectDisabled)).To(Equal(before + 1))
	})

	It("skips the non-UUID-task_identifier site without writing when autoInject=false", func() {
		ctx := context.Background()
		var writeCount int
		v := &vaultScanner{
			metrics: metrics.New(),
			hashes:  make(map[string]fileEntry),
			ops: fileOps{
				readFile: func(_ context.Context, _ string) ([]byte, error) {
					return []byte("---\ntask_identifier: not-a-uuid\nstatus: in_progress\nassignee: claude\n---\n# body\n"), nil
				},
				writeFile: func(_ context.Context, _ string, _ []byte) error {
					writeCount++
					return nil
				},
			},
			autoInject: false,
		}
		before := counterValue(metrics.ReasonAutoInjectDisabled)

		task, written, werr := v.processFile(ctx, "non-uuid.md")

		Expect(task).To(BeNil())
		Expect(written).To(Equal(""))
		Expect(werr).To(BeFalse())
		Expect(writeCount).To(Equal(0))
		Expect(counterValue(metrics.ReasonAutoInjectDisabled)).To(Equal(before + 1))
	})

	It("skips the duplicate-task_identifier site without writing when autoInject=false", func() {
		ctx := context.Background()
		var writeCount int
		dup := "11111111-1111-4111-8111-111111111111"
		v := &vaultScanner{
			metrics: metrics.New(),
			hashes: map[string]fileEntry{
				"other.md": {taskIdentifier: lib.TaskIdentifier(dup)},
			},
			ops: fileOps{
				readFile: func(_ context.Context, _ string) ([]byte, error) {
					return []byte("---\ntask_identifier: " + dup + "\nstatus: in_progress\nassignee: claude\n---\n# body\n"), nil
				},
				writeFile: func(_ context.Context, _ string, _ []byte) error {
					writeCount++
					return nil
				},
			},
			autoInject: false,
		}
		before := counterValue(metrics.ReasonAutoInjectDisabled)

		task, written, werr := v.processFile(ctx, "dup.md")

		Expect(task).To(BeNil())
		Expect(written).To(Equal(""))
		Expect(werr).To(BeFalse())
		Expect(writeCount).To(Equal(0))
		Expect(counterValue(metrics.ReasonAutoInjectDisabled)).To(Equal(before + 1))
	})

	It("injects UUIDs at all three trigger sites and does not tick the disabled counter when autoInject=true", func() {
		ctx := context.Background()
		var writeCount int
		dup := "11111111-1111-4111-8111-111111111111"
		fixtures := map[string][]byte{
			"empty-id.md": []byte("---\nstatus: in_progress\nassignee: claude\n---\n# body\n"),
			"non-uuid.md": []byte("---\ntask_identifier: not-a-uuid\nstatus: in_progress\nassignee: claude\n---\n# body\n"),
			"dup.md":      []byte("---\ntask_identifier: " + dup + "\nstatus: in_progress\nassignee: claude\n---\n# body\n"),
		}
		v := &vaultScanner{
			metrics: metrics.New(),
			hashes: map[string]fileEntry{
				"other.md": {taskIdentifier: lib.TaskIdentifier(dup)},
			},
			ops: fileOps{
				readFile: func(_ context.Context, relPath string) ([]byte, error) {
					return fixtures[relPath], nil
				},
				writeFile: func(_ context.Context, _ string, _ []byte) error {
					writeCount++
					return nil
				},
			},
			autoInject: true,
		}
		before := counterValue(metrics.ReasonAutoInjectDisabled)

		for _, relPath := range []string{"empty-id.md", "non-uuid.md", "dup.md"} {
			_, written, werr := v.processFile(ctx, relPath)
			Expect(werr).To(BeFalse(), "site %s: write error", relPath)
			Expect(written).To(Equal(relPath), "site %s: should have written", relPath)
		}
		Expect(writeCount).To(Equal(3))
		Expect(counterValue(metrics.ReasonAutoInjectDisabled)).To(Equal(before))
	})

	It("does NOT gate the writeCounterReset path when autoInject=false (AC7)", func() {
		ctx := context.Background()
		var writeCount int
		taskID := "22222222-2222-4222-8222-222222222222"
		v := &vaultScanner{
			metrics: metrics.New(),
			hashes: map[string]fileEntry{
				"parked.md": {
					hash:           [32]byte{}, // any non-matching hash so the file looks "changed"
					taskIdentifier: lib.TaskIdentifier(taskID),
					assignee:       lib.TaskAssignee(""),
				},
			},
			ops: fileOps{
				readFile: func(_ context.Context, _ string) ([]byte, error) {
					return []byte("---\ntask_identifier: " + taskID + "\nstatus: in_progress\nassignee: claude\n---\n# body\n"), nil
				},
				writeFile: func(_ context.Context, _ string, _ []byte) error {
					writeCount++
					return nil
				},
			},
			autoInject: false,
		}
		beforeDisabled := counterValue(metrics.ReasonAutoInjectDisabled)

		_, written, werr := v.processFile(ctx, "parked.md")

		Expect(werr).To(BeFalse())
		Expect(written).To(Equal("parked.md"), "writeCounterReset write must have happened")
		Expect(writeCount).To(Equal(1))
		Expect(counterValue(metrics.ReasonAutoInjectDisabled)).To(Equal(beforeDisabled),
			"ReasonAutoInjectDisabled must NOT tick on the counter-reset path (AC7)")
	})
})
