// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package routing_test

import (
	"context"

	lib "github.com/bborbe/agent"
	task "github.com/bborbe/agent/command/task"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/routing"
)

var _ = Describe("ShouldProcess", func() {
	DescribeTable(
		"routing matrix",
		func(cmdTargetVault, vaultName string, want bool) {
			cmd := task.CreateCommand{
				TaskIdentifier: lib.TaskIdentifier("task-1"),
				Title:          "T",
				Frontmatter:    lib.TaskFrontmatter{"status": "next"},
				TargetVault:    cmdTargetVault,
			}
			Expect(routing.ShouldProcess(cmd, vaultName)).To(Equal(want))
		},
		// (cmd empty, my openclaw) → true (legacy fallback to openclaw)
		Entry("empty target, vaultName=openclaw → true (legacy fallback)", "", "openclaw", true),
		// (cmd openclaw, my openclaw) → true
		Entry("openclaw target, vaultName=openclaw → true", "openclaw", "openclaw", true),
		// (cmd personal, my personal) → true
		Entry("personal target, vaultName=personal → true", "personal", "personal", true),
		// (cmd empty, my personal) → false (legacy fallback is openclaw, not personal)
		Entry(
			"empty target, vaultName=personal → false (legacy is openclaw)",
			"",
			"personal",
			false,
		),
		// (cmd openclaw, my personal) → false
		Entry("openclaw target, vaultName=personal → false", "openclaw", "personal", false),
		// (cmd other, my openclaw) → false
		Entry("other target, vaultName=openclaw → false", "other", "openclaw", false),
	)
})

var _ = Describe("ShouldProcessResult", func() {
	DescribeTable(
		"result routing matrix",
		func(targetVault, vaultName string, want bool) {
			req := lib.Task{
				TaskIdentifier: lib.TaskIdentifier("task-1"),
				Frontmatter:    lib.TaskFrontmatter{},
				Content:        lib.TaskContent("## Result\n"),
			}
			if targetVault != "<absent>" {
				req.Frontmatter["target_vault"] = targetVault
			}
			Expect(routing.ShouldProcessResult(req, vaultName)).To(Equal(want))
		},
		// (result openclaw, my personal) → false (cross-vault traffic, skip)
		Entry("openclaw target_vault, vaultName=personal → false", "openclaw", "personal", false),
		// (result personal, my personal) → true
		Entry("personal target_vault, vaultName=personal → true", "personal", "personal", true),
		// (result personal, my openclaw) → false
		Entry("personal target_vault, vaultName=openclaw → false", "personal", "openclaw", false),
		// (absent target_vault, my personal) → true (legacy fallback — not-found handling covers missing)
		Entry(
			"absent target_vault, vaultName=personal → true (legacy fallback)",
			"<absent>",
			"personal",
			true,
		),
	)
})

var _ = Describe("ShouldProcessFrontmatterCommand", func() {
	DescribeTable(
		"frontmatter-command routing matrix",
		func(targetVault, vaultName string, want bool) {
			Expect(routing.ShouldProcessFrontmatterCommand(targetVault, vaultName)).To(Equal(want))
		},
		// empty targetVault falls through (legacy unstamped command — owner heals, non-owner drops)
		Entry("empty target, vaultName=openclaw → true (fall through)", "", "openclaw", true),
		Entry("empty target, vaultName=personal → true (fall through)", "", "personal", true),
		// (target openclaw, my openclaw) → true
		Entry("openclaw target, vaultName=openclaw → true", "openclaw", "openclaw", true),
		// (target personal, my personal) → true
		Entry("personal target, vaultName=personal → true", "personal", "personal", true),
		// (target openclaw, my personal) → false (cross-vault traffic)
		Entry("openclaw target, vaultName=personal → false", "openclaw", "personal", false),
		// (target personal, my openclaw) → false
		Entry("personal target, vaultName=openclaw → false", "personal", "openclaw", false),
		// (target other, my openclaw) → false
		Entry("other target, vaultName=openclaw → false", "other", "openclaw", false),
	)
})

var _ = Describe("ValidateVaultName", func() {
	var ctx context.Context
	BeforeEach(func() { ctx = context.Background() })

	It("rejects empty VAULT_NAME", func() {
		err := routing.ValidateVaultName(ctx, "")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("VAULT_NAME"))
	})

	It("rejects invalid slug 'Bad'", func() {
		err := routing.ValidateVaultName(ctx, "Bad")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("VAULT_NAME"))
		Expect(err.Error()).To(ContainSubstring("^[a-z][a-z0-9-]*$"))
	})

	It("accepts openclaw", func() {
		Expect(routing.ValidateVaultName(ctx, "openclaw")).To(Succeed())
	})

	It("accepts personal", func() {
		Expect(routing.ValidateVaultName(ctx, "personal")).To(Succeed())
	})
})
