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
)

// NewExpiredCommandSkip returns a predicate reporting whether msg carries a command whose
// RequestTime is older than commandExpireDuration. True means the message is dropped; false
// means it is passed on.
//
// The predicate exists to be composed outside cdb.CreateCommandMessageHandlerBatch. The handler
// already discards an expired command, but it does so from inside the transaction that
// NewMessageHandlerBatchTxUpdate opens for every batch, so the discard costs a bbolt write
// transaction and its fsync. Composed outside, the same command is dropped before the
// transaction exists.
//
// A message that does not unmarshal as a command is kept, so the handler reports the parse
// error exactly as it would without this filter; the predicate never hides a malformed message.
func NewExpiredCommandSkip(
	commandExpireDuration time.Duration,
	currentDateTime libtime.CurrentDateTimeGetter,
) func(*sarama.ConsumerMessage) bool {
	return func(msg *sarama.ConsumerMessage) bool {
		var command base.Command
		if err := json.Unmarshal(msg.Value, &command); err != nil {
			return false
		}
		return currentDateTime.Now().Time().After(command.RequestTime.Add(commandExpireDuration))
	}
}
