// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vaultapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

var _ = Describe("PatchTask", func() {
	var (
		ctx      context.Context
		recorder *recordingServer
		server   *httptest.Server
		client   vaultapi.Client
	)

	BeforeEach(func() {
		ctx = context.Background()
		recorder = &recordingServer{}
	})

	JustBeforeEach(func() {
		client = vaultapi.NewClient(server.URL, "secret", "agent-task-controller")
	})

	AfterEach(func() {
		if server != nil {
			server.Close()
		}
		Expect(recorder.failure()).NotTo(HaveOccurred())
	})

	Context("when the API refuses the transition", func() {
		BeforeEach(func() {
			server = httptest.NewServer(recorder.handler(
				http.StatusConflict,
				`{"error":"invalid_transition","reason":"refusing phase transition \"done\" -> \"human_review\"","from":"done","to":"human_review"}`,
			))
		})

		It("reports the refusal, not a duplicate", func() {
			// The full Ginkgo name is "PatchTask when the API refuses the
			// transition reports the refusal, not a duplicate" (AC1).
			_, err := client.PatchTask(
				ctx, "task-1", map[string]any{"phase": "human_review"},
			)
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, vaultapi.ErrAlreadyExists)).To(BeFalse())
			Expect(errors.Is(err, vaultapi.ErrRefused)).To(BeTrue())

			var refusal *vaultapi.RefusedError
			Expect(errors.As(err, &refusal)).To(BeTrue())
			Expect(refusal.Code).To(Equal("invalid_transition"))
			Expect(refusal.From).To(Equal("done"))
			Expect(refusal.To).To(Equal("human_review"))
			Expect(strings.Count(err.Error(), "patch task task-1")).To(Equal(1))
		})
	})

	Context("when the address is occupied", func() {
		BeforeEach(func() {
			server = httptest.NewServer(recorder.handler(
				http.StatusConflict,
				`{"error":"task_exists","task_identifier":"task-1","status":"completed"}`,
			))
		})

		It("names the operation once and keeps ErrAlreadyExists", func() {
			_, err := client.PatchTask(ctx, "task-1", map[string]any{"phase": "done"})
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, vaultapi.ErrAlreadyExists)).To(BeTrue())
			Expect(errors.Is(err, vaultapi.ErrRefused)).To(BeFalse())
			Expect(strings.Count(err.Error(), "patch task task-1")).To(Equal(1))

			var conflict *vaultapi.AlreadyExistsError
			Expect(errors.As(err, &conflict)).To(BeTrue())
			Expect(conflict.What).To(Equal("patch task task-1"))
		})
	})

	Context("when the 409 body is not a conflict envelope", func() {
		BeforeEach(func() {
			server = httptest.NewServer(recorder.handler(
				http.StatusConflict,
				`conflict`,
			))
		})

		It("keeps the ErrAlreadyExists contract", func() {
			// Negative control for over-narrowing (AC5): a body that does not
			// parse must not become a refusal or a new error class.
			_, err := client.PatchTask(ctx, "task-1", map[string]any{"phase": "done"})
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, vaultapi.ErrAlreadyExists)).To(BeTrue())
			Expect(errors.Is(err, vaultapi.ErrRefused)).To(BeFalse())
		})
	})
})
