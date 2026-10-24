// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"encoding/json"
	"time"

	"github.com/IBM/sarama"
	"github.com/bborbe/cqrs/base"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// NewExpiredCommandSkip returns a predicate reporting whether msg carries a command whose
// RequestTime is older than commandExpireDuration. True means the message is dropped; false
// means it is passed on.
//
// The predicate exists to be composed outside cdb.CreateCommandMessageHandlerBatch. The handler
// already discards an expired command, but it does so from inside the batch transaction that
// NewMessageHandlerBatchTxUpdate opens, so the discard costs a bbolt write transaction and its
// fsync. Composed outside, the same command is dropped before the batch reaches the transaction.
//
// The drop is logged at V(2) with the same wording the handler used, so moving the decision ahead
// of the transaction does not also silence it — an operator asking why a command never ran still
// gets the answer. That log is the predicate's only side effect: it carries no state, and glog is
// safe to call from the per-partition consumer goroutines that share this predicate.
//
// A message that is not valid JSON is kept, so the handler reports the parse error exactly as it
// would without this filter. A payload that parses but carries no RequestTime is not kept: it
// unmarshals with a zero RequestTime, the comparison reads that as long expired, and the message
// is dropped — the same verdict the handler reaches, one step earlier.
//
// A kept message is JSON-decoded twice, here and again inside
// cdb.NewCommandObjectMessageHandlerTx. That is deliberate: the saving is a whole batch
// transaction, and the decode is small beside it. Do not "optimise" the second decode away
// without removing this one too.
func NewExpiredCommandSkip(
	commandExpireDuration time.Duration,
	currentDateTime libtime.CurrentDateTimeGetter,
) func(*sarama.ConsumerMessage) bool {
	return func(msg *sarama.ConsumerMessage) bool {
		var command base.Command
		if err := json.Unmarshal(msg.Value, &command); err != nil {
			return false
		}
		now := currentDateTime.Now()
		expireTime := command.RequestTime.Add(commandExpireDuration)
		if !now.Time().After(expireTime) {
			return false
		}
		glog.V(2).Infof(
			"command expired (expire %s < now %s)",
			expireTime.Format(time.RFC3339),
			now.Time().Format(time.RFC3339),
		)
		return true
	}
}
