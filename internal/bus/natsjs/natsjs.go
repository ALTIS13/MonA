package natsjs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"asic-control/internal/bus"
	"asic-control/internal/events"

	"github.com/nats-io/nats.go"
)

type Config struct {
	URL     string
	Prefix  string
	Timeout time.Duration
}

type Client struct {
	nc     *nats.Conn
	js     nats.JetStreamContext
	prefix string
}

func Connect(cfg Config) (*Client, error) {
	nc, err := nats.Connect(cfg.URL, nats.Timeout(cfg.Timeout))
	if err != nil {
		return nil, err
	}
	js, err := nc.JetStream()
	if err != nil {
		_ = nc.Drain()
		nc.Close()
		return nil, err
	}
	return &Client{nc: nc, js: js, prefix: cfg.Prefix}, nil
}

func (c *Client) Close() error {
	if c.nc == nil {
		return nil
	}
	return c.nc.Drain()
}

func (c *Client) EnsureStreams() error {
	// Single stream for all MonA subjects under prefix.
	name := fmt.Sprintf("%s_events", c.prefix)
	subject := events.Subject(c.prefix, ">")

	_, err := c.js.StreamInfo(name)
	if err == nil {
		return nil
	}
	if !errors.Is(err, nats.ErrStreamNotFound) {
		return err
	}

	_, err = c.js.AddStream(&nats.StreamConfig{
		Name:      name,
		Subjects:  []string{subject},
		Storage:   nats.FileStorage,
		Retention: nats.LimitsPolicy,
		MaxAge:    7 * 24 * time.Hour,
		// Tunables for high-volume metrics are handled separately (ClickHouse).
		// JetStream is for events/state transitions, not per-second metrics.
	})
	return err
}

func (c *Client) Publish(ctx context.Context, subject string, data []byte) error {
	s := events.Subject(c.prefix, subject)
	_, err := c.js.PublishMsg(&nats.Msg{
		Subject: s,
		Data:    data,
	}, nats.Context(ctx))
	return err
}

type pullConsumer struct {
	sub *nats.Subscription
}

func (c *Client) NewPullConsumer(durable, filterSubject string, maxAckPending int) (bus.PullConsumer, error) {
	s := events.Subject(c.prefix, filterSubject)

	// Create durable consumer implicitly by using PullSubscribe + durable name.
	sub, err := c.js.PullSubscribe(s, durable,
		nats.ManualAck(),
		nats.AckExplicit(),
		nats.MaxAckPending(maxAckPending),
	)
	if err != nil {
		return nil, err
	}
	return &pullConsumer{sub: sub}, nil
}

type msg struct {
	m *nats.Msg
}

func (m *msg) Data() []byte { return m.m.Data }
func (m *msg) Ack() error   { return m.m.Ack() }
func (m *msg) Nak() error   { return m.m.Nak() }
func (m *msg) Term() error  { return m.m.Term() }

func (pc *pullConsumer) Fetch(ctx context.Context, batch int, wait time.Duration) ([]bus.Message, error) {
	msgs, err := pc.sub.Fetch(batch, nats.Context(ctx), nats.MaxWait(wait))
	if err != nil {
		return nil, err
	}
	out := make([]bus.Message, 0, len(msgs))
	for _, nm := range msgs {
		out = append(out, &msg{m: nm})
	}
	return out, nil
}

