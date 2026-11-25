// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"
	"encoding/json"

	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// commandParkBucket is the BoltDB bucket holding commands the controller could
// not execute yet. It lives in the same database as the consumer offsets, so a
// parked command and the offset that moves past it are written by the same
// process and survive the same restart.
const commandParkBucket = "parked-commands"

// ParkedCommand is one command parked because the vault-api could not answer.
//
// It holds the command itself rather than a reference to a Kafka message: the
// command topic's retention is shorter than an outage can be, and the offset is
// committed as soon as the command is parked (that commit is what stops a
// transient failure from replaying the whole batch), so the message is not
// guaranteed to be re-readable.
type ParkedCommand struct {
	// Key is the bucket key, which is the command's requestID.
	Key string `json:"key"`
	// Command is the command as it was received, so a re-drive executes the same
	// request. It round-trips through JSON, so a numeric Data value comes back as
	// a float64 — adequate for the identifying fields, which are strings.
	Command base.Command `json:"command"`
	// ParkedAt is when the command was parked. A re-park of the same requestID
	// replaces it, so this is the most recent park, not the first. The re-drive
	// delay is measured from it; the drop window is measured from the command's own
	// RequestTime, so a re-park cannot extend a command's life.
	ParkedAt libtime.DateTime `json:"parkedAt"`
	// TaskIdentifier is the task the command addresses, kept beside the payload
	// so a drop can name it without decoding the command again.
	TaskIdentifier string `json:"taskIdentifier,omitempty"`
}

//counterfeiter:generate -o ../../mocks/command_park.go --fake-name CommandPark . CommandPark

// CommandPark stores commands whose execution failed transiently, so they can be
// re-driven instead of lost.
//
// It lives in the same BoltDB as the consumer offsets. That does not make the park
// and the offset one write — the consumer marks the offset in a separate
// transaction immediately after the handler's — so a crash in the gap leaves the
// command parked AND the message re-readable. The command is then executed twice
// at worst, which is why only repeat-safe operations are parked.
//
// ⚠️ The database directory (DATA_DIR) must therefore be persistent: a park that
// dies with the pod is a command lost exactly as it was before this type existed.
type CommandPark interface {
	// Park writes the command inside the caller's transaction, so the park commits
	// with the handler work that failed. Re-parking the same requestID replaces the
	// entry and refreshes ParkedAt.
	Park(
		ctx context.Context,
		tx libkv.Tx,
		command base.Command,
		parkedAt libtime.DateTime,
		taskIdentifier string,
	) error
	// List returns every parked command. Order is the bucket's key order, not park
	// time; a caller reads each entry's own ParkedAt.
	//
	// It opens its own read transaction, so it must not be called while a write
	// transaction on the same database is open — bbolt allows one writer, and a
	// nested transaction on the same DB can block until it times out.
	List(ctx context.Context) ([]ParkedCommand, error)
	// Delete removes one parked command, by key. It opens its own write
	// transaction, with the same restriction as List.
	Delete(ctx context.Context, key string) error
}

// NewCommandPark returns a CommandPark backed by db.
func NewCommandPark(db libkv.DB) CommandPark {
	return &commandPark{
		db:     db,
		bucket: libkv.NewBucketName(commandParkBucket),
	}
}

type commandPark struct {
	db     libkv.DB
	bucket libkv.BucketName
}

// parkKey is the command's own identity. A command re-published with the same
// requestID overwrites its parked entry rather than adding a second one, so a
// producer's retry cannot multiply the re-drive work.
func parkKey(command base.Command) string {
	return string(command.RequestID)
}

func (p *commandPark) Park(
	ctx context.Context,
	tx libkv.Tx,
	command base.Command,
	parkedAt libtime.DateTime,
	taskIdentifier string,
) error {
	entry := ParkedCommand{
		Key:            parkKey(command),
		Command:        command,
		ParkedAt:       parkedAt,
		TaskIdentifier: taskIdentifier,
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return errors.Wrap(ctx, err, "marshal parked command")
	}
	bucket, err := tx.CreateBucketIfNotExists(ctx, p.bucket)
	if err != nil {
		return errors.Wrap(ctx, err, "create park bucket")
	}
	if err := bucket.Put(ctx, []byte(entry.Key), raw); err != nil {
		return errors.Wrapf(ctx, err, "put parked command %s", entry.Key)
	}
	return nil
}

func (p *commandPark) List(ctx context.Context) ([]ParkedCommand, error) {
	result := make([]ParkedCommand, 0)
	err := p.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		bucket, err := tx.Bucket(ctx, p.bucket)
		if err != nil {
			// No bucket means nothing has ever been parked, which is the ordinary
			// steady state rather than a failure.
			if errors.Is(err, libkv.ErrBucketNotFound) {
				return nil
			}
			return err
		}
		return libkv.ForEach(ctx, bucket, func(item libkv.Item) error {
			return item.Value(func(raw []byte) error {
				var entry ParkedCommand
				if err := json.Unmarshal(raw, &entry); err != nil {
					// A corrupt entry must not wedge the loop: log it with its key
					// and skip it, leaving it for an operator rather than deleting
					// evidence. It is logged once per sweep, which is what makes it
					// impossible to miss and cheap to live with.
					glog.Warningf(
						"parked command %s cannot be decoded and is skipped: %v",
						item.Key(),
						err,
					)
					return nil
				}
				// The bucket key is authoritative: a Delete by key must address the
				// entry that was actually read, even if the stored copy disagrees.
				entry.Key = string(item.Key())
				result = append(result, entry)
				return nil
			})
		})
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "list parked commands")
	}
	return result, nil
}

func (p *commandPark) Delete(ctx context.Context, key string) error {
	err := p.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		bucket, err := tx.Bucket(ctx, p.bucket)
		if err != nil {
			if errors.Is(err, libkv.ErrBucketNotFound) {
				return nil
			}
			return err
		}
		// Deleting an absent key is not an error: bbolt's Bucket.Delete returns nil
		// for it, and libkv documents ErrKeyNotFound as currently unreturned.
		return bucket.Delete(ctx, []byte(key))
	})
	if err != nil {
		return errors.Wrapf(ctx, err, "delete parked command %s", key)
	}
	return nil
}
