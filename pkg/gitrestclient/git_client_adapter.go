// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gitrestclient

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// readinessAttempts and readinessAttemptTimeout bound the startup probe, so a
// genuinely dead endpoint still fails the process instead of hanging it.
//
// The budget is sized against two hard limits. The controller serves no /healthz
// until EnsureCloned returns — the router is built later in Run — so the liveness
// probe (initialDelaySeconds 10, periodSeconds 10, failureThreshold 5) kills the
// container at ~50 s. And the vault pod's measured cold start is 23 s, which the
// probe must be able to outlast: 5 attempts with 2s/4s/8s/16s backoff put the last
// attempt at ~38 s, while the per-attempt timeout stops a blackholed endpoint from
// stretching a single attempt to the HTTP client's 30 s.
const (
	readinessAttempts       = 5
	readinessAttemptTimeout = 2 * time.Second
)

// NewGitClient creates a GitClient backed by git-rest HTTP calls.
// basePath is the logical root path used when computing relative paths
// (e.g. "/data/vault") — it does NOT need to exist on disk.
// gitRestClient is the HTTP client created by NewGitRestClient.
func NewGitClient(gitRestClient GitRestClient, basePath string) GitClient {
	return newGitClientWithBackoff(gitRestClient, basePath, exponentialBackoff)
}

func newGitClientWithBackoff(
	gitRestClient GitRestClient,
	basePath string,
	backoff func(attempt int) time.Duration,
) GitClient {
	return &gitRestGitClientAdapter{
		client:   gitRestClient,
		basePath: strings.TrimRight(basePath, "/"),
		backoff:  backoff,
	}
}

type gitRestGitClientAdapter struct {
	client   GitRestClient
	basePath string
	backoff  func(attempt int) time.Duration
}

// Compile-time assertion — catches missing methods if the GitClient interface evolves.
var _ GitClient = (*gitRestGitClientAdapter)(nil)

// EnsureCloned probes git-rest readiness at startup, retrying a transient refusal.
//
// A single probe used to be fatal, so a refusal that clears on its own — the vault
// listener not yet accepting, or the new pod's route not yet programmed — exited the
// process. Each exit cost a Sentry event and left the controller dispatching nothing
// until its next restart.
func (a *gitRestGitClientAdapter) EnsureCloned(ctx context.Context) error {
	var lastErr error
	for attempt := 1; attempt <= readinessAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return errors.Wrapf(
					ctx,
					ctx.Err(),
					"probe git-rest readiness cancelled during backoff",
				)
			case <-time.After(a.backoff(attempt)):
			}
		}
		ready, err := a.probeReadiness(ctx)
		if err == nil && ready {
			if attempt > 1 {
				// Unconditional: the only signal separating a transient refusal
				// from a permanent one, and the one an operator reads after a rollout.
				glog.Infof(
					"git-rest readiness confirmed on attempt %d of %d",
					attempt,
					readinessAttempts,
				)
			} else {
				glog.V(2).Infof("git-rest readiness confirmed")
			}
			return nil
		}
		lastErr = err
		failure := "git-rest not ready (503)"
		if err != nil {
			failure = err.Error()
		}
		glog.Warningf(
			"git-rest readiness probe failed on attempt %d of %d: %s",
			attempt,
			readinessAttempts,
			failure,
		)
	}
	if lastErr != nil {
		return errors.Wrapf(ctx, lastErr, "probe git-rest readiness")
	}
	return errors.Errorf(
		ctx,
		"git-rest is not ready (503) after %d attempts; check vault-obsidian-openclaw pod status",
		readinessAttempts,
	)
}

// probeReadiness runs one readiness call under its own deadline, so a blackholed
// endpoint cannot spend the whole retry budget on a single attempt.
func (a *gitRestGitClientAdapter) probeReadiness(ctx context.Context) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, readinessAttemptTimeout)
	defer cancel()
	return a.client.IsReady(probeCtx)
}

// Pull is a no-op — git-rest manages its own pulls internally.
func (a *gitRestGitClientAdapter) Pull(ctx context.Context) error {
	glog.V(4).Infof("gitRestGitClientAdapter.Pull: no-op (git-rest handles pull internally)")
	return nil
}

// CommitAndPush is a no-op — git-rest auto-commits on every Post/Delete.
func (a *gitRestGitClientAdapter) CommitAndPush(ctx context.Context, message string) error {
	glog.V(4).Infof("gitRestGitClientAdapter.CommitAndPush: no-op (git-rest auto-commits)")
	return nil
}

// Path returns the logical base path. Note: this path does NOT exist on disk when
// using the gitrest adapter — callers must use ReadFile/WriteFile/ListFiles instead
// of direct filesystem operations.
func (a *gitRestGitClientAdapter) Path() string {
	return a.basePath
}

// AtomicWriteAndCommitPush writes content to absPath via git-rest POST.
// Atomicity relies on per-task Kafka partitioning (no local mutex).
func (a *gitRestGitClientAdapter) AtomicWriteAndCommitPush(
	ctx context.Context,
	absPath string,
	content []byte,
	message string,
) error {
	rel, err := filepath.Rel(a.basePath, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return errors.Errorf(ctx, "absPath %q is not under basePath %q", absPath, a.basePath)
	}
	glog.V(3).Infof("gitrest: POST %s (%s)", rel, message)
	return a.client.Post(ctx, rel, content)
}

// AtomicWriteIfAbsentAndCommitPush writes content to absPath via git-rest
// create-only POST (?create_only=1). git-rest answers 409 Conflict for an
// occupied path, mapped to ErrAlreadyExists; nothing is written in that case.
// Atomicity relies on per-task Kafka partitioning (no local mutex).
func (a *gitRestGitClientAdapter) AtomicWriteIfAbsentAndCommitPush(
	ctx context.Context,
	absPath string,
	content []byte,
	message string,
) error {
	rel, err := filepath.Rel(a.basePath, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return errors.Errorf(ctx, "absPath %q is not under basePath %q", absPath, a.basePath)
	}
	glog.V(3).Infof("gitrest: POST (create-only) %s (%s)", rel, message)
	return a.client.PostIfAbsent(ctx, rel, content)
}

// AtomicReadModifyWriteAndCommitPush reads absPath, calls modify, then writes the result via git-rest.
// Atomicity relies on per-task Kafka partitioning (no local mutex).
func (a *gitRestGitClientAdapter) AtomicReadModifyWriteAndCommitPush(
	ctx context.Context,
	absPath string,
	modify func(current []byte) ([]byte, error),
	message string,
) error {
	rel, err := filepath.Rel(a.basePath, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return errors.Errorf(ctx, "absPath %q is not under basePath %q", absPath, a.basePath)
	}
	current, err := a.client.Get(ctx, rel)
	if err != nil {
		return errors.Wrapf(ctx, err, "read file before modify %s", rel)
	}
	updated, err := modify(current)
	if err != nil {
		return errors.Wrapf(ctx, err, "modify func failed for %s", rel)
	}
	if bytes.Equal(updated, current) {
		// An unchanged file produces no commit, so the POST would only spend a
		// git-rest write+push round trip (~2.7 s p50 on openclaw, 2026-10-21) for nothing.
		glog.V(2).Infof("gitrest: skip unchanged read-modify-write %s (%s)", rel, message)
		return nil
	}
	glog.V(3).Infof("gitrest: POST (read-modify-write) %s (%s)", rel, message)
	return errors.Wrapf(ctx, a.client.Post(ctx, rel, updated), "write after modify %s", rel)
}

// ListFiles delegates to GitRestClient.List.
func (a *gitRestGitClientAdapter) ListFiles(ctx context.Context, glob string) ([]string, error) {
	return a.client.List(ctx, glob)
}

// ReadFile delegates to GitRestClient.Get.
func (a *gitRestGitClientAdapter) ReadFile(ctx context.Context, relPath string) ([]byte, error) {
	content, err := a.client.Get(ctx, relPath)
	return content, errors.Wrapf(ctx, err, "read file %s", relPath)
}

// WriteFile delegates to GitRestClient.Post.
func (a *gitRestGitClientAdapter) WriteFile(
	ctx context.Context,
	relPath string,
	content []byte,
) error {
	return errors.Wrapf(ctx, a.client.Post(ctx, relPath, content), "write file %s", relPath)
}
