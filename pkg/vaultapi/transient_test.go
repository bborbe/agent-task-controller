// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vaultapi_test

import (
	"context"
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

// The command consumer decides whether to commit its Kafka offset from the error
// class alone. A 503 that reads as permanent loses the command (measured
// 2026-11-24: three release tasks), so these specs pin which answers are the
// moment's and which are the request's.
var _ = Describe("transient classification", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	statusServer := func(status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"whatever"}`))
		}))
	}

	DescribeTable("marks a retryable status as transient",
		func(status int) {
			server := statusServer(status)
			defer server.Close()
			client := vaultapi.NewClient(server.URL, "", "")

			_, err := client.ReadTask(ctx, "abc")
			Expect(err).To(HaveOccurred())
			Expect(vaultapi.IsTransient(err)).To(BeTrue())
		},
		Entry("500 internal server error", http.StatusInternalServerError),
		Entry("502 bad gateway", http.StatusBadGateway),
		Entry("503 service unavailable", http.StatusServiceUnavailable),
		Entry("504 gateway timeout", http.StatusGatewayTimeout),
		Entry("429 too many requests", http.StatusTooManyRequests),
		Entry("408 request timeout", http.StatusRequestTimeout),
	)

	DescribeTable("does not mark a decided status as transient",
		func(status int) {
			server := statusServer(status)
			defer server.Close()
			client := vaultapi.NewClient(server.URL, "", "")

			_, err := client.ReadTask(ctx, "abc")
			Expect(err).To(HaveOccurred())
			Expect(vaultapi.IsTransient(err)).To(BeFalse())
		},
		Entry("400 bad request", http.StatusBadRequest),
		Entry("404 not found", http.StatusNotFound),
		Entry("422 unprocessable entity", http.StatusUnprocessableEntity),
		Entry("501 not implemented", http.StatusNotImplemented),
	)

	It("marks an unreachable vault-api as transient", func() {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := server.URL
		server.Close()

		client := vaultapi.NewClient(url, "", "")
		_, err := client.ReadTask(ctx, "abc")
		Expect(err).To(HaveOccurred())
		Expect(vaultapi.IsTransient(err)).To(BeTrue())
	})

	It("marks a truncated response body as transient", func() {
		// The request reached the server and the status was fine; the connection
		// died while the body was being read. Nothing was decided, so the caller
		// must be able to retry.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"abc"`))
		}))
		defer server.Close()

		client := vaultapi.NewClient(server.URL, "", "")
		_, err := client.ReadTask(ctx, "abc")
		Expect(err).To(HaveOccurred())
		Expect(vaultapi.IsTransient(err)).To(BeTrue())
	})

	It("marks an expired deadline as transient", func() {
		slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(200 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"abc","title":"t"}`))
		}))
		defer slow.Close()
		client := vaultapi.NewClient(slow.URL, "", "")

		expired, cancelExpired := context.WithTimeout(ctx, time.Millisecond)
		defer cancelExpired()
		_, err := client.ReadTask(expired, "abc")
		Expect(err).To(HaveOccurred())
		Expect(vaultapi.IsTransient(err)).To(BeTrue())
		// The marker must not hide the cause: callers still ask their own questions
		// of the wrapped error.
		Expect(stderrors.Is(err, context.DeadlineExceeded)).To(BeTrue())
	})

	It("does not mark a cancelled context as transient", func() {
		// A cancelled context is the consumer shutting down. Retrying it would
		// delay the shutdown and could loop the consumer through a restart.
		server := statusServer(http.StatusOK)
		defer server.Close()
		client := vaultapi.NewClient(server.URL, "", "")

		cancelled, cancelCancelled := context.WithCancel(ctx)
		cancelCancelled()
		_, err := client.ReadTask(cancelled, "abc")
		Expect(err).To(HaveOccurred())
		Expect(vaultapi.IsTransient(err)).To(BeFalse())
	})
})
