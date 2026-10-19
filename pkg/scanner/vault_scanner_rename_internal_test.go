// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

// This spec file is internal (package scanner, not scanner_test) because the
// rename spec has to seed the scanner's unexported bookkeeping (hashes) and
// assert the deleted and written values scanFiles returns. Driving scanFiles
// directly presents the post-rename directory state — the old path absent from
// the listing — without git plumbing, and it is the function whose ordering is
// the defect: pruning must happen before the per-file loop.

import (
	"context"

	lib "github.com/bborbe/agent"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
)

var _ = Describe("rename preserves task_identifier (spec 017)", func() {
	const (
		renameID = "5d83b642-1111-4111-8111-111111111111"
	)

	taskFile := func(id string) string {
		return "---\ntask_identifier: " + id +
			"\nstatus: in_progress\nassignee: claude\n---\nbody\n"
	}

	It("RenameKeepsIdentifier: a rename keeps the identifier and writes nothing", func() {
		ctx := context.Background()
		oldPath := "tasks/old.md"
		newPath := "tasks/new.md"
		newContent := taskFile(renameID)

		var writes []string
		v := &vaultScanner{
			metrics: metrics.New(),
			taskDir: "tasks",
			hashes: map[string]fileEntry{
				oldPath: {
					hash:           [32]byte{1},
					taskIdentifier: lib.TaskIdentifier(renameID),
					assignee:       lib.TaskAssignee("claude"),
				},
			},
			ops: fileOps{
				listFiles: func(_ context.Context, _ string) ([]string, error) {
					return []string{newPath}, nil
				},
				readFile: func(_ context.Context, _ string) ([]byte, error) {
					return []byte(newContent), nil
				},
				writeFile: func(_ context.Context, relPath string, _ []byte) error {
					writes = append(writes, relPath)
					return nil
				},
			},
			autoInject: true,
			index:      make(map[lib.TaskIdentifier][]string),
		}

		changed, deleted, writtenPaths, writeError := v.scanFiles(ctx)

		Expect(writeError).To(BeFalse())
		Expect(writes).To(BeEmpty(), "a rename must not write the file")
		Expect(writtenPaths).To(BeEmpty())
		Expect(deleted).To(ConsistOf(lib.TaskIdentifier(renameID)))
		Expect(v.hashes).NotTo(HaveKey(oldPath))
		Expect(v.hashes).To(HaveLen(1))
		Expect(v.hashes[newPath].taskIdentifier).To(Equal(lib.TaskIdentifier(renameID)))
		Expect(changed).To(HaveLen(1))
		Expect(changed[0].TaskIdentifier).To(Equal(lib.TaskIdentifier(renameID)))
		path, found, err := v.Resolve(ctx, lib.TaskIdentifier(renameID))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(path).To(Equal(newPath))
		Expect(v.index[lib.TaskIdentifier(renameID)]).To(ConsistOf(newPath))
	})

	It(
		"GenuineDuplicateStillReMints: two existing files carrying one identifier re-mint one",
		func() {
			ctx := context.Background()
			firstPath := "tasks/first.md"
			secondPath := "tasks/second.md"
			duplicateContent := taskFile(renameID)

			var writes []string
			var lastWritten []byte
			v := &vaultScanner{
				metrics: metrics.New(),
				taskDir: "tasks",
				hashes:  make(map[string]fileEntry),
				ops: fileOps{
					listFiles: func(_ context.Context, _ string) ([]string, error) {
						return []string{firstPath, secondPath}, nil
					},
					readFile: func(_ context.Context, _ string) ([]byte, error) {
						return []byte(duplicateContent), nil
					},
					writeFile: func(_ context.Context, relPath string, content []byte) error {
						writes = append(writes, relPath)
						lastWritten = append([]byte(nil), content...)
						return nil
					},
				},
				autoInject: true,
				index:      make(map[lib.TaskIdentifier][]string),
			}

			changed, deleted, writtenPaths, writeError := v.scanFiles(ctx)

			Expect(writeError).To(BeFalse())
			Expect(writes).To(Equal([]string{secondPath}), "exactly one file is re-minted")
			Expect(writtenPaths).To(Equal([]string{secondPath}))
			Expect(deleted).To(BeEmpty())
			Expect(v.hashes[firstPath].taskIdentifier).To(Equal(lib.TaskIdentifier(renameID)))
			Expect(v.hashes[secondPath].taskIdentifier).NotTo(Equal(lib.TaskIdentifier(renameID)))
			Expect(isValidUUID(string(v.hashes[secondPath].taskIdentifier))).To(BeTrue())
			Expect(changed).To(HaveLen(1))
			Expect(changed[0].TaskIdentifier).To(Equal(lib.TaskIdentifier(renameID)))
			Expect(string(lastWritten)).NotTo(ContainSubstring(renameID))
			Expect(string(lastWritten)).To(ContainSubstring("task_identifier: "))
			Expect(v.index[lib.TaskIdentifier(renameID)]).To(ConsistOf(firstPath))
			Expect(v.index).To(HaveLen(2))
		},
	)
})
