// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"
	"strings"
	"time"

	lib "github.com/bborbe/agent"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"

	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

// transitionPriorTyped aborts one still-open prior through the typed client.
//
// It writes the frozen four-field supersede transition — status, phase,
// completed_date, superseded_by — and, only when the caller says the prior came
// from the recurring publisher, created_by. The field NAMES are a contract with
// the vault consumer surfaces (spec 002, "Frozen frontmatter field names") and
// are deliberately unchanged by this port.
//
// superseded_by keeps carrying a vault-relative PATH even though the write is
// addressed by identifier: specs 002 and 004 freeze it as the new instance's
// relPath and the vault reads it as one. The path is never read back from the
// API — the create already knows the title the server used, so the caller
// derives it exactly as it did before.
//
// Best-effort like its git-rest predecessor: a write error is logged and
// swallowed, and the already-created task is never rolled back.
func transitionPriorTyped(
	ctx context.Context,
	client vaultapi.Client,
	currentDateTime libtime.CurrentDateTimeGetter,
	prior vaultapi.Task,
	newRelPath string,
	taskIdentifier lib.TaskIdentifier,
	setCreatedBy bool,
) {
	fields := map[string]any{
		"status":         "aborted",
		"phase":          "done",
		"completed_date": currentDateTime.Now().UTC().Format(time.RFC3339),
		"superseded_by":  newRelPath,
	}
	if setCreatedBy {
		fields["created_by"] = "recurring-task-creator"
	}
	if _, err := client.PatchTask(ctx, prior.ID, fields); err != nil {
		glog.Warningf(
			"auto-supersede: write prior %s failed for %s: %v",
			prior.Title,
			taskIdentifier,
			err,
		)
		return
	}
	glog.V(2).Infof(
		"auto-supersede: %s -> %s for %s",
		prior.Title,
		newRelPath,
		taskIdentifier,
	)
}

// hasTitlePrefix reports whether a task's title starts with prefix. Every
// supersede scan filters on it in memory because the typed listing has no title
// parameter — and adding one would reintroduce the path-shaped addressing this
// migration removes.
func hasTitlePrefix(t vaultapi.Task, prefix string) bool {
	return strings.HasPrefix(t.Title, prefix)
}

// frontmatterString reads one string field from a task's frontmatter, treating a
// missing or non-string value as empty.
func frontmatterString(t vaultapi.Task, key string) string {
	v, _ := t.Frontmatter[key].(string)
	return v
}
