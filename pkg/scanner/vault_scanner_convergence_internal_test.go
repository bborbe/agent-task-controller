// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"regexp"

	lib "github.com/bborbe/agent"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"gopkg.in/yaml.v3"

	gitclient "github.com/bborbe/agent-task-controller/pkg/gitrestclient"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
)

// convergenceGitClient is a no-op gitclient.GitClient so RunCycle-driven specs can
// observe ScanResult. CommitAndPush is counted so a spec can prove a halted repair
// committed nothing as well as writing nothing.
type convergenceGitClient struct {
	path        string
	commitCount int
}

var _ gitclient.GitClient = (*convergenceGitClient)(nil)

func (t *convergenceGitClient) EnsureCloned(_ context.Context) error { return nil }

func (t *convergenceGitClient) Pull(_ context.Context) error { return nil }

func (t *convergenceGitClient) Path() string { return t.path }

func (t *convergenceGitClient) CommitAndPush(_ context.Context, _ string) error {
	t.commitCount++
	return nil
}

func (t *convergenceGitClient) AtomicWriteAndCommitPush(
	_ context.Context,
	_ string,
	_ []byte,
	_ string,
) error {
	return nil
}

func (t *convergenceGitClient) AtomicWriteIfAbsentAndCommitPush(
	_ context.Context,
	_ string,
	_ []byte,
	_ string,
) error {
	return nil
}

func (t *convergenceGitClient) AtomicReadModifyWriteAndCommitPush(
	_ context.Context,
	_ string,
	_ func([]byte) ([]byte, error),
	_ string,
) error {
	return nil
}

func (t *convergenceGitClient) ListFiles(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (t *convergenceGitClient) ReadFile(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}

func (t *convergenceGitClient) WriteFile(_ context.Context, _ string, _ []byte) error {
	return nil
}

// convergenceHarness wires a vaultScanner over a real tmpdir with write-counting
// file ops, driven through RunCycle so ScanResult is directly observable.
type convergenceHarness struct {
	dir        string
	scanner    *vaultScanner
	git        *convergenceGitClient
	writeCount int
	results    chan ScanResult
}

func newConvergenceHarness(dir string, autoInject bool) *convergenceHarness {
	h := &convergenceHarness{
		dir:     dir,
		git:     &convergenceGitClient{path: dir},
		results: make(chan ScanResult, 1),
	}
	h.scanner = &vaultScanner{
		gitClient: h.git,
		taskDir:   ".",
		hashes:    make(map[string]fileEntry),
		metrics:   metrics.New(),
		ops: fileOps{
			listFiles: func(_ context.Context, glob string) ([]string, error) {
				matches, err := filepath.Glob(filepath.Join(dir, glob))
				if err != nil {
					return nil, err
				}
				rel := make([]string, 0, len(matches))
				for _, m := range matches {
					r, relErr := filepath.Rel(dir, m)
					if relErr != nil {
						continue
					}
					rel = append(rel, r)
				}
				return rel, nil
			},
			readFile: func(_ context.Context, p string) ([]byte, error) {
				return os.ReadFile(filepath.Join(dir, p)) // #nosec G304 -- test-only path
			},
			writeFile: func(_ context.Context, p string, content []byte) error {
				h.writeCount++
				return os.WriteFile(filepath.Join(dir, p), content, 0600)
			},
		},
		autoInject: autoInject,
	}
	return h
}

// runCycles runs n RunCycle calls, draining the buffered results channel after each
// so the scanner's non-blocking send never drops a result, and returns every
// ScanResult in order.
func (h *convergenceHarness) runCycles(ctx context.Context, n int) []ScanResult {
	out := make([]ScanResult, 0, n)
	for i := 0; i < n; i++ {
		h.scanner.RunCycle(ctx, h.results)
		var r ScanResult
		Expect(h.results).To(Receive(&r))
		out = append(out, r)
	}
	return out
}

// skipCounterValue reads the current value of the skipped-files counter for reason
// from the default registry.
func skipCounterValue(reason metrics.SkipReason) float64 {
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

// convergenceKeyLineRe counts surviving task_identifier key lines in a repaired
// file, including the quoted and spaced key spellings. Declared here rather than
// reused from production so the assertion does not move if the production regex does.
var convergenceKeyLineRe = regexp.MustCompile(
	`^[[:space:]]*['"]?task_identifier['"]?[[:space:]]*:`,
)

// haltLogAnywhereRe is the greppable halt substring, used for the negative-evidence
// specs where the expected count is zero.
var haltLogAnywhereRe = regexp.MustCompile(`task_identifier repair did not converge`)

// haltLogFor builds the ERROR-level, path-specific halt log matcher. glog prefixes
// error lines with E, so anchoring on ^E also proves the severity.
func haltLogFor(relPath string) *regexp.Regexp {
	return regexp.MustCompile(
		`^E.*task_identifier repair did not converge, halting repair for: ` +
			regexp.QuoteMeta(relPath) + `$`,
	)
}

// convergenceFixtureA — escaped-underscore key spelling. yaml.v3 resolves the
// double-quoted escape to the key task_identifier, so processFile routes the file
// into the present-but-invalid repair branch, but taskIdentifierKeyLine matches only
// the literal spellings, so removeTaskIdentifier is a no-op. Injection then prepends
// a fresh UUID and DeduplicateFrontmatter's last-wins resolves task_identifier back
// to the integer 501 — the original accumulator loop, at flat file size, reachable in
// v0.7.4. Widening the removal regex to chase this spelling is explicitly NOT this
// spec's job (spec Constraints); the guard bounds it instead.
const convergenceFixtureA = "---\n\"task\\u005fidentifier\": 501\nstatus: in_progress\n---\nbody\n"

// convergenceFixtureB — flow-mapping frontmatter. removeTaskIdentifier does not match
// a line beginning "{", and the injected line makes the candidate frontmatter
// unparseable: DeduplicateFrontmatter fails with "could not find expected ':'".
// Non-convergence via the fail-closed parse path.
const convergenceFixtureB = "---\n{task_identifier: 501, status: in_progress}\n---\nbody\n"

var convergenceAC2Fixtures = []struct {
	relPath string
	content string
}{
	{"ac2-a-escaped-key.md", convergenceFixtureA},
	{"ac2-b-flow-map.md", convergenceFixtureB},
}

// convergenceBlockStyleFixtures re-declare the spec-008 ac2Fixtures rows 10-12
// frontmatter strings literally. They are NOT imported from that table, so the
// guard proves it stays silent on the shapes without coupling to the other spec.
var convergenceBlockStyleFixtures = []struct {
	relPath     string
	frontmatter string
}{
	{"ac6-10-block-seq.md", "task_identifier:\n  - a\n  - b"},
	{"ac6-11-block-map.md", "task_identifier:\n  a: b"},
	{"ac6-12-block-scalar.md", "task_identifier: |\n  abc"},
}

var _ = Describe("task_identifier repair convergence guard (spec 009)", func() {
	ctx := context.Background()

	It(
		"AC2: refuses a non-converging repair with zero writes, one ERROR log, one counter increment",
		func() {
			for _, fx := range convergenceAC2Fixtures {
				dir, err := os.MkdirTemp("", "scanner-spec009-ac2-*")
				Expect(err).NotTo(HaveOccurred())
				Expect(
					os.WriteFile(filepath.Join(dir, fx.relPath), []byte(fx.content), 0600),
				).To(Succeed())
				h := newConvergenceHarness(dir, true)

				before := skipCounterValue(metrics.ReasonRepairNotConverging)
				var results []ScanResult
				captured := captureGlogWarnings(func() {
					results = h.runCycles(ctx, 5)
				})

				Expect(h.writeCount).To(Equal(0), fx.relPath)
				Expect(h.git.commitCount).To(Equal(0), fx.relPath)
				onDisk, err := os.ReadFile(
					filepath.Join(dir, fx.relPath),
				) // #nosec G304 -- test-only path
				Expect(err).NotTo(HaveOccurred(), fx.relPath)
				Expect(string(onDisk)).To(Equal(fx.content), fx.relPath)
				Expect(
					countLinesMatching([]byte(captured), haltLogFor(fx.relPath)),
				).To(Equal(1), fx.relPath)
				Expect(
					skipCounterValue(metrics.ReasonRepairNotConverging)-before,
				).To(Equal(1.0), fx.relPath)
				for _, r := range results {
					Expect(r.Changed).To(BeEmpty(), fx.relPath)
				}

				Expect(os.RemoveAll(dir)).To(Succeed())
			}
		},
	)

	It("AC3: the halt self-clears when the file content changes", func() {
		dir, err := os.MkdirTemp("", "scanner-spec009-ac3-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(os.RemoveAll(dir)).To(Succeed()) }()
		relPath := "ac3.md"
		Expect(
			os.WriteFile(filepath.Join(dir, relPath), []byte(convergenceFixtureA), 0600),
		).To(Succeed())
		h := newConvergenceHarness(dir, true)

		// Reach the halted state: five cycles refuse the repair, zero writes.
		captureGlogWarnings(func() {
			h.runCycles(ctx, 5)
		})
		Expect(h.writeCount).To(Equal(0))

		// The re-arm is content-keyed: no time-based, cycle-count-based, or
		// process-global flag participates in the decision. The only signal is the
		// on-disk hash differing from the halted state's stored hash.
		repairable := "---\ntask_identifier: 501\nstatus: in_progress\n---\nbody\n"
		Expect(os.WriteFile(filepath.Join(dir, relPath), []byte(repairable), 0600)).To(Succeed())
		before := skipCounterValue(metrics.ReasonRepairNotConverging)
		captured := captureGlogWarnings(func() {
			h.runCycles(ctx, 1)
		})

		Expect(h.writeCount).To(Equal(1))
		finalBytes, err := os.ReadFile(
			filepath.Join(dir, relPath),
		) // #nosec G304 -- test-only path
		Expect(err).NotTo(HaveOccurred())
		Expect(countLinesMatching(finalBytes, convergenceKeyLineRe)).To(Equal(1))
		fm, err := extractFrontmatter(ctx, finalBytes)
		Expect(err).NotTo(HaveOccurred())
		var fmMap map[string]interface{}
		Expect(yaml.Unmarshal([]byte(fm), &fmMap)).To(Succeed())
		idStr, isString := fmMap["task_identifier"].(string)
		Expect(isString).To(BeTrue())
		_, parseErr := uuid.Parse(idStr)
		Expect(parseErr).NotTo(HaveOccurred())
		Expect(
			skipCounterValue(metrics.ReasonRepairNotConverging) - before,
		).To(Equal(0.0))
		Expect(countLinesMatching([]byte(captured), haltLogAnywhereRe)).To(Equal(0))
	})

	It("AC4: a halted file never emits an empty identifier on delete", func() {
		dir, err := os.MkdirTemp("", "scanner-spec009-ac4-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(os.RemoveAll(dir)).To(Succeed()) }()
		relPath := "ac4.md"
		Expect(
			os.WriteFile(filepath.Join(dir, relPath), []byte(convergenceFixtureA), 0600),
		).To(Succeed())
		h := newConvergenceHarness(dir, true)

		captureGlogWarnings(func() {
			h.runCycles(ctx, 5)
		})
		Expect(h.writeCount).To(Equal(0))

		Expect(os.Remove(filepath.Join(dir, relPath))).To(Succeed())
		results := h.runCycles(ctx, 1)

		Expect(results[0].Deleted).NotTo(ContainElement(lib.TaskIdentifier("")))
		Expect(h.writeCount).To(Equal(0))
	})

	It("AC5: stays silent on a healthy file with a valid UUID", func() {
		dir, err := os.MkdirTemp("", "scanner-spec009-ac5-healthy-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(os.RemoveAll(dir)).To(Succeed()) }()
		relPath := "ac5-healthy.md"
		fixture := "---\ntask_identifier: 3f2b1c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d\n" +
			"status: in_progress\nassignee: claude\n---\nbody\n"
		Expect(os.WriteFile(filepath.Join(dir, relPath), []byte(fixture), 0600)).To(Succeed())
		h := newConvergenceHarness(dir, true)

		before := skipCounterValue(metrics.ReasonRepairNotConverging)
		captured := captureGlogWarnings(func() {
			h.runCycles(ctx, 5)
		})

		Expect(h.writeCount).To(Equal(0))
		Expect(
			skipCounterValue(metrics.ReasonRepairNotConverging) - before,
		).To(Equal(0.0))
		Expect(countLinesMatching([]byte(captured), haltLogAnywhereRe)).To(Equal(0))
	})

	It("AC5: stays silent when auto-inject is disabled, re-skipping every cycle", func() {
		dir, err := os.MkdirTemp("", "scanner-spec009-ac5-disabled-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(os.RemoveAll(dir)).To(Succeed()) }()
		relPath := "ac5-disabled.md"
		Expect(
			os.WriteFile(filepath.Join(dir, relPath), []byte(convergenceFixtureA), 0600),
		).To(Succeed())
		h := newConvergenceHarness(dir, false)

		before := skipCounterValue(metrics.ReasonRepairNotConverging)
		beforeDisabled := skipCounterValue(metrics.ReasonAutoInjectDisabled)
		captured := captureGlogWarnings(func() {
			h.runCycles(ctx, 5)
		})

		Expect(h.writeCount).To(Equal(0))
		Expect(
			skipCounterValue(metrics.ReasonRepairNotConverging) - before,
		).To(Equal(0.0))
		Expect(countLinesMatching([]byte(captured), haltLogAnywhereRe)).To(Equal(0))
		// auto_inject_disabled increments 5 times, not 1: the auto-inject gate returns
		// before any hash entry is stored, so processFile's content-hash short-circuit
		// never engages and every cycle re-skips.
		Expect(
			skipCounterValue(metrics.ReasonAutoInjectDisabled) - beforeDisabled,
		).To(Equal(5.0))
	})

	It("AC5: converges a key-absent file in exactly one write", func() {
		dir, err := os.MkdirTemp("", "scanner-spec009-ac5-keyabsent-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(os.RemoveAll(dir)).To(Succeed()) }()
		relPath := "ac5-key-absent.md"
		fixture := "---\nstatus: in_progress\nassignee: claude\n---\nbody\n"
		Expect(os.WriteFile(filepath.Join(dir, relPath), []byte(fixture), 0600)).To(Succeed())
		h := newConvergenceHarness(dir, true)

		before := skipCounterValue(metrics.ReasonRepairNotConverging)
		captured := captureGlogWarnings(func() {
			h.runCycles(ctx, 5)
		})

		Expect(h.writeCount).To(Equal(1))
		Expect(
			skipCounterValue(metrics.ReasonRepairNotConverging) - before,
		).To(Equal(0.0))
		Expect(countLinesMatching([]byte(captured), haltLogAnywhereRe)).To(Equal(0))

		finalBytes, err := os.ReadFile(
			filepath.Join(dir, relPath),
		) // #nosec G304 -- test-only path
		Expect(err).NotTo(HaveOccurred())
		Expect(countLinesMatching(finalBytes, convergenceKeyLineRe)).To(Equal(1))
		fm, err := extractFrontmatter(ctx, finalBytes)
		Expect(err).NotTo(HaveOccurred())
		var fmMap map[string]interface{}
		Expect(yaml.Unmarshal([]byte(fm), &fmMap)).To(Succeed())
		idStr, isString := fmMap["task_identifier"].(string)
		Expect(isString).To(BeTrue())
		_, parseErr := uuid.Parse(idStr)
		Expect(parseErr).NotTo(HaveOccurred())
	})

	It("AC6: stays silent on the spec-008 block-style shapes, which converge in one write", func() {
		for _, fx := range convergenceBlockStyleFixtures {
			dir, err := os.MkdirTemp("", "scanner-spec009-ac6-*")
			Expect(err).NotTo(HaveOccurred())
			fixture := "---\n" + fx.frontmatter + "\nstatus: in_progress\nassignee: claude\n---\nbody\n"
			Expect(
				os.WriteFile(filepath.Join(dir, fx.relPath), []byte(fixture), 0600),
			).To(Succeed())
			h := newConvergenceHarness(dir, true)

			before := skipCounterValue(metrics.ReasonRepairNotConverging)
			captured := captureGlogWarnings(func() {
				h.runCycles(ctx, 5)
			})

			Expect(h.writeCount).To(Equal(1), fx.relPath)
			Expect(
				skipCounterValue(metrics.ReasonRepairNotConverging)-before,
			).To(Equal(0.0), fx.relPath)
			Expect(countLinesMatching([]byte(captured), haltLogAnywhereRe)).To(Equal(0), fx.relPath)

			finalBytes, err := os.ReadFile(
				filepath.Join(dir, fx.relPath),
			) // #nosec G304 -- test-only path
			Expect(err).NotTo(HaveOccurred(), fx.relPath)
			Expect(countLinesMatching(finalBytes, convergenceKeyLineRe)).To(Equal(1), fx.relPath)
			fm, err := extractFrontmatter(ctx, finalBytes)
			Expect(err).NotTo(HaveOccurred(), fx.relPath)
			var fmMap map[string]interface{}
			Expect(yaml.Unmarshal([]byte(fm), &fmMap)).To(Succeed(), fx.relPath)
			idStr, isString := fmMap["task_identifier"].(string)
			Expect(isString).To(BeTrue(), fx.relPath)
			_, parseErr := uuid.Parse(idStr)
			Expect(parseErr).NotTo(HaveOccurred(), fx.relPath)

			Expect(os.RemoveAll(dir)).To(Succeed())
		}
	})

	It("repairConverges fails closed on every defensive branch", func() {
		id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		// Extract failure: no frontmatter delimiter at all.
		Expect(repairConverges(ctx, []byte("no frontmatter at all"), id)).To(BeFalse())
		// Dedup failure: the injected line followed by a flow mapping is unparseable
		// (fixture B's post-injection candidate).
		Expect(
			repairConverges(
				ctx,
				[]byte(
					"---\ntask_identifier: "+id+
						"\n{task_identifier: 501, status: in_progress}\n---\nbody\n",
				),
				id,
			),
		).To(BeFalse())
		// Unmarshal failure: a scalar-only frontmatter parses as a node but not as a map.
		Expect(repairConverges(ctx, []byte("---\nhello\n---\nbody\n"), id)).To(BeFalse())
		// Key absent after a clean parse.
		Expect(
			repairConverges(ctx, []byte("---\nstatus: in_progress\n---\nbody\n"), id),
		).To(BeFalse())
		// Key present but not a string.
		Expect(
			repairConverges(ctx, []byte("---\ntask_identifier: 501\n---\nbody\n"), id),
		).To(BeFalse())
		// Convergent: string equal to the freshly minted id.
		Expect(
			repairConverges(ctx, []byte("---\ntask_identifier: "+id+"\n---\nbody\n"), id),
		).To(BeTrue())
		// String present but not equal to the freshly minted id.
		Expect(
			repairConverges(
				ctx,
				[]byte("---\ntask_identifier: "+id+"\n---\nbody\n"),
				"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
			),
		).To(BeFalse())
	})
})
