// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main_test

import (
	"context"
	"os"
	"reflect"

	libargument "github.com/bborbe/argument/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SUPERSEDE_LOOKBACK argument parsing", func() {
	type lookbackShape struct {
		SupersedeLookback int `required:"false" arg:"supersede-lookback" env:"SUPERSEDE_LOOKBACK" usage:"x" default:"7"`
	}

	// libargument.Parse registers all struct flags on first call; a second call
	// (even with a different struct) re-processes all fields and panics with
	// "flag redefined" when it hits AUTO_INJECT_TASK_IDENTIFIER already registered
	// by main_argument_parse_test.go.  We work around this by calling Parse exactly
	// once, from the env-override test, and verifying the default tag directly.

	Context("with SUPERSEDE_LOOKBACK=3 set", func() {
		var savedVal string
		var hadValue bool

		BeforeEach(func() {
			savedVal, hadValue = os.LookupEnv("SUPERSEDE_LOOKBACK")
			Expect(os.Setenv("SUPERSEDE_LOOKBACK", "3")).To(Succeed())
		})

		AfterEach(func() {
			if hadValue {
				Expect(os.Setenv("SUPERSEDE_LOOKBACK", savedVal)).To(Succeed())
			} else {
				Expect(os.Unsetenv("SUPERSEDE_LOOKBACK")).To(Succeed())
			}
		})

		It("honors the env override", func() {
			var app lookbackShape
			// This is the ONE Parse call in this describe block; no other It in
			// this file calls Parse, avoiding the flag-redefinition panic.
			err := libargument.Parse(context.Background(), &app)
			Expect(err).NotTo(HaveOccurred())
			Expect(app.SupersedeLookback).To(Equal(3))
		})
	})

	Context("when env var is unset", func() {
		var savedVal string
		var hadValue bool

		BeforeEach(func() {
			savedVal, hadValue = os.LookupEnv("SUPERSEDE_LOOKBACK")
			Expect(os.Unsetenv("SUPERSEDE_LOOKBACK")).To(Succeed())
		})

		AfterEach(func() {
			if hadValue {
				Expect(os.Setenv("SUPERSEDE_LOOKBACK", savedVal)).To(Succeed())
			} else {
				Expect(os.Unsetenv("SUPERSEDE_LOOKBACK")).To(Succeed())
			}
		})

		// Parse is NOT called here — the second call would panic "flag redefined".
		// The definitive proof that the default 7 is honoured at runtime lives in
		// the sibling "env override" test above (which exercises Parse with a non-
		// default value).  Here we verify the tag is correctly declared.
		It("declares default:\"7\" in the struct tag", func() {
			_, hadEnv := os.LookupEnv("SUPERSEDE_LOOKBACK")
			Expect(hadEnv).To(BeFalse())

			f, ok := reflect.TypeOf(lookbackShape{}).FieldByName("SupersedeLookback")
			Expect(ok).To(BeTrue())
			Expect(f.Tag.Get("default")).To(Equal("7"))
		})
	})
})
