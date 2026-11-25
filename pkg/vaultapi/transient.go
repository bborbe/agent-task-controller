// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vaultapi

import (
	"context"
	stderrors "errors"
	"io"
	"net"
	"net/http"
	"syscall"

	"github.com/bborbe/errors"
)

// ErrTransient marks a failure that is a property of the moment rather than of
// the request: the vault-api was unreachable, timed out, or answered with a
// status that means "try again". A caller must treat a marked error as "not
// decided yet" — the same request can succeed a minute later, and a command
// consumer that commits its Kafka offset past it loses the command for good.
//
// It is deliberately a marker rather than a category of its own: the underlying
// cause stays reachable through Unwrap, so errors.Is(err, context.DeadlineExceeded)
// keeps working beside errors.Is(err, ErrTransient).
var ErrTransient = stderrors.New("transient")

// IsTransient reports whether err is, or wraps, ErrTransient.
func IsTransient(err error) bool {
	return err != nil && stderrors.Is(err, ErrTransient)
}

// transientError attaches ErrTransient to a cause without replacing its
// message. The message is the cause's own, because that is what lands in the
// command-failure log line an operator has to act on.
type transientError struct {
	err error
}

func (t *transientError) Error() string { return t.err.Error() }

func (t *transientError) Unwrap() error { return t.err }

// Is makes errors.Is(err, ErrTransient) true for the marked form while the cause
// keeps answering its own Is checks.
func (t *transientError) Is(target error) bool { return target == ErrTransient }

// markTransient marks err as retryable. A nil error stays nil, so a caller can
// mark unconditionally.
func markTransient(err error) error {
	if err == nil {
		return nil
	}
	return &transientError{err: err}
}

// isTransientStatus reports whether an HTTP status means "the request was not
// decided". 5xx is the server's own admission that it could not answer; 429 and
// 408 are the two 4xx statuses that describe the moment rather than the request.
// 501 Not Implemented is excluded on purpose: it reports a route the server will
// never serve, so retrying it only delays the visible failure.
func isTransientStatus(status int) bool {
	if status == http.StatusNotImplemented {
		return false
	}
	return status >= http.StatusInternalServerError ||
		status == http.StatusTooManyRequests ||
		status == http.StatusRequestTimeout
}

// unexpectedStatusError renders a status this client does not decode, marked
// transient when the status says the failure belongs to the moment and not to
// the request. Every route's "unexpected status" branch goes through here, so no
// route can report a 503 as a permanent failure.
func unexpectedStatusError(ctx context.Context, what string, status int, raw []byte) error {
	err := errors.Errorf(ctx, "%s: unexpected status %d: %s", what, status, snippet(raw))
	if isTransientStatus(status) {
		return markTransient(err)
	}
	return err
}

// isTransientTransportError reports whether a transport-level failure is worth
// retrying: the connection was refused, reset or broken, the request timed out,
// or the body ended early. A cancelled context is NOT transient — it is the
// consumer shutting down, and retrying it would delay the shutdown.
func isTransientTransportError(err error) bool {
	if err == nil {
		return false
	}
	if stderrors.Is(err, context.Canceled) {
		return false
	}
	if stderrors.Is(err, context.DeadlineExceeded) ||
		stderrors.Is(err, io.EOF) ||
		stderrors.Is(err, io.ErrUnexpectedEOF) ||
		stderrors.Is(err, syscall.ECONNREFUSED) ||
		stderrors.Is(err, syscall.ECONNRESET) ||
		stderrors.Is(err, syscall.ECONNABORTED) ||
		stderrors.Is(err, syscall.ETIMEDOUT) ||
		stderrors.Is(err, syscall.EHOSTUNREACH) ||
		stderrors.Is(err, syscall.ENETUNREACH) ||
		stderrors.Is(err, syscall.EPIPE) {
		return true
	}
	// A name that does not resolve is usually the vault-api Service being
	// recreated or a resolver blip, not a route the API will never serve: the
	// request never left the process, so repeating it is free. A definitive
	// "no such host" is the exception — that is a misconfigured address, and
	// repeating it only delays the failure reaching whoever can fix it.
	var dnsErr *net.DNSError
	if stderrors.As(err, &dnsErr) {
		return !dnsErr.IsNotFound
	}
	var netErr net.Error
	if stderrors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}
