// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vaultapi_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

var _ = Describe("IsTerminalStatus", func() {
	// The create's conflict answer is only actionable with this predicate: a
	// terminal occupant may be replaced, a live one never may. The two terminal
	// values are the whole protocol, so an unknown or empty status must read as
	// live rather than being guessed at.
	DescribeTable(
		"reports terminality",
		func(status string, expected bool) {
			Expect(vaultapi.IsTerminalStatus(status)).To(Equal(expected))
		},
		Entry("completed", "completed", true),
		Entry("aborted", "aborted", true),
		Entry("empty", "", false),
		Entry("live: next", "next", false),
		Entry("live: in_progress", "in_progress", false),
		Entry("live: hold", "hold", false),
		Entry("unknown value", "some-unknown-value", false),
		// The server normalises before sending, so padding is not expected on the
		// wire; if it ever appears it must not be read as terminal.
		Entry("padded completed", " completed ", false),
		Entry("case-different", "Completed", false),
	)
})
