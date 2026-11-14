// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	lib "github.com/bborbe/agent"
	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	libtime "github.com/bborbe/time"
	libtimemocks "github.com/bborbe/time/mocks"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/mocks"
	"github.com/bborbe/agent-task-controller/pkg/command"
	"github.com/bborbe/agent-task-controller/pkg/metrics"
	"github.com/bborbe/agent-task-controller/pkg/result"
	"github.com/bborbe/agent-task-controller/pkg/scanner"
)

// This file drives the create executor against the REAL identifier→path index of a
// trigger-only scanner (POLL_INTERVAL=0s), which builds the index once at boot and
// never rebuilds it. It pins the 2026-10-27 incident: identifier 9371a3d2 was carried
// by an aborted file indexed at boot and by a live file a later create wrote. The
// index kept only the aborted path, so the executor's spawn write landed on the
// aborted file instead of refusing.
var _ = Describe("NewCreateTaskExecutor index bookkeeping on a duplicated identifier", func() {
	const (
		duplicatedID = "9371a3d2-0000-4000-8000-000000000001"
		oldTitle     = "Aborted Update - 38c900d"
		newTitle     = "Live Update - fb971cc"
	)

	var (
		ctx       context.Context
		cancel    context.CancelFunc
		tmpDir    string
		taskDir   string
		fakeGit   *mocks.GitClient
		fakeVault *mocks.VaultAPIClient
		clock     *libtimemocks.CurrentDateTimeGetter
		resolver  scanner.VaultScanner

		failReadMutex sync.Mutex
		failReadOnce  string
		// failReadAlwaysPath fails every read of that path, modelling a git-rest
		// that stays unavailable instead of answering once.
		failReadAlwaysPath string
	)

	oldPath := func() string { return filepath.Join(taskDir, oldTitle+".md") }
	newPath := func() string { return filepath.Join(taskDir, newTitle+".md") }

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		var err error
		tmpDir, err = os.MkdirTemp("", "create-task-index-duplicate-test-*")
		Expect(err).NotTo(HaveOccurred())
		taskDir = "tasks"
		Expect(os.MkdirAll(filepath.Join(tmpDir, taskDir), 0750)).To(Succeed())

		Expect(os.WriteFile(
			filepath.Join(tmpDir, oldPath()),
			[]byte(
				"---\ntask_identifier: "+duplicatedID+"\nassignee: claude\nstatus: aborted\n---\nbody\n",
			),
			0600,
		)).To(Succeed())

		failReadOnce = ""
		failReadAlwaysPath = ""
		fakeGit = &mocks.GitClient{}
		fakeGit.PathReturns(tmpDir)
		fakeGit.ListFilesStub = func(_ context.Context, glob string) ([]string, error) {
			matches, globErr := filepath.Glob(filepath.Join(tmpDir, glob))
			if globErr != nil {
				return nil, globErr
			}
			rel := make([]string, 0, len(matches))
			for _, m := range matches {
				r, relErr := filepath.Rel(tmpDir, m)
				if relErr != nil {
					return nil, relErr
				}
				rel = append(rel, r)
			}
			return rel, nil
		}
		fakeGit.ReadFileStub = func(_ context.Context, relPath string) ([]byte, error) {
			failReadMutex.Lock()
			fail := failReadOnce == relPath || failReadAlwaysPath == relPath
			if failReadOnce == relPath {
				failReadOnce = ""
			}
			failReadMutex.Unlock()
			if fail {
				return nil, errors.New("GET " + relPath + " returned 503: unavailable")
			}
			content, readErr := os.ReadFile(
				filepath.Join(tmpDir, relPath),
			) // #nosec G304 -- test-only path
			if readErr != nil {
				return nil, errors.New("GET " + relPath + " returned 404: not found")
			}
			return content, nil
		}

		clock = &libtimemocks.CurrentDateTimeGetter{}
		clock.NowReturns(libtime.DateTime(time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)))
		fakeVault = newFSTaskClient(tmpDir, taskDir)

		// Trigger-only boot: Run builds the index once and then only waits on a
		// trigger nothing sends.
		resolver = scanner.NewGitRestVaultScanner(
			fakeGit, taskDir, 0, make(chan struct{}), metrics.New(), true, "openclaw", 0,
		)
		go func() {
			defer GinkgoRecover()
			Expect(resolver.Run(ctx, make(chan scanner.ScanResult, 1))).To(Succeed())
		}()
		Eventually(func() string {
			path, _, _ := resolver.Resolve(ctx, lib.TaskIdentifier(duplicatedID))
			return path
		}).Should(Equal(oldPath()))
	})

	AfterEach(func() {
		cancel()
		Expect(os.RemoveAll(tmpDir)).To(Succeed())
	})

	createCmd := func() cdb.CommandObject {
		event, err := base.ParseEvent(ctx, task.CreateCommand{
			TaskIdentifier: lib.TaskIdentifier(duplicatedID),
			Title:          newTitle,
			Frontmatter:    lib.TaskFrontmatter{"assignee": "claude", "status": "in_progress"},
			// The create path refuses an empty TargetVault; stamp the vault as a
			// real producer does.
			TargetVault: "openclaw",
		})
		Expect(err).NotTo(HaveOccurred())
		return cdb.CommandObject{
			Command: base.Command{
				RequestID: base.NewRequestID(),
				Operation: task.CreateCommandOperation,
				Initiator: "test",
				Data:      event,
			},
			SchemaID: cdb.SchemaID{Group: "agent", Kind: "task", Version: "v1"},
		}
	}

	// The two specs that used to live here — "records the second file beside the
	// indexed one" and "keeps the index entry when the indexed file cannot be
	// read" — both drove their scenario by failing the REDIRECT's read of the
	// indexed file. The redirect no longer reads a file: it asks the API for the
	// identifier, and that answer is authoritative, so "the redirect could not
	// verify the occupant" is no longer a reachable state and neither spec has a
	// trigger left. The duplicate-detection path they exercised is unchanged and
	// still covered by the `index recording` specs in
	// task_create_task_executor_test.go, including the read-failure case for the
	// BOOKKEEPING read, which is the read that still exists.

	It("replaces an index entry whose file no longer carries the identifier", func() {
		// The indexed file was rewritten to carry another identifier after the boot
		// build. Trigger-only mode never rebuilds, so recording the new file beside
		// it would be a false duplicate that blocks every write for the pod's life.
		Expect(os.WriteFile(
			filepath.Join(tmpDir, oldPath()),
			[]byte(
				"---\ntask_identifier: 9371a3d2-0000-4000-8000-0000000000ff\nstatus: aborted\n---\nbody\n",
			),
			0600,
		)).To(Succeed())
		executor := command.NewCreateTaskExecutor(
			fakeVault, taskDir, "openclaw", clock, testK, resolver,
		)

		_, _, err := executor.HandleCommand(ctx, nil, createCmd())
		Expect(err).NotTo(HaveOccurred())

		path, found, resolveErr := resolver.Resolve(ctx, lib.TaskIdentifier(duplicatedID))
		Expect(resolveErr).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(path).To(Equal(newPath()))

		writePath, _, findErr := result.FindTaskFilePath(
			ctx, fakeGit, taskDir, lib.TaskIdentifier(duplicatedID), resolver,
		)
		Expect(findErr).NotTo(HaveOccurred())
		Expect(writePath).To(Equal(newPath()))
	})
})
