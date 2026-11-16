// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("countParkedProbes", func() {
	const (
		parkedPath     = "probe-pi-headless-prod.md"
		terminalPath   = "probe-claude-headless-dev.md"
		advancingPath  = "probe-a2a-worker-dev.md"
		unreadablePath = "probe-broken-dev.md"
		otherTaskType  = "probe-not-a-healthcheck.md"
		unprefixedPath = "Healthcheck trading-agent - 2026-11-02.md"
	)

	// probeFile renders the frontmatter the gauge reads. Only these four keys matter:
	// the predicate is task_type + phase + status + current_job, and a body is carried
	// only so the fixture looks like a real probe record.
	probeFile := func(taskType, phase, status, currentJob string) string {
		return "---\n" +
			"task_type: " + taskType + "\n" +
			"phase: " + phase + "\n" +
			"status: " + status + "\n" +
			"current_job: \"" + currentJob + "\"\n" +
			"---\n\nreply 'ok'\n"
	}

	newScanner := func(files map[string]string) *vaultScanner {
		return &vaultScanner{
			ops: fileOps{
				readFile: func(_ context.Context, relPath string) ([]byte, error) {
					content, ok := files[relPath]
					if !ok {
						return nil, os.ErrNotExist
					}
					return []byte(content), nil
				},
			},
		}
	}

	It("counts exactly the parked record out of parked, terminal, advancing and unparseable", func() {
		// The four-record matrix the acceptance criterion names. It fails on a gauge
		// that blanks when one file will not parse, and on a gauge that counts a
		// terminal or a running record.
		v := newScanner(map[string]string{
			parkedPath:     probeFile("healthcheck", "human_review", "hold", ""),
			terminalPath:   probeFile("healthcheck", "done", "completed", ""),
			advancingPath:  probeFile("healthcheck", "planning", "in_progress", "claude-headless-771a0318"),
			unreadablePath: "no frontmatter at all\n",
		})
		Expect(v.countParkedProbes(
			context.Background(),
			[]string{parkedPath, terminalPath, advancingPath, unreadablePath},
		)).To(Equal(1))
	})

	It("counts a probe parked at hold, the shape a re-drive cap produces", func() {
		v := newScanner(map[string]string{
			parkedPath: probeFile("healthcheck", "planning", "hold", ""),
		})
		Expect(v.countParkedProbes(context.Background(), []string{parkedPath})).To(Equal(1))
	})

	It("counts a probe parked at a non-terminal phase with status in_progress", func() {
		v := newScanner(map[string]string{
			parkedPath: probeFile("healthcheck", "execution", "in_progress", ""),
		})
		Expect(v.countParkedProbes(context.Background(), []string{parkedPath})).To(Equal(1))
	})

	It("does not count a probe whose job is running", func() {
		v := newScanner(map[string]string{
			parkedPath: probeFile("healthcheck", "planning", "in_progress", "claude-headless-771a0318"),
		})
		Expect(v.countParkedProbes(context.Background(), []string{parkedPath})).To(Equal(0))
	})

	It("does not count a probe at a terminal phase", func() {
		v := newScanner(map[string]string{
			parkedPath: probeFile("healthcheck", "done", "in_progress", ""),
		})
		Expect(v.countParkedProbes(context.Background(), []string{parkedPath})).To(Equal(0))
	})

	It("does not count a probe with a terminal status and a non-terminal phase", func() {
		for _, status := range []string{"completed", "aborted"} {
			v := newScanner(map[string]string{
				parkedPath: probeFile("healthcheck", "planning", status, ""),
			})
			Expect(v.countParkedProbes(context.Background(), []string{parkedPath})).To(Equal(0),
				"status %q must not count", status)
		}
	})

	It("does not count a record whose task_type is not healthcheck", func() {
		v := newScanner(map[string]string{
			otherTaskType: probeFile("llm", "planning", "in_progress", ""),
		})
		Expect(v.countParkedProbes(context.Background(), []string{otherTaskType})).To(Equal(0))
	})

	It("skips files whose name does not carry the probe prefix", func() {
		v := newScanner(map[string]string{
			parkedPath:     probeFile("healthcheck", "planning", "hold", ""),
			unprefixedPath: probeFile("healthcheck", "planning", "hold", ""),
		})
		Expect(v.countParkedProbes(
			context.Background(),
			[]string{parkedPath, unprefixedPath},
		)).To(Equal(1))
	})

	It("skips an unreadable file instead of failing the count", func() {
		v := newScanner(map[string]string{
			parkedPath: probeFile("healthcheck", "planning", "hold", ""),
		})
		Expect(v.countParkedProbes(
			context.Background(),
			[]string{parkedPath, unreadablePath},
		)).To(Equal(1))
	})

	It("counts every parked probe across the vault", func() {
		v := newScanner(map[string]string{
			parkedPath:    probeFile("healthcheck", "planning", "hold", ""),
			terminalPath:  probeFile("healthcheck", "planning", "hold", ""),
			advancingPath: probeFile("healthcheck", "human_review", "hold", ""),
		})
		Expect(v.countParkedProbes(
			context.Background(),
			[]string{parkedPath, terminalPath, advancingPath},
		)).To(Equal(3))
	})
})
