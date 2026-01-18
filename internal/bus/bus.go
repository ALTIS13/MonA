package bus

import (
	"context"
	"time"
)

type Publisher interface {
	Publish(ctx context.Context, subject string, data []byte) error
}

type PullConsumer interface {
	// Fetch blocks up to wait time, returning up to batch messages.
	Fetch(ctx context.Context, batch int, wait time.Duration) ([]Message, error)
}

type Message interface {
	Data() []byte
	Ack() error
	Nak() error
	Term() error
}

