// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package routing decides whether a task controller should process a
// given CreateCommand based on the command's target vault and the
// controller's configured VAULT_NAME.
package routing

import (
	"context"
	"regexp"

	lib "github.com/bborbe/agent"
	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/errors"
	"github.com/bborbe/validation"
)

// LegacyDefaultVault is the vault a controller acts on when a command
// leaves its TargetVault empty. Hard-coded per spec 044; do not make configurable.
const LegacyDefaultVault = "openclaw"

// targetVaultSlugRegexp must stay in sync with the same regex on
// task.CreateCommand.Validate (lib/command/task/create-command.go).
var targetVaultSlugRegexp = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ValidateVaultName returns a wrapped validation error when vaultName is empty
// or does not match the slug regex ^[a-z][a-z0-9-]*$.
func ValidateVaultName(ctx context.Context, vaultName string) error {
	if vaultName == "" {
		return errors.Wrap(ctx, validation.Error, "VAULT_NAME must not be empty")
	}
	if !targetVaultSlugRegexp.MatchString(vaultName) {
		return errors.Wrapf(
			ctx,
			validation.Error,
			"VAULT_NAME %q must match ^[a-z][a-z0-9-]*$",
			vaultName,
		)
	}
	return nil
}

// ShouldProcess returns true iff the controller's vaultName owns this command.
// An empty cmd.TargetVault falls back to LegacyDefaultVault (spec 044 AC 12).
// A non-empty cmd.TargetVault is compared verbatim (no case-folding).
func ShouldProcess(cmd task.CreateCommand, vaultName string) bool {
	effective := cmd.TargetVault
	if effective == "" {
		effective = LegacyDefaultVault
	}
	return effective == vaultName
}

// ShouldProcessResult returns true iff the controller's vaultName owns this
// result. The result's frontmatter carries target_vault — stamped at task
// create (buildCreateTaskContent) and echoed back by the agent when it
// publishes. A result whose target_vault differs from vaultName is cross-vault
// traffic (both controllers consume the shared topic) and must be skipped
// before the write scan so the owning vault alone writes it. An absent
// target_vault (legacy task created before stamping) falls through to true:
// the write path's not-found handling covers genuinely-missing tasks.
func ShouldProcessResult(req lib.Task, vaultName string) bool {
	if v, ok := req.Frontmatter.String("target_vault"); ok {
		return v == vaultName
	}
	return true
}
