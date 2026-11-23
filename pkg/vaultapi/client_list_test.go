// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vaultapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

// The listing is the cross-repo contract the index-miss path is built on: it
// matches candidates on task_identifier_previous without reading a page. A tag
// rename on either side would pass every other spec — testvault builds the struct
// directly and never crosses the wire — while silently dropping alias resolution
// AND the duplicate pair that spans the two keys, which is a wrong-task write
// rather than a visible failure. This spec pins the JSON key at the boundary.
var _ = Describe("ListTasks", func() {
	var (
		ctx      context.Context
		recorder *recordingServer
		server   *httptest.Server
		client   vaultapi.Client
	)

	BeforeEach(func() {
		ctx = context.Background()
		recorder = &recordingServer{}
		// The server is created here, not in the It: JustBeforeEach builds the client
		// from server.URL and runs before the It body.
		server = httptest.NewServer(recorder.handler(
			http.StatusOK,
			`{"items":[{"id":"new-id","title":"Alpha","task_identifier_previous":"old-id"},`+
				`{"id":"plain","title":"Beta"}],"total":2}`,
		))
	})

	JustBeforeEach(func() {
		client = vaultapi.NewClient(server.URL, "secret", "agent-task-controller")
	})

	AfterEach(func() {
		if server != nil {
			server.Close()
		}
		// Asserted on the spec goroutine: the handler records rather than asserts,
		// so a failure inside it surfaces as itself instead of a connection error.
		Expect(recorder.failure()).NotTo(HaveOccurred())
	})

	It("decodes task_identifier_previous from the listing items", func() {
		tasks, err := client.ListTasks(ctx, vaultapi.Filter{})
		Expect(err).NotTo(HaveOccurred())
		Expect(tasks).To(HaveLen(2))
		Expect(tasks[0].ID).To(Equal("new-id"))
		Expect(tasks[0].TaskIdentifierPrevious).To(Equal("old-id"))
		// A page never re-addressed carries no predecessor. It must decode as the
		// empty string, not a stray value — an empty predecessor is what keeps every
		// plain row out of the alias candidate set.
		Expect(tasks[1].TaskIdentifierPrevious).To(BeEmpty())

		method, path, _, _ := recorder.captured()
		Expect(method).To(Equal(http.MethodGet))
		Expect(path).To(Equal("/api/v1/tasks"))
	})
})
