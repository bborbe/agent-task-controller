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

// VerdictNoOwningController is the single token a controller logs when it
// declines a create whose target_vault it does not serve.
//
// A create is broadcast, so every controller sees it. When one of them serves
// the target it claims the command and materializes the task; when none does,
// every controller declines it. Logging the same token on every decline is what
// turns "no controller claimed this" into one greppable state instead of two
// silent ones — the skip that used to sit at V(2), and the exists-at-title-path
// failure on the controller the command was misrouted to. The token names the
// task, so an operator reading it knows exactly which record is stuck.
const VerdictNoOwningController = "stuck: no owning controller"

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

// ValidateTargetVault returns a validation error when a create command's
// TargetVault is empty or does not match the vault slug regex. Callers must run
// it BEFORE ShouldProcess: an empty target would otherwise resolve to
// LegacyDefaultVault and a malformed one would be dropped as cross-vault
// traffic — both silent, and both the deadlock the create guard exists to
// remove.
func ValidateTargetVault(ctx context.Context, targetVault string) error {
	if targetVault == "" {
		return errors.Wrapf(
			ctx,
			validation.Error,
			"target_vault must not be empty; the producer must stamp the owning vault",
		)
	}
	if !targetVaultSlugRegexp.MatchString(targetVault) {
		return errors.Wrapf(
			ctx,
			validation.Error,
			"target_vault %q must match ^[a-z][a-z0-9-]*$",
			targetVault,
		)
	}
	return nil
}

// ShouldProcess returns true iff the controller's vaultName owns this command.
// An empty cmd.TargetVault falls back to LegacyDefaultVault (spec 044 AC 12);
// the create path rejects an empty target via ValidateTargetVault before
// calling this, so the fallback is reachable only from a caller that skipped
// that guard. A non-empty cmd.TargetVault is compared verbatim (no
// case-folding).
func ShouldProcess(cmd task.CreateCommand, vaultName string) bool {
	effective := cmd.TargetVault
	if effective == "" {
		effective = LegacyDefaultVault
	}
	return effective == vaultName
}

// ShouldProcessResult returns true iff the controller's vaultName owns this
// result. The result's frontmatter carries target_vault — stamped by the typed
// create and echoed back by the agent when it publishes. A result whose
// target_vault differs from vaultName is cross-vault traffic (both controllers
// consume the shared topic) and must be skipped before the write scan so the
// owning vault alone writes it. An absent
// target_vault (legacy task created before stamping) falls through to true:
// the write path's not-found handling covers genuinely-missing tasks.
func ShouldProcessResult(req lib.Task, vaultName string) bool {
	if v, ok := req.Frontmatter.String("target_vault"); ok {
		return v == vaultName
	}
	return true
}

// ShouldProcessFrontmatterCommand returns true iff the controller's vaultName
// owns this frontmatter command (update-frontmatter, increment-frontmatter,
// complete). An empty targetVault falls through to true — matching
// ShouldProcessResult's legacy semantics — so legacy unstamped commands are
// attempted by every controller: the owning vault finds the file and heals it
// (stamping target_vault in the same write), the non-owning vault drops it. A
// non-empty targetVault is compared verbatim to vaultName (no case-folding).
// Deliberately NOT defaulting to LegacyDefaultVault (unlike ShouldProcess):
// routing legacy personal-vault tasks to the openclaw controller would
// permanently orphan them.
func ShouldProcessFrontmatterCommand(targetVault, vaultName string) bool {
	if targetVault == "" {
		return true
	}
	return targetVault == vaultName
}
