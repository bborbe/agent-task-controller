// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gitrestclient

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// NewGitClient creates a GitClient backed by git-rest HTTP calls.
// basePath is the logical root path used when computing relative paths
// (e.g. "/data/vault") — it does NOT need to exist on disk.
// gitRestClient is the HTTP client created by NewGitRestClient.
func NewGitClient(gitRestClient GitRestClient, basePath string) GitClient {
	return &gitRestGitClientAdapter{
		client:   gitRestClient,
		basePath: strings.TrimRight(basePath, "/"),
	}
}

type gitRestGitClientAdapter struct {
	client   GitRestClient
	basePath string
}

// Compile-time assertion — catches missing methods if the GitClient interface evolves.
var _ GitClient = (*gitRestGitClientAdapter)(nil)

// EnsureCloned probes git-rest readiness at startup.
func (a *gitRestGitClientAdapter) EnsureCloned(ctx context.Context) error {
	ready, err := a.client.IsReady(ctx)
	if err != nil {
		return errors.Wrapf(ctx, err, "probe git-rest readiness")
	}
	if !ready {
		return errors.Errorf(
			ctx,
			"git-rest is not ready (503); check vault-obsidian-openclaw pod status",
		)
	}
	glog.V(2).Infof("git-rest readiness confirmed")
	return nil
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
