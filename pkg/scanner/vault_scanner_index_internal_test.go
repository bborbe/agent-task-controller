// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

// This spec file is internal (package scanner, not scanner_test) because the
// multimap case has to seed the scanner's unexported bookkeeping (hashes) and then
// drive the real publishIndex: the duplicate case it covers is unreachable through
// a plain RunCycle, because processFile repairs a duplicate it processes. The
// external test package's doubles live in scanner_test and are not visible here,
// so the doubles below are hand-written.

import (
	"context"
	"os"
	"path/filepath"
	"time"

	lib "github.com/bborbe/agent"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	gitclient "github.com/bborbe/agent-task-controller/pkg/gitrestclient"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
)

// indexGitClient is a gitclient.GitClient with real file I/O over a tmpdir and a read
// counter, so a spec can prove a resolver lookup issues no read. It embeds
// convergenceGitClient for the methods the index specs never exercise.
type indexGitClient struct {
	*convergenceGitClient
	readFileCalls int
}

var _ gitclient.GitClient = (*indexGitClient)(nil)

func (c *indexGitClient) ListFiles(_ context.Context, glob string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(c.path, glob))
	if err != nil {
		return nil, err
	}
	rel := make([]string, 0, len(matches))
	for _, m := range matches {
		r, relErr := filepath.Rel(c.path, m)
		if relErr != nil {
			return nil, relErr
		}
		rel = append(rel, r)
	}
	return rel, nil
}

func (c *indexGitClient) ReadFile(_ context.Context, relPath string) ([]byte, error) {
	c.readFileCalls++
	return os.ReadFile(filepath.Join(c.path, relPath)) // #nosec G304 -- test-only path
}

func (c *indexGitClient) WriteFile(_ context.Context, relPath string, content []byte) error {
	return os.WriteFile(
		filepath.Join(c.path, relPath),
		content,
		0600,
	) // #nosec G306 -- test-only file
}

const (
	alphaID = "11111111-1111-4111-8111-111111111111"
	betaID  = "22222222-2222-4222-8222-222222222222"
	dupID   = "33333333-3333-4333-8333-333333333333"
)

var _ = Describe("vaultScanner identifier index", func() {
	var (
		ctx     context.Context
		dir     string
		client  *indexGitClient
		results chan ScanResult
		s       *vaultScanner
		r       ScanResult
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		dir, err = os.MkdirTemp("", "scanner-index-test-*")
		Expect(err).NotTo(HaveOccurred())
		client = &indexGitClient{convergenceGitClient: &convergenceGitClient{path: dir}}
		results = make(chan ScanResult, 1)
		var ok bool
		s, ok = NewGitRestVaultScanner(
			client, ".", time.Hour, nil, metrics.New(), true,
		).(*vaultScanner)
		Expect(ok).To(BeTrue())
	})

	AfterEach(func() {
		Expect(os.RemoveAll(dir)).To(Succeed())
	})

	write := func(name, content string) {
		Expect(os.WriteFile(filepath.Join(dir, name), []byte(content), 0600)).To(Succeed())
	}

	cycle := func() {
		s.RunCycle(ctx, results)
		Expect(results).To(Receive(&r))
	}

	taskFile := func(id string) string {
		return "---\ntask_identifier: " + id +
			"\nstatus: in_progress\nassignee: claude\n---\nbody\n"
	}

	It("builds the identifier to path mapping from a completed scan cycle", func() {
		write("alpha.md", taskFile(alphaID))
		write("beta.md", taskFile(betaID))
		cycle()

		path, found, err := s.Resolve(ctx, lib.TaskIdentifier(alphaID))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(path).To(Equal("alpha.md"))

		path, found, err = s.Resolve(ctx, lib.TaskIdentifier(betaID))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(path).To(Equal("beta.md"))
	})

	It("reports an unknown identifier as absent, not as an error", func() {
		write("alpha.md", taskFile(alphaID))
		cycle()

		path, found, err := s.Resolve(ctx, lib.TaskIdentifier(betaID))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		Expect(path).To(Equal(""))
	})

	It("never indexes the empty identifier a halted repair stores", func() {
		write("halted.md", "---\n{task_identifier: 501, status: in_progress}\n---\nbody\n")
		write("alpha.md", taskFile(alphaID))
		cycle()

		Expect(s.hashes["halted.md"].taskIdentifier).To(Equal(lib.TaskIdentifier("")))
		Expect(s.hashes["alpha.md"].taskIdentifier).To(Equal(lib.TaskIdentifier(alphaID)))
		Expect(s.index).NotTo(HaveKey(lib.TaskIdentifier("")))
		Expect(s.index).To(HaveLen(1))

		before := client.readFileCalls
		path, found, err := s.Resolve(ctx, lib.TaskIdentifier(""))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		Expect(path).To(Equal(""))
		Expect(client.readFileCalls).To(Equal(before))

		path, found, err = s.Resolve(ctx, lib.TaskIdentifier(alphaID))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(path).To(Equal("alpha.md"))
		Expect(client.readFileCalls).To(Equal(before))
	})

	It("rebuilds the index per cycle instead of accumulating stale entries", func() {
		write("mover.md", taskFile(alphaID))
		cycle()
		path, found, err := s.Resolve(ctx, lib.TaskIdentifier(alphaID))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(path).To(Equal("mover.md"))

		Expect(os.Remove(filepath.Join(dir, "mover.md"))).To(Succeed())
		cycle()
		path, found, err = s.Resolve(ctx, lib.TaskIdentifier(alphaID))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		Expect(path).To(Equal(""))

		write("newhome.md", taskFile(alphaID))
		cycle()
		path, found, err = s.Resolve(ctx, lib.TaskIdentifier(alphaID))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(path).To(Equal("newhome.md"))
	})

	It(
		"preserves duplicate identifiers and reports two paths for one identifier as an error",
		func() {
			s.hashes = map[string]fileEntry{
				"first.md":  {taskIdentifier: lib.TaskIdentifier(dupID)},
				"second.md": {taskIdentifier: lib.TaskIdentifier(dupID)},
			}
			s.publishIndex()

			path, found, err := s.Resolve(ctx, lib.TaskIdentifier(dupID))
			Expect(found).To(BeFalse())
			Expect(path).To(Equal(""))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("duplicate task_identifier " + dupID))
			Expect(err.Error()).To(ContainSubstring("first.md"))
			Expect(err.Error()).To(ContainSubstring("second.md"))
		},
	)

	// Requires ENABLE_RACE=true to be meaningful: Makefile.precommit sets
	// TESTFLAGS_RACE = -race=false by default and CI runs only `make precommit`, so
	// in CI this is a plain concurrency smoke test. The real detector run — and the
	// evidence spec 016's race AC names — is
	// `ENABLE_RACE=true go test -race -count=1 ./pkg/scanner/... ./pkg/result/...`.
	// A green CI run is therefore not race coverage.
	It("stays clean under the race detector when scanning and resolving concurrently", func() {
		write("alpha.md", taskFile(alphaID))
		cycle()

		done := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			defer close(done)
			for i := 0; i < 50; i++ {
				s.RunCycle(ctx, results)
				<-results
			}
		}()

		for i := 0; i < 50; i++ {
			_, found, resolveErr := s.Resolve(ctx, lib.TaskIdentifier(alphaID))
			Expect(resolveErr).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
		}

		Eventually(done).Should(BeClosed())

		path, found, err := s.Resolve(ctx, lib.TaskIdentifier(alphaID))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(path).To(Equal("alpha.md"))
	})
})
