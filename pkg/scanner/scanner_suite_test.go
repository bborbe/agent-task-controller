// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner_test

import (
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate

func TestSuite(t *testing.T) {
	time.Local = time.UTC
	format.TruncatedDiff = false
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	// Wider than the repo's 60 s default: most specs here shell out to `git` to
	// build a real repository. Measured 2026-11-08 on a loaded host: ~48 s run
	// alone, and over 60 s inside a parallel `make precommit`, which timed out.
	suiteConfig.Timeout = 180 * time.Second
	RunSpecs(t, "Test Suite", suiteConfig, reporterConfig)
}
