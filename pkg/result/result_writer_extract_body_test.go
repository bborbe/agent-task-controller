// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package result_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/result"
)

var _ = Describe("ResultWriter ExtractBody", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	Describe("ExtractBody", func() {
		It("returns body after frontmatter delimiter", func() {
			content := []byte("---\nkey: value\n---\nbody content here\n")
			body, err := result.ExtractBody(ctx, content)
			Expect(err).NotTo(HaveOccurred())
			Expect(body).To(Equal("body content here\n"))
		})

		It("returns empty string when body is empty", func() {
			content := []byte("---\nkey: value\n---\n")
			body, err := result.ExtractBody(ctx, content)
			Expect(err).NotTo(HaveOccurred())
			Expect(body).To(Equal(""))
		})

		It("returns error when no frontmatter delimiter", func() {
			content := []byte("no delimiter here")
			_, err := result.ExtractBody(ctx, content)
			Expect(err).To(HaveOccurred())
		})

		It("returns error when frontmatter is not closed", func() {
			content := []byte("---\nkey: value\n")
			_, err := result.ExtractBody(ctx, content)
			Expect(err).To(HaveOccurred())
		})

		It("handles CRLF line endings after opening delimiter", func() {
			content := []byte("---\r\nkey: value\n---\nbody\n")
			body, err := result.ExtractBody(ctx, content)
			Expect(err).NotTo(HaveOccurred())
			Expect(body).To(Equal("body\n"))
		})
	})
})
