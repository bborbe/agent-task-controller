// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"os"
	"sort"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
)

// factsCacheVault isolates this file's series from the other internal tests, which
// share the package-level gauges.
const factsCacheVault = "facts-cache-vault"

// factsCacheScanner builds a scanner over a mutable vault state, so a test can move
// a file's listing stamp between passes the way a real write would. stamps is read
// on every pass, unreadable makes a path fail its read, and reads counts the
// readFile calls the cache did not prevent.
func factsCacheScanner(
	files map[string]string,
	unreadable map[string]struct{},
	stamps map[string]*time.Time,
	reads *int,
) *vaultScanner {
	paths := make([]string, 0, len(files)+len(unreadable))
	for relPath := range files {
		paths = append(paths, relPath)
	}
	for relPath := range unreadable {
		paths = append(paths, relPath)
	}
	sort.Strings(paths)

	return &vaultScanner{
		metrics:   metrics.New(),
		taskDir:   "tasks",
		vaultName: factsCacheVault,
		ops: fileOps{
			listFiles: func(_ context.Context, _ string) ([]string, error) {
				return paths, nil
			},
			readFile: func(_ context.Context, relPath string) ([]byte, error) {
				*reads++
				if _, ok := unreadable[relPath]; ok {
					return nil, os.ErrNotExist
				}
				content, ok := files[relPath]
				if !ok {
					return nil, os.ErrNotExist
				}
				return []byte(content), nil
			},
			updatedAt: func(relPath string) *time.Time {
				return stamps[relPath]
			},
		},
	}
}

var _ = Describe("metrics facts cache", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	// This is SC2's discriminating half (a). A cache that skips a file because its
	// stamp has not moved would drop a capped record from the candidate set, and the
	// sweep would stop escalating it — the exact failure the incremental scan must
	// not introduce. Two passes, one unchanged stamp, and the record must be a
	// candidate on both.
	It("still returns a capped record as a candidate on a pass that reuses its facts", func() {
		stamp := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
		files := map[string]string{"tasks/capped.md": sweepCappedRecord()}
		stamps := map[string]*time.Time{"tasks/capped.md": &stamp}
		reads := 0
		vs := factsCacheScanner(files, nil, stamps, &reads)

		first, _, firstReads, ok := vs.recomputeVaultMetrics(ctx)
		Expect(ok).To(BeTrue())
		Expect(first).To(ConsistOf("tasks/capped.md"))
		Expect(firstReads).To(Equal(1))

		second, _, secondReads, ok := vs.recomputeVaultMetrics(ctx)
		Expect(ok).To(BeTrue())
		Expect(secondReads).To(Equal(0), "an unchanged stamp must not be read again")
		Expect(second).To(ConsistOf("tasks/capped.md"),
			"a reused fact must still produce the candidate")
	})

	// The other half of the same claim: the skip must end the moment the file
	// changes. A cache that never invalidates would report the old facts forever.
	It("re-reads a file whose listing stamp moved", func() {
		before := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
		after := before.Add(5 * time.Minute)
		files := map[string]string{"tasks/plain.md": taskAgeSummaryFile(
			"in_progress", "phase: execution", "task_type: llm",
			taskAgeSummaryQuoted("created", 2*time.Hour),
		)}
		stamps := map[string]*time.Time{"tasks/plain.md": &before}
		reads := 0
		vs := factsCacheScanner(files, nil, stamps, &reads)

		_, _, firstReads, ok := vs.recomputeVaultMetrics(ctx)
		Expect(ok).To(BeTrue())
		Expect(firstReads).To(Equal(1))

		stamps["tasks/plain.md"] = &after

		_, _, secondReads, ok := vs.recomputeVaultMetrics(ctx)
		Expect(ok).To(BeTrue())
		Expect(secondReads).To(Equal(1), "a moved stamp must force a re-read")
	})

	// SC2's discriminating half (b): a file that could not be read on one pass must
	// be read on a later one. Without this, an unreadable file would be cached as
	// skipped and starve out of every later candidate set.
	It("re-reads a file it could not read on an earlier pass", func() {
		stamp := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
		files := map[string]string{}
		unreadable := map[string]struct{}{"tasks/capped.md": {}}
		stamps := map[string]*time.Time{"tasks/capped.md": &stamp}
		reads := 0
		vs := factsCacheScanner(files, unreadable, stamps, &reads)

		first, _, firstReads, ok := vs.recomputeVaultMetrics(ctx)
		Expect(ok).To(BeTrue())
		Expect(firstReads).To(Equal(1))
		Expect(first).To(BeEmpty())

		delete(unreadable, "tasks/capped.md")
		files["tasks/capped.md"] = sweepCappedRecord()

		second, _, secondReads, ok := vs.recomputeVaultMetrics(ctx)
		Expect(ok).To(BeTrue())
		Expect(secondReads).To(Equal(1), "a previously unreadable file must be retried")
		Expect(second).To(ConsistOf("tasks/capped.md"))
	})

	// A nil stamp is "unknown", never "unchanged": without this, a listing that
	// stopped carrying updated_at would freeze every fact at its first value.
	It("reads every pass when the listing carries no stamp", func() {
		files := map[string]string{"tasks/capped.md": sweepCappedRecord()}
		reads := 0
		vs := factsCacheScanner(files, nil, map[string]*time.Time{}, &reads)

		_, _, firstReads, ok := vs.recomputeVaultMetrics(ctx)
		Expect(ok).To(BeTrue())
		Expect(firstReads).To(Equal(1))

		_, _, secondReads, ok := vs.recomputeVaultMetrics(ctx)
		Expect(ok).To(BeTrue())
		Expect(secondReads).To(Equal(1), "an unknown stamp must never be treated as unchanged")
	})
})
