// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vaultapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/agent-task-controller/pkg/vaultapi"
)

// recordingServer answers one canned response and records the request that
// produced it, so a spec can assert on the wire shape rather than only on the
// decoded result.
type recordingServer struct {
	method string
	path   string
	body   map[string]any
	header http.Header
}

func (r *recordingServer) handler(status int, response string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		r.method = req.Method
		r.path = req.URL.Path
		r.header = req.Header.Clone()
		raw, err := io.ReadAll(req.Body)
		Expect(err).NotTo(HaveOccurred())
		if len(raw) > 0 {
			Expect(json.Unmarshal(raw, &r.body)).To(Succeed())
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, err = w.Write([]byte(response))
		Expect(err).NotTo(HaveOccurred())
	}
}

var _ = Describe("CreateTask", func() {
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
	})

	Context("without ReopenTerminal", func() {
		BeforeEach(func() {
			server = httptest.NewServer(
				recorder.handler(http.StatusCreated, `{"id":"task-1","title":"Alpha"}`),
			)
		})

		It("posts the title, frontmatter and body, and no reopen field", func() {
			created, err := client.CreateTask(ctx, vaultapi.Task{
				Title:       "Alpha",
				Frontmatter: map[string]any{"status": "in_progress"},
				Body:        "hello\n",
			}, vaultapi.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(created.ID).To(Equal("task-1"))

			Expect(recorder.method).To(Equal(http.MethodPost))
			Expect(recorder.path).To(Equal("/api/v1/tasks"))
			Expect(recorder.body["title"]).To(Equal("Alpha"))
			Expect(recorder.body["body"]).To(Equal("hello\n"))
			// The mode is opt-in: sending it unconditionally would make every
			// ordinary create ask to replace a terminal occupant.
			Expect(recorder.body).NotTo(HaveKey("reopen"))
			Expect(recorder.header.Get("X-Gateway-Secret")).To(Equal("secret"))
		})
	})

	Context("with ReopenTerminal", func() {
		BeforeEach(func() {
			server = httptest.NewServer(
				recorder.handler(
					http.StatusOK,
					`{"id":"task-2","title":"Alpha","reopened":true}`,
				),
			)
		})

		It("asks the API to replace a terminal occupant", func() {
			created, err := client.CreateTask(ctx, vaultapi.Task{
				Title:       "Alpha",
				Frontmatter: map[string]any{"status": "in_progress"},
			}, vaultapi.CreateOptions{ReopenTerminal: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(created.ID).To(Equal("task-2"))
			Expect(recorder.body["reopen"]).To(Equal("terminal"))
		})
	})

	Context("when the title path is occupied", func() {
		BeforeEach(func() {
			server = httptest.NewServer(recorder.handler(
				http.StatusConflict,
				`{"error":"task_exists","id":"Alpha","task_identifier":"task-1","status":"completed"}`,
			))
		})

		It("carries the occupant so the caller can decide what to do", func() {
			// The 409's `id` is the title the caller just sent, so it says nothing.
			// The occupant's identifier and status are what distinguish "same task,
			// terminal, reopen" from "different task, disambiguate the title".
			_, err := client.CreateTask(
				ctx,
				vaultapi.Task{Title: "Alpha"},
				vaultapi.CreateOptions{},
			)
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, vaultapi.ErrAlreadyExists)).To(BeTrue())

			var conflict *vaultapi.AlreadyExistsError
			Expect(errors.As(err, &conflict)).To(BeTrue())
			Expect(conflict.Code).To(Equal("task_exists"))
			Expect(conflict.TaskIdentifier).To(Equal("task-1"))
			Expect(conflict.Status).To(Equal("completed"))
		})
	})
})

var _ = Describe("ReaddressTask", func() {
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
	})

	Context("when the target identifier is free", func() {
		BeforeEach(func() {
			server = httptest.NewServer(
				recorder.handler(http.StatusOK, `{"id":"task-2","title":"Alpha"}`),
			)
		})

		It("posts the new identifier to the readdress route", func() {
			moved, err := client.ReaddressTask(ctx, "task-1", "task-2")
			Expect(err).NotTo(HaveOccurred())
			Expect(moved.ID).To(Equal("task-2"))

			Expect(recorder.method).To(Equal(http.MethodPost))
			Expect(recorder.path).To(Equal("/api/v1/tasks/task-1/readdress"))
			Expect(recorder.body["task_identifier"]).To(Equal("task-2"))
		})
	})

	Context("when the target identifier is taken", func() {
		BeforeEach(func() {
			server = httptest.NewServer(recorder.handler(
				http.StatusConflict,
				`{"error":"task_exists","id":"Bravo","task_identifier":"task-2","status":"in_progress"}`,
			))
		})

		It("refuses rather than merging two tasks into one address", func() {
			_, err := client.ReaddressTask(ctx, "task-1", "task-2")
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, vaultapi.ErrAlreadyExists)).To(BeTrue())

			var conflict *vaultapi.AlreadyExistsError
			Expect(errors.As(err, &conflict)).To(BeTrue())
			Expect(conflict.TaskIdentifier).To(Equal("task-2"))
			Expect(conflict.Status).To(Equal("in_progress"))
		})
	})
})
