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
	notifcmd "github.com/bborbe/notification/command/notification"
	libtime "github.com/bborbe/time"
	libtimemocks "github.com/bborbe/time/mocks"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/result"
)

// The assertions in this file read the raw captured message string. They must never
// decode the URI first: Go's query decoder turns "+" back into a space, so a spec that
// parsed the deeplink would pass against the buggy form and prove nothing.
var _ = Describe("resultWriter escalation deeplink encoding", func() {
	const (
		deeplinkTaskIdentifier = "deeplink-task-uuid"
		deeplinkTaskContent    = "---\ntask_identifier: deeplink-task-uuid\n" +
			"status: in_progress\nphase: execution\nretry_count: 3\nmax_retries: 3\n" +
			"assignee: claude\n---\n## Result\nStatus: failed\n"

		// The six encoding inputs.
		spacedTaskName  = "PR Review github - bborbe-pr-review-fixtures - 2 - 03448af5 - retry-288998c8"
		plainTaskName   = "Update-Go-bborbe-vault-cli-f9b19bd"
		emDashTaskName  = "Em Dash — Task"
		plusTaskName    = "Fix plus+encoded task"
		nestedTaskName  = "Nested Task"
		spacedVaultName = "my vault"
	)

	var (
		ctx      context.Context
		tmpDir   string
		fakeGit  *mocks.GitClient
		fakeTime *libtimemocks.CurrentDateTimeGetter
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tmpDir, err = os.MkdirTemp("", "result-writer-deeplink-*")
		Expect(err).NotTo(HaveOccurred())

		fakeGit = &mocks.GitClient{}
		fakeGit.PathReturns(tmpDir)
		fakeGit.ListFilesStub = func(_ context.Context, glob string) ([]string, error) {
			matches, globErr := filepath.Glob(filepath.Join(tmpDir, glob))
			if globErr != nil {
				return nil, globErr
			}
			var rel []string
			for _, m := range matches {
				r, _ := filepath.Rel(tmpDir, m)
				rel = append(rel, r)
			}
			return rel, nil
		}
		fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
			return os.ReadFile(filepath.Join(tmpDir, relPath)) // #nosec G304 -- test-only path
		}
		fakeGit.AtomicReadModifyWriteAndCommitPushStub = func(
			_ context.Context,
			absPath string,
			modify func([]byte) ([]byte, error),
			_ string,
		) error {
			current, readErr := os.ReadFile(absPath) // #nosec G304 -- test helper
			if readErr != nil {
				return readErr
			}
			updated, modifyErr := modify(current)
			if modifyErr != nil {
				return modifyErr
			}
			return os.WriteFile(absPath, updated, 0600) // #nosec G306 -- test helper
		}

		fakeTime = &libtimemocks.CurrentDateTimeGetter{}
		fakeTime.NowReturns(libtime.DateTime(time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)))
	})

	// publishMessage writes the escalation fixture into taskDir and returns the one
	// published message, read as a raw string.
	publishMessage := func(taskDir, vaultName, taskName string) string {
		Expect(os.MkdirAll(filepath.Join(tmpDir, taskDir), 0750)).To(Succeed())
		Expect(
			os.WriteFile(
				filepath.Join(tmpDir, taskDir, taskName+".md"),
				[]byte(deeplinkTaskContent),
				0600,
			),
		).To(Succeed())

		var published []notifcmd.NotificationPublishCommand
		writer := result.NewResultWriter(
			fakeGit,
			taskDir,
			vaultName,
			fakeTime,
			metrics.New(),
			libtime.NewWaiterDuration(),
			notifcmd.NotificationPublishCommandSenderFunc(
				func(_ context.Context, command notifcmd.NotificationPublishCommand) error {
					published = append(published, command)
					return nil
				},
			),
		)

		Expect(writer.WriteResult(ctx, lib.Task{
			TaskIdentifier: lib.TaskIdentifier(deeplinkTaskIdentifier),
			Frontmatter: lib.TaskFrontmatter{
				"task_identifier": deeplinkTaskIdentifier,
				"status":          "in_progress",
				"phase":           "execution",
				"retry_count":     3,
				"max_retries":     3,
				"assignee":        "claude",
			},
			Content: lib.TaskContent("## Result\nStatus: failed\n"),
		})).To(Succeed())

		Expect(published).To(HaveLen(1), "the fixture must escalate exactly once")
		return string(published[0].Message)
	}

	DescribeTable(
		"encodes a space as %20 and never as +",
		func(taskDir, vaultName, taskName string, wantContains, wantAbsent []string) {
			message := publishMessage(taskDir, vaultName, taskName)

			for _, want := range wantContains {
				Expect(message).To(ContainSubstring(want))
			}
			for _, absent := range wantAbsent {
				Expect(message).NotTo(ContainSubstring(absent))
			}
		},
		Entry(
			"a name containing spaces",
			"tasks",
			"openclaw",
			spacedTaskName,
			[]string{
				"obsidian://open?vault=openclaw&file=tasks%2FPR%20Review%20github%20-%20",
			},
			[]string{"+"},
		),
		Entry(
			"a name containing no spaces",
			"tasks",
			"openclaw",
			plainTaskName,
			[]string{
				"obsidian://open?vault=openclaw&file=tasks%2FUpdate-Go-bborbe-vault-cli-f9b19bd",
			},
			[]string{"+", "%20"},
		),
		Entry(
			"a name containing an em dash",
			"tasks",
			"openclaw",
			emDashTaskName,
			[]string{
				"obsidian://open?vault=openclaw&file=tasks%2FEm%20Dash%20%E2%80%94%20Task",
			},
			[]string{"+"},
		),
		Entry(
			"a name containing a literal plus",
			"tasks",
			"openclaw",
			plusTaskName,
			[]string{
				"obsidian://open?vault=openclaw&file=tasks%2FFix%20plus%2Bencoded%20task",
			},
			[]string{"+"},
		),
		Entry(
			"a nested vault-relative path",
			"25 Tasks/nested",
			"openclaw",
			nestedTaskName,
			[]string{
				"obsidian://open?vault=openclaw&file=25%20Tasks%2Fnested%2FNested%20Task",
			},
			[]string{"+"},
		),
		Entry(
			"a vault name containing a space",
			"tasks",
			spacedVaultName,
			"Spaced Vault Task",
			[]string{
				"obsidian://open?vault=my%20vault&file=tasks%2FSpaced%20Vault%20Task",
			},
			[]string{"+"},
		),
	)
})
