// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
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
	// Command is the original command, verbatim, so a re-drive executes exactly
	// what the producer sent.
	Command base.Command `json:"command"`
	// ParkedAt is when the command was first parked; the re-drive window and the
	// expiry drop are both measured from it.
	ParkedAt time.Time `json:"parkedAt"`
	// TaskIdentifier is the task the command addresses, kept beside the payload
	// so a drop can name it without decoding the command again.
	TaskIdentifier string `json:"taskIdentifier,omitempty"`
}

// CommandPark stores commands whose execution failed transiently, so they can be
// re-driven instead of lost.
type CommandPark interface {
	// Park writes the command inside the caller's transaction, so the park and the
	// offset commit it enables are one write: either the command is parked and the
	// offset may advance, or neither happened.
	Park(
		ctx context.Context,
		tx libkv.Tx,
		command base.Command,
		parkedAt time.Time,
		taskIdentifier string,
	) error
	// List returns every parked command, oldest park first is not guaranteed — the
	// re-drive reads each entry's own ParkedAt.
	List(ctx context.Context) ([]ParkedCommand, error)
	// Delete removes one parked command, by key.
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
	parkedAt time.Time,
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
		return errors.Wrapf(ctx, err, "marshal parked command")
	}
	bucket, err := tx.CreateBucketIfNotExists(ctx, p.bucket)
	if err != nil {
		return errors.Wrapf(ctx, err, "create park bucket")
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
			if errors.Is(err, libkv.BucketNotFoundError) {
				return nil
			}
			return errors.Wrapf(ctx, err, "get park bucket")
		}
		return libkv.ForEach(ctx, bucket, func(item libkv.Item) error {
			return item.Value(func(raw []byte) error {
				var entry ParkedCommand
				if err := json.Unmarshal(raw, &entry); err != nil {
					// A corrupt entry must not wedge the loop: log it with its key
					// and skip it, leaving it for an operator rather than deleting
					// evidence.
					glog.Warningf(
						"parked command %s cannot be decoded and is skipped: %v",
						item.Key(),
						err,
					)
					return nil
				}
				result = append(result, entry)
				return nil
			})
		})
	})
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "list parked commands")
	}
	return result, nil
}

func (p *commandPark) Delete(ctx context.Context, key string) error {
	err := p.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		bucket, err := tx.Bucket(ctx, p.bucket)
		if err != nil {
			if errors.Is(err, libkv.BucketNotFoundError) {
				return nil
			}
			return errors.Wrapf(ctx, err, "get park bucket")
		}
		if err := bucket.Delete(ctx, []byte(key)); err != nil {
			if errors.Is(err, libkv.KeyNotFoundError) {
				return nil
			}
			return errors.Wrapf(ctx, err, "delete parked command %s", key)
		}
		return nil
	})
	if err != nil {
		return errors.Wrapf(ctx, err, "delete parked command %s", key)
	}
	return nil
}
