// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	lib "github.com/bborbe/agent"
	"github.com/golang/glog"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
)

// captureGlogWarnings runs fn with glog WARNING output captured and returns the
// captured bytes. glog's -logtostderr defaults to false, so warnings otherwise
// go to files; setting the flag routes them to stderr, which we redirect to a
// pipe. The previous flag value is restored alongside os.Stderr so later specs
// do not inherit glog-on-stderr. A drain goroutine reads the pipe concurrently,
// so fn can never block on a full 64 KB kernel pipe buffer. Ginkgo runs specs
// serially, so the global os.Stderr redirect is safe.
func captureGlogWarnings(fn func()) string {
	oldLogToStderr := flag.Lookup("logtostderr").Value.String()
	Expect(flag.Set("logtostderr", "true")).To(Succeed())
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	Expect(err).NotTo(HaveOccurred())
	os.Stderr = w

	type drainResult struct {
		out []byte
		err error
	}
	// Drain concurrently and BEFORE fn runs: an unread pipe blocks the writer
	// once the 64 KB kernel buffer fills. AC7 emits roughly 12 KB today, so a
	// read-after-run helper only works by luck.
	done := make(chan drainResult, 1)
	go func() {
		b, rerr := io.ReadAll(r)
		done <- drainResult{out: b, err: rerr}
	}()

	fn()

	// Flush glog's buffers into the pipe FIRST (a flush after the read is dead
	// code), then restore os.Stderr before closing the writer so glog's
	// flushDaemon goroutine can never write into a closed pipe mid-read, then
	// close the writer so the drain goroutine sees EOF.
	glog.Flush()
	os.Stderr = oldStderr
	Expect(flag.Set("logtostderr", oldLogToStderr)).To(Succeed())
	_ = w.Close()

	res := <-done
	Expect(res.err).NotTo(HaveOccurred())
	_ = r.Close()
	return string(res.out)
}

// countLinesMatching returns the number of lines in content that match re.
func countLinesMatching(content []byte, re *regexp.Regexp) int {
	n := 0
	for _, line := range strings.Split(string(content), "\n") {
		if re.MatchString(line) {
			n++
		}
	}
	return n
}

var _ = Describe("injectAndStore", func() {
	counterValue := func(reason metrics.SkipReason) float64 {
		mfs, err := prometheus.DefaultGatherer.Gather()
		Expect(err).NotTo(HaveOccurred())
		for _, mf := range mfs {
			if mf.GetName() != "agent_controller_vault_scanner_skipped_files_total" {
				continue
			}
			for _, m := range mf.GetMetric() {
				for _, lp := range m.GetLabel() {
					if lp.GetName() == "reason" && lp.GetValue() == reason.String() {
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
	counterValue := func(reason metrics.SkipReason) float64 {
		mfs, err := prometheus.DefaultGatherer.Gather()
		Expect(err).NotTo(HaveOccurred())
		for _, mf := range mfs {
			if mf.GetName() != "agent_controller_vault_scanner_skipped_files_total" {
				continue
			}
			for _, m := range mf.GetMetric() {
				for _, lp := range m.GetLabel() {
					if lp.GetName() == "reason" && lp.GetValue() == reason.String() {
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
					return []byte(
						"---\ntask_identifier: not-a-uuid\nstatus: in_progress\nassignee: claude\n---\n# body\n",
					), nil
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
					return []byte(
						"---\ntask_identifier: " + dup + "\nstatus: in_progress\nassignee: claude\n---\n# body\n",
					), nil
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

	It(
		"injects UUIDs at all three trigger sites and does not tick the disabled counter when autoInject=true",
		func() {
			ctx := context.Background()
			var writeCount int
			dup := "11111111-1111-4111-8111-111111111111"
			fixtures := map[string][]byte{
				"empty-id.md": []byte("---\nstatus: in_progress\nassignee: claude\n---\n# body\n"),
				"non-uuid.md": []byte(
					"---\ntask_identifier: not-a-uuid\nstatus: in_progress\nassignee: claude\n---\n# body\n",
				),
				"dup.md": []byte(
					"---\ntask_identifier: " + dup + "\nstatus: in_progress\nassignee: claude\n---\n# body\n",
				),
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
		},
	)

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
					return []byte(
						"---\ntask_identifier: " + taskID + "\nstatus: in_progress\nassignee: claude\n---\n# body\n",
					), nil
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

var _ = Describe("removeTaskIdentifier (spec 008)", func() {
	It("removes key lines and their value spans byte-exactly", func() {
		// Double-quoted key spelling.
		Expect(string(removeTaskIdentifier([]byte(`---
"task_identifier": 501
status: in_progress
---
body
`)))).To(Equal(`---
status: in_progress
---
body
`))
		// Whitespace before the colon.
		Expect(string(removeTaskIdentifier([]byte(`---
task_identifier : 501
status: in_progress
---
body
`)))).To(Equal(`---
status: in_progress
---
body
`))
		// Block-style sequence value span.
		Expect(string(removeTaskIdentifier([]byte(`---
task_identifier:
  - a
  - b
status: in_progress
---
body
`)))).To(Equal(`---
status: in_progress
---
body
`))
		// Block-style mapping value span.
		Expect(string(removeTaskIdentifier([]byte(`---
task_identifier:
  a: b
status: in_progress
---
body
`)))).To(Equal(`---
status: in_progress
---
body
`))
		// Block scalar value span.
		Expect(string(removeTaskIdentifier([]byte(`---
task_identifier: |
  abc
status: in_progress
---
body
`)))).To(Equal(`---
status: in_progress
---
body
`))
		// Multiple key lines are all removed.
		Expect(string(removeTaskIdentifier([]byte(`---
task_identifier: 501
status: in_progress
task_identifier: 502
---
body
`)))).To(Equal(`---
status: in_progress
---
body
`))
		// A body line at column 0 inside a fenced block survives byte-identically.
		fenced := "---\ntask_identifier: 501\nstatus: in_progress\n---\n# Runbook\n\n```\ntask_identifier: 501\n```\n"
		Expect(
			string(removeTaskIdentifier([]byte(fenced))),
		).To(Equal("---\nstatus: in_progress\n---\n# Runbook\n\n```\ntask_identifier: 501\n```\n"))
		// CRLF file: \r is preserved on every kept line.
		crlf := "---\r\ntask_identifier: 501\r\nstatus: in_progress\r\n---\r\nbody\r\n"
		Expect(
			string(removeTaskIdentifier([]byte(crlf))),
		).To(Equal("---\r\nstatus: in_progress\r\n---\r\nbody\r\n"))
		// Unterminated frontmatter is a safety no-op.
		unterminated := "---\ntask_identifier: 501\nstatus: in_progress\n"
		Expect(string(removeTaskIdentifier([]byte(unterminated)))).To(Equal(unterminated))
	})

	// parseFrontmatterMap asserts the frontmatter of content is still valid
	// YAML after removal and returns it as a map. A span that terminates early
	// leaves an orphaned value fragment behind, which fails here.
	parseFrontmatterMap := func(content []byte) map[string]interface{} {
		fm, err := extractFrontmatter(context.Background(), content)
		Expect(err).NotTo(HaveOccurred())
		var fmMap map[string]interface{}
		Expect(yaml.Unmarshal([]byte(fm), &fmMap)).To(Succeed())
		return fmMap
	}

	// expectKeyGone asserts the frontmatter parses and no longer carries a
	// task_identifier key, while the following top-level key survived.
	expectKeyGone := func(content []byte) {
		fmMap := parseFrontmatterMap(content)
		_, hasKey := fmMap["task_identifier"]
		Expect(hasKey).To(BeFalse())
		status, isString := fmMap["status"].(string)
		Expect(isString).To(BeTrue())
		Expect(status).To(Equal("in_progress"))
	}

	It("removes a block scalar value containing a genuinely empty line", func() {
		// yaml.v3 emits genuinely empty lines (length 0, not whitespace-only)
		// for blanks inside | and > scalars. A length-based indentation test
		// terminates the span there and orphans "  third line".
		in := "---\ntask_identifier: |\n  first line\n\n  third line\n" +
			"status: in_progress\n---\nbody\n"
		out := removeTaskIdentifier([]byte(in))
		Expect(string(out)).To(Equal("---\nstatus: in_progress\n---\nbody\n"))
		expectKeyGone(out)
	})

	It("removes a folded scalar value containing a genuinely empty line", func() {
		in := "---\ntask_identifier: >\n  first line\n\n  third line\n" +
			"status: in_progress\n---\nbody\n"
		out := removeTaskIdentifier([]byte(in))
		Expect(string(out)).To(Equal("---\nstatus: in_progress\n---\nbody\n"))
		expectKeyGone(out)
	})

	It("removes a block sequence whose items are separated by a blank line", func() {
		in := "---\ntask_identifier:\n  - a\n\n  - b\nstatus: in_progress\n---\nbody\n"
		out := removeTaskIdentifier([]byte(in))
		Expect(string(out)).To(Equal("---\nstatus: in_progress\n---\nbody\n"))
		expectKeyGone(out)
	})

	It("preserves a blank line separating the span from the next key", func() {
		// Trailing blanks are trimmed back out of the removal set, so a
		// separator blank line before the next top-level key survives instead
		// of being swallowed (the over-deletion this must not regress into).
		in := "---\ntask_identifier: |\n  abc\n\nstatus: in_progress\n---\nbody\n"
		out := removeTaskIdentifier([]byte(in))
		Expect(string(out)).To(Equal("---\n\nstatus: in_progress\n---\nbody\n"))
		expectKeyGone(out)
	})

	It("preserves a blank separator after a block sequence value span", func() {
		in := "---\ntask_identifier:\n  - a\n\n  - b\n\nstatus: in_progress\n---\nbody\n"
		out := removeTaskIdentifier([]byte(in))
		Expect(string(out)).To(Equal("---\n\nstatus: in_progress\n---\nbody\n"))
		expectKeyGone(out)
	})

	It("keeps a trailing blank line before the closing delimiter", func() {
		// Pending blanks left over when the closing --- is reached are trimmed
		// back out too, so the frontmatter keeps its trailing blank line.
		in := "---\nstatus: in_progress\ntask_identifier: |\n  abc\n\n---\nbody\n"
		out := removeTaskIdentifier([]byte(in))
		Expect(string(out)).To(Equal("---\nstatus: in_progress\n\n---\nbody\n"))
		expectKeyGone(out)
	})
})

var _ = Describe("task_identifier backfill repair (spec 008)", func() {
	counterValue := func(reason metrics.SkipReason) float64 {
		mfs, err := prometheus.DefaultGatherer.Gather()
		Expect(err).NotTo(HaveOccurred())
		for _, mf := range mfs {
			if mf.GetName() != "agent_controller_vault_scanner_skipped_files_total" {
				continue
			}
			for _, m := range mf.GetMetric() {
				for _, lp := range m.GetLabel() {
					if lp.GetName() == "reason" && lp.GetValue() == reason.String() {
						return m.GetCounter().GetValue()
					}
				}
			}
		}
		return 0
	}

	// taskIdentifierKeyLineRe is the spec's key-aware match used to count
	// surviving task_identifier key lines in repaired files. Unlike the plain
	// ^task_identifier: prefix, it also sees the quoted and spaced key spellings.
	var taskIdentifierKeyLineRe = regexp.MustCompile(`^[[:space:]]*['"]?task_identifier['"]?[[:space:]]*:`)

	var ac2Fixtures = []struct {
		label       string
		relPath     string
		frontmatter string // frontmatter line(s) between the opening and closing --- delimiters
	}{
		{"row 1 - unquoted int (production incident)", "ac2-01-int.md", "task_identifier: 501"},
		{"row 2 - bool", "ac2-02-bool.md", "task_identifier: true"},
		{"row 3 - null", "ac2-03-null.md", "task_identifier: null"},
		{"row 4 - float", "ac2-04-float.md", "task_identifier: 1.5"},
		{"row 5 - empty double-quoted string", "ac2-05-empty-dq.md", `task_identifier: ""`},
		{"row 6 - empty single-quoted string", "ac2-06-empty-sq.md", `task_identifier: ''`},
		{"row 7 - whitespace-only string", "ac2-07-ws.md", `task_identifier: "   "`},
		{"row 8 - flow sequence", "ac2-08-flow-seq.md", "task_identifier: [a, b]"},
		{"row 9 - flow mapping", "ac2-09-flow-map.md", "task_identifier: {a: b}"},
		{"row 10 - block-style sequence", "ac2-10-block-seq.md", "task_identifier:\n  - a\n  - b"},
		{"row 11 - block-style mapping", "ac2-11-block-map.md", "task_identifier:\n  a: b"},
		{"row 12 - block scalar", "ac2-12-block-scalar.md", "task_identifier: |\n  abc"},
		{
			"row 13 - quoted non-UUID string (parity row)",
			"ac2-13-quoted.md",
			`task_identifier: "501"`,
		},
		{"row 14 - double-quoted key", "ac2-14-dq-key.md", `"task_identifier": 501`},
		{"row 15 - space before colon", "ac2-15-spaced-colon.md", "task_identifier : 501"},
		{"row 16 - single-quoted key", "ac2-16-sq-key.md", `'task_identifier': 501`},
	}

	ac2Content := func(frontmatter string) string {
		return "---\n" + frontmatter + "\nstatus: in_progress\nassignee: claude\n---\nbody\n"
	}

	It(
		"AC1: repairs the production-shape unquoted int in exactly one write across five cycles",
		func() {
			ctx := context.Background()
			tmpDir, err := os.MkdirTemp("", "scanner-spec008-ac1-*")
			Expect(err).NotTo(HaveOccurred())
			defer func() { Expect(os.RemoveAll(tmpDir)).To(Succeed()) }()

			fixture := ac2Content("task_identifier: 501")
			Expect(
				os.WriteFile(filepath.Join(tmpDir, "ac1.md"), []byte(fixture), 0600),
			).To(Succeed())

			var writeCount int
			v := &vaultScanner{
				metrics: metrics.New(),
				hashes:  make(map[string]fileEntry),
				ops: fileOps{
					readFile: func(_ context.Context, p string) ([]byte, error) {
						return os.ReadFile(
							filepath.Join(tmpDir, p),
						) // #nosec G304 -- test-only path
					},
					writeFile: func(_ context.Context, relPath string, content []byte) error {
						writeCount++
						return os.WriteFile(filepath.Join(tmpDir, relPath), content, 0600)
					},
				},
				autoInject: true,
			}

			for i := 0; i < 5; i++ {
				_, _, werr := v.processFile(ctx, "ac1.md")
				Expect(werr).To(BeFalse())
			}
			Expect(writeCount).To(Equal(1), "ac1.md must be repaired in exactly one write")

			finalBytes, err := os.ReadFile(
				filepath.Join(tmpDir, "ac1.md"),
			) // #nosec G304 -- test-only path
			Expect(err).NotTo(HaveOccurred())
			Expect(countLinesMatching(finalBytes, taskIdentifierKeyLineRe)).To(Equal(1))

			fm, err := extractFrontmatter(ctx, finalBytes)
			Expect(err).NotTo(HaveOccurred())
			var fmMap map[string]interface{}
			Expect(yaml.Unmarshal([]byte(fm), &fmMap)).To(Succeed())
			idStr, isString := fmMap["task_identifier"].(string)
			Expect(isString).To(BeTrue())
			_, parseErr := uuid.Parse(idStr)
			Expect(parseErr).NotTo(HaveOccurred())
		},
	)

	It("AC2: converges all 16 malformed shapes in exactly one write each", func() {
		ctx := context.Background()
		tmpDir, err := os.MkdirTemp("", "scanner-spec008-ac2-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(os.RemoveAll(tmpDir)).To(Succeed()) }()

		for _, fx := range ac2Fixtures {
			fixture := ac2Content(fx.frontmatter)
			Expect(
				os.WriteFile(filepath.Join(tmpDir, fx.relPath), []byte(fixture), 0600),
			).To(Succeed())

			// Fresh counter and fresh scanner per row so each row's write-count
			// assertion is independent of every preceding row.
			writeCount := 0
			v := &vaultScanner{
				metrics: metrics.New(),
				hashes:  make(map[string]fileEntry),
				ops: fileOps{
					readFile: func(_ context.Context, p string) ([]byte, error) {
						return os.ReadFile(
							filepath.Join(tmpDir, p),
						) // #nosec G304 -- test-only path
					},
					writeFile: func(_ context.Context, relPath string, content []byte) error {
						writeCount++
						return os.WriteFile(filepath.Join(tmpDir, relPath), content, 0600)
					},
				},
				autoInject: true,
			}

			for i := 0; i < 5; i++ {
				_, _, werr := v.processFile(ctx, fx.relPath)
				Expect(werr).To(BeFalse(), fx.label)
			}
			Expect(writeCount).To(Equal(1), fx.label)

			finalBytes, err := os.ReadFile(
				filepath.Join(tmpDir, fx.relPath),
			) // #nosec G304 -- test-only path
			Expect(err).NotTo(HaveOccurred(), fx.label)
			Expect(countLinesMatching(finalBytes, taskIdentifierKeyLineRe)).To(Equal(1), fx.label)

			fm, err := extractFrontmatter(ctx, finalBytes)
			Expect(err).NotTo(HaveOccurred(), fx.label)
			var fmMap map[string]interface{}
			Expect(yaml.Unmarshal([]byte(fm), &fmMap)).To(Succeed(), fx.label)
			idStr, isString := fmMap["task_identifier"].(string)
			Expect(isString).To(BeTrue(), fx.label)
			_, parseErr := uuid.Parse(idStr)
			Expect(parseErr).NotTo(HaveOccurred(), fx.label)
		}
	})

	It("AC3: key-absent files are injected from the unmodified content byte-for-byte", func() {
		ctx := context.Background()
		tmpDir, err := os.MkdirTemp("", "scanner-spec008-ac3-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(os.RemoveAll(tmpDir)).To(Succeed()) }()

		fixture := "---\nstatus: in_progress\nassignee: claude\n---\nbody\n"
		Expect(os.WriteFile(filepath.Join(tmpDir, "ac3.md"), []byte(fixture), 0600)).To(Succeed())

		var writeCount int
		var lastWritten []byte
		v := &vaultScanner{
			metrics: metrics.New(),
			hashes:  make(map[string]fileEntry),
			ops: fileOps{
				readFile: func(_ context.Context, p string) ([]byte, error) {
					return os.ReadFile(filepath.Join(tmpDir, p)) // #nosec G304 -- test-only path
				},
				writeFile: func(_ context.Context, relPath string, content []byte) error {
					writeCount++
					lastWritten = append([]byte(nil), content...)
					return os.WriteFile(filepath.Join(tmpDir, relPath), content, 0600)
				},
			},
			autoInject: true,
		}

		captured := captureGlogWarnings(func() {
			for i := 0; i < 5; i++ {
				_, _, werr := v.processFile(ctx, "ac3.md")
				Expect(werr).To(BeFalse())
			}
		})

		Expect(writeCount).To(Equal(1))

		fm, err := extractFrontmatter(ctx, lastWritten)
		Expect(err).NotTo(HaveOccurred())
		var fmMap map[string]interface{}
		Expect(yaml.Unmarshal([]byte(fm), &fmMap)).To(Succeed())
		id, isString := fmMap["task_identifier"].(string)
		Expect(isString).To(BeTrue())

		expected := "---\ntask_identifier: " + id + "\n" + strings.TrimPrefix(fixture, "---\n")
		Expect(string(lastWritten)).To(Equal(expected),
			"injection must receive the unmodified original read bytes")
		Expect(strings.Contains(captured, "replacing invalid task_identifier")).To(BeFalse())
		Expect(strings.Contains(captured, "replacing non-UUID task_identifier")).To(BeFalse())
	})

	It("AC4: healthy files are never written", func() {
		ctx := context.Background()
		tmpDir, err := os.MkdirTemp("", "scanner-spec008-ac4-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(os.RemoveAll(tmpDir)).To(Succeed()) }()

		fixture := "---\ntask_identifier: 55555555-5555-4555-8555-555555555555\nstatus: in_progress\nassignee: claude\n---\nbody\n"
		Expect(os.WriteFile(filepath.Join(tmpDir, "ac4.md"), []byte(fixture), 0600)).To(Succeed())

		var writeCount int
		nonNilTasks := 0
		v := &vaultScanner{
			metrics: metrics.New(),
			hashes:  make(map[string]fileEntry),
			ops: fileOps{
				readFile: func(_ context.Context, p string) ([]byte, error) {
					return os.ReadFile(filepath.Join(tmpDir, p)) // #nosec G304 -- test-only path
				},
				writeFile: func(_ context.Context, _ string, _ []byte) error {
					writeCount++
					return nil
				},
			},
			autoInject: true,
		}

		captured := captureGlogWarnings(func() {
			for i := 0; i < 5; i++ {
				task, _, werr := v.processFile(ctx, "ac4.md")
				Expect(werr).To(BeFalse())
				if task != nil {
					nonNilTasks++
				}
			}
		})

		Expect(writeCount).To(Equal(0))
		onDisk, err := os.ReadFile(filepath.Join(tmpDir, "ac4.md")) // #nosec G304 -- test-only path
		Expect(err).NotTo(HaveOccurred())
		Expect(string(onDisk)).To(Equal(fixture))
		Expect(
			nonNilTasks,
		).To(Equal(1), "healthy file must publish exactly once across five cycles")
		Expect(strings.Count(captured, "replacing invalid task_identifier")).To(Equal(0))
		Expect(strings.Count(captured, "replacing non-UUID task_identifier")).To(Equal(0))
	})

	It("AC5: content outside the frontmatter region is preserved byte-for-byte", func() {
		ctx := context.Background()
		tmpDir, err := os.MkdirTemp("", "scanner-spec008-ac5-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(os.RemoveAll(tmpDir)).To(Succeed()) }()

		body := "# Runbook\n\n```\ntask_identifier: 501\n```\n"
		fixture := "---\ntask_identifier: 501\nstatus: in_progress\n---\n" + body
		Expect(os.WriteFile(filepath.Join(tmpDir, "ac5.md"), []byte(fixture), 0600)).To(Succeed())

		var writeCount int
		v := &vaultScanner{
			metrics: metrics.New(),
			hashes:  make(map[string]fileEntry),
			ops: fileOps{
				readFile: func(_ context.Context, p string) ([]byte, error) {
					return os.ReadFile(filepath.Join(tmpDir, p)) // #nosec G304 -- test-only path
				},
				writeFile: func(_ context.Context, relPath string, content []byte) error {
					writeCount++
					return os.WriteFile(filepath.Join(tmpDir, relPath), content, 0600)
				},
			},
			autoInject: true,
		}

		_, _, werr := v.processFile(ctx, "ac5.md")
		Expect(werr).To(BeFalse())
		Expect(writeCount).To(Equal(1))

		finalBytes, err := os.ReadFile(
			filepath.Join(tmpDir, "ac5.md"),
		) // #nosec G304 -- test-only path
		Expect(err).NotTo(HaveOccurred())

		Expect(extractBody(finalBytes)).To(Equal(extractBody([]byte(fixture))),
			"fence markers and the body's own task_identifier line must survive byte-for-byte")
		Expect(countLinesMatching(finalBytes, regexp.MustCompile(`^task_identifier:`))).To(Equal(2),
			"one frontmatter occurrence plus one body occurrence")

		fm, err := extractFrontmatter(ctx, finalBytes)
		Expect(err).NotTo(HaveOccurred())
		var fmMap map[string]interface{}
		Expect(yaml.Unmarshal([]byte(fm), &fmMap)).To(Succeed())
		idStr, isString := fmMap["task_identifier"].(string)
		Expect(isString).To(BeTrue())
		_, parseErr := uuid.Parse(idStr)
		Expect(parseErr).NotTo(HaveOccurred())
	})

	It("AC5b: frontmatter outside the removed key is preserved byte-for-byte", func() {
		ctx := context.Background()
		tmpDir, err := os.MkdirTemp("", "scanner-spec008-ac5b-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(os.RemoveAll(tmpDir)).To(Succeed()) }()

		fixture := "---\n# managed by vault-cli\ntask_identifier: 501\nstatus: in_progress\nassignee: claude\ndue: 2026-09-05\npage_count: \"07\"\n---\nbody\n"
		Expect(os.WriteFile(filepath.Join(tmpDir, "ac5b.md"), []byte(fixture), 0600)).To(Succeed())

		var writeCount int
		v := &vaultScanner{
			metrics: metrics.New(),
			hashes:  make(map[string]fileEntry),
			ops: fileOps{
				readFile: func(_ context.Context, p string) ([]byte, error) {
					return os.ReadFile(filepath.Join(tmpDir, p)) // #nosec G304 -- test-only path
				},
				writeFile: func(_ context.Context, relPath string, content []byte) error {
					writeCount++
					return os.WriteFile(filepath.Join(tmpDir, relPath), content, 0600)
				},
			},
			autoInject: true,
		}

		_, _, werr := v.processFile(ctx, "ac5b.md")
		Expect(werr).To(BeFalse())
		Expect(writeCount).To(Equal(1))

		finalBytes, err := os.ReadFile(
			filepath.Join(tmpDir, "ac5b.md"),
		) // #nosec G304 -- test-only path
		Expect(err).NotTo(HaveOccurred())

		fm, err := extractFrontmatter(ctx, finalBytes)
		Expect(err).NotTo(HaveOccurred())
		var fmMap map[string]interface{}
		Expect(yaml.Unmarshal([]byte(fm), &fmMap)).To(Succeed())
		idStr, isString := fmMap["task_identifier"].(string)
		Expect(isString).To(BeTrue())
		_, parseErr := uuid.Parse(idStr)
		Expect(parseErr).NotTo(HaveOccurred())

		expected := "---\ntask_identifier: " + idStr + "\n" + strings.Replace(
			strings.TrimPrefix(fixture, "---\n"),
			"task_identifier: 501\n",
			"",
			1,
		)
		Expect(string(finalBytes)).To(Equal(expected),
			"comment, status, assignee, due, and quoted page_count must survive byte-for-byte")
		Expect(
			countLinesMatching(finalBytes, regexp.MustCompile(`^# managed by vault-cli`)),
		).To(Equal(1))
		Expect(countLinesMatching(finalBytes, regexp.MustCompile(`^due: 2026-09-05$`))).To(Equal(1))
		Expect(
			countLinesMatching(finalBytes, regexp.MustCompile(`^page_count: "07"$`)),
		).To(Equal(1))
	})

	It("AC6: repair is greppable from logs alone", func() {
		ctx := context.Background()
		tmpDir, err := os.MkdirTemp("", "scanner-spec008-ac6-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(os.RemoveAll(tmpDir)).To(Succeed()) }()

		// Each row carries the exact log line it must emit. The non-string
		// class keeps the frozen `replacing invalid task_identifier` substring
		// (Go type only, so a big sequence cannot blow up a log line); the
		// string-but-not-a-UUID class emits `replacing non-UUID
		// task_identifier` with the offending value, which %T would have lost.
		rows := []struct {
			relPath  string
			fm       string
			expected string
		}{
			{
				"ac6-int.md",
				"task_identifier: 501",
				"replacing invalid task_identifier of type int in ac6-int.md",
			},
			{
				"ac6-empty.md",
				`task_identifier: ""`,
				`replacing non-UUID task_identifier "" in ac6-empty.md`,
			},
			{
				"ac6-quoted.md",
				`task_identifier: "501"`,
				`replacing non-UUID task_identifier "501" in ac6-quoted.md`,
			},
		}
		for _, row := range rows {
			Expect(
				os.WriteFile(filepath.Join(tmpDir, row.relPath), []byte(ac2Content(row.fm)), 0600),
			).To(Succeed())
		}

		writeCount := 0
		v := &vaultScanner{
			metrics: metrics.New(),
			hashes:  make(map[string]fileEntry),
			ops: fileOps{
				readFile: func(_ context.Context, p string) ([]byte, error) {
					return os.ReadFile(filepath.Join(tmpDir, p)) // #nosec G304 -- test-only path
				},
				writeFile: func(_ context.Context, relPath string, content []byte) error {
					writeCount++
					return os.WriteFile(filepath.Join(tmpDir, relPath), content, 0600)
				},
			},
			autoInject: true,
		}

		captured := captureGlogWarnings(func() {
			for _, row := range rows {
				_, _, werr := v.processFile(ctx, row.relPath)
				Expect(werr).To(BeFalse(), row.relPath)
			}
		})

		Expect(writeCount).To(Equal(3))
		// Both substrings stay greppable, and together they still account for
		// exactly one repair log per repaired file.
		Expect(strings.Count(captured, "replacing invalid task_identifier")).To(Equal(1),
			"one repair log for the non-string row")
		Expect(strings.Count(captured, "replacing non-UUID task_identifier")).To(Equal(2),
			"one repair log per string-but-invalid row")
		for _, row := range rows {
			Expect(strings.Contains(captured, row.expected)).To(BeTrue(), row.relPath)
		}
	})

	It("AC7: AUTO_INJECT_TASK_IDENTIFIER=false skips all 16 shapes without writing", func() {
		ctx := context.Background()
		tmpDir, err := os.MkdirTemp("", "scanner-spec008-ac7-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(os.RemoveAll(tmpDir)).To(Succeed()) }()

		for _, fx := range ac2Fixtures {
			Expect(
				os.WriteFile(
					filepath.Join(tmpDir, fx.relPath),
					[]byte(ac2Content(fx.frontmatter)),
					0600,
				),
			).To(Succeed())
		}

		before := counterValue(metrics.ReasonAutoInjectDisabled)
		writeCount := 0
		v := &vaultScanner{
			metrics: metrics.New(),
			hashes:  make(map[string]fileEntry),
			ops: fileOps{
				readFile: func(_ context.Context, p string) ([]byte, error) {
					return os.ReadFile(filepath.Join(tmpDir, p)) // #nosec G304 -- test-only path
				},
				writeFile: func(_ context.Context, _ string, _ []byte) error {
					writeCount++
					return nil
				},
			},
			autoInject: false,
		}

		captured := captureGlogWarnings(func() {
			for _, fx := range ac2Fixtures {
				for i := 0; i < 5; i++ {
					_, _, werr := v.processFile(ctx, fx.relPath)
					Expect(werr).To(BeFalse(), fx.label)
				}
			}
		})

		Expect(writeCount).To(Equal(0))
		Expect(counterValue(metrics.ReasonAutoInjectDisabled)).To(Equal(before + 16*5))
		Expect(
			strings.Contains(
				captured,
				"AUTO_INJECT_TASK_IDENTIFIER=false; skipping task without valid task_identifier:",
			),
		).To(BeTrue())
		Expect(strings.Contains(captured, "replacing invalid task_identifier")).To(BeFalse())
		Expect(strings.Contains(captured, "replacing non-UUID task_identifier")).To(BeFalse())
	})
})
