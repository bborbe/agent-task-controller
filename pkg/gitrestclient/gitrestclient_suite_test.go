// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate

package gitrestclient_test

import (
	"flag"
	"io"
	"os"
	"testing"
	"time"

	"github.com/golang/glog"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
)

func TestGitrestclient(t *testing.T) {
	time.Local = time.UTC
	format.TruncatedDiff = false
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	suiteConfig.Timeout = 60 * time.Second
	RunSpecs(t, "Gitrestclient Suite", suiteConfig, reporterConfig)
}

// captureGlogWarnings runs fn with glog WARNING output captured and returns the
// captured bytes. glog's -logtostderr defaults to false, so warnings otherwise go
// to files; setting the flag routes them to stderr, which we redirect to a pipe.
// The previous flag value is restored alongside os.Stderr so later specs do not
// inherit glog-on-stderr. A drain goroutine reads the pipe concurrently so fn can
// never block on a full 64 KB kernel pipe buffer. Ginkgo runs specs serially, so
// the global os.Stderr redirect is safe. Same helper as
// pkg/scanner/vault_scanner_internal_test.go.
func captureGlogWarnings(fn func()) string {
	oldLogToStderr := flag.Lookup("logtostderr").Value.String()
	Expect(flag.Set("logtostderr", "true")).To(Succeed())
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	Expect(err).NotTo(HaveOccurred())
	os.Stderr = w

	type drainResult struct {
		out []byte
		err error
	}
	done := make(chan drainResult, 1)
	go func() {
		b, rerr := io.ReadAll(r)
		done <- drainResult{out: b, err: rerr}
	}()

	fn()

	// Flush into the pipe FIRST, then restore os.Stderr before closing the writer
	// so glog's flushDaemon goroutine can never write into a closed pipe mid-read.
	glog.Flush()
	os.Stderr = oldStderr
	Expect(flag.Set("logtostderr", oldLogToStderr)).To(Succeed())
	_ = w.Close()

	res := <-done
	Expect(res.err).NotTo(HaveOccurred())
	_ = r.Close()
	return string(res.out)
}
