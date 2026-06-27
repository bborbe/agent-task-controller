// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main_test

import (
	"context"
	"os"

	//nolint:depguard // false positive: rule denies github.com/bborbe/argument (no /v2) but prefix-matches github.com/bborbe/argument/v2; this IS the v2 the rule recommends
	libargument "github.com/bborbe/argument/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AUTO_INJECT_TASK_IDENTIFIER required-tag enforcement (AC1)", func() {
	type appShape struct {
		AutoInjectTaskIdentifier string `required:"true" arg:"auto-inject-task-identifier" env:"AUTO_INJECT_TASK_IDENTIFIER" usage:"x"`
	}

	var saved string
	var hadValue bool

	BeforeEach(func() {
		saved, hadValue = os.LookupEnv("AUTO_INJECT_TASK_IDENTIFIER")
		Expect(os.Unsetenv("AUTO_INJECT_TASK_IDENTIFIER")).To(Succeed())
	})

	AfterEach(func() {
		if hadValue {
			Expect(os.Setenv("AUTO_INJECT_TASK_IDENTIFIER", saved)).To(Succeed())
		}
	})

	It("returns a non-nil error from argument.Parse when the env var is unset", func() {
		var app appShape
		err := libargument.Parse(context.Background(), &app)
		Expect(err).To(HaveOccurred())
		// The library's error message contains the field name, arg name, or env var name.
		errStr := err.Error()
		Expect(
			errStr,
		).To(SatisfyAny(
			ContainSubstring("AUTO_INJECT_TASK_IDENTIFIER"),
			ContainSubstring("AutoInjectTaskIdentifier"),
			ContainSubstring("auto-inject-task-identifier"),
		), "error %q should reference the missing required field", errStr)
	})
})
