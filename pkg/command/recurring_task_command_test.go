// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	lib "github.com/bborbe/agent"
	task "github.com/bborbe/agent/command/task"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/command"
)

var _ = Describe("isRecurringTaskCommand", func() {
	cmdWith := func(frontmatter lib.TaskFrontmatter) task.CreateCommand {
		return task.CreateCommand{
			TaskIdentifier: lib.TaskIdentifier("t"),
			Title:          "t",
			Frontmatter:    frontmatter,
		}
	}

	It("treats a recurring-task-creator command as recurring", func() {
		Expect(command.IsRecurringTaskCommand(cmdWith(lib.TaskFrontmatter{
			"created_by": "recurring-task-creator",
		}))).To(BeTrue())
	})

	It("treats a command carrying a recurring: key as recurring", func() {
		Expect(command.IsRecurringTaskCommand(cmdWith(lib.TaskFrontmatter{
			"recurring": "go-version:go1.27.0",
		}))).To(BeTrue())
	})

	It("does not require created_by for the recurring: key", func() {
		Expect(command.IsRecurringTaskCommand(cmdWith(lib.TaskFrontmatter{
			"recurring":  "go-version:go1.27.0",
			"created_by": "go-version-watcher",
		}))).To(BeTrue())
	})

	It("does not treat an empty recurring: value as recurring", func() {
		Expect(command.IsRecurringTaskCommand(cmdWith(lib.TaskFrontmatter{
			"recurring": "",
		}))).To(BeFalse())
	})

	It("does not treat an unmarked command as recurring", func() {
		Expect(command.IsRecurringTaskCommand(cmdWith(lib.TaskFrontmatter{
			"created_by": "go-version-watcher",
			"status":     "in_progress",
		}))).To(BeFalse())
	})

	It("does not treat a missing frontmatter as recurring", func() {
		Expect(command.IsRecurringTaskCommand(cmdWith(nil))).To(BeFalse())
	})
})
