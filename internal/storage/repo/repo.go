package repo

import (
	"context"
	"time"
)

type Device struct {
	ID       string
	ShardID  string
	IP       string
	MAC      string
	Vendor   string
	Model    string
	Firmware string
	Hostname string
	Tags     map[string]string
}

type DeviceState struct {
	DeviceID     string
	Online       bool
	LastSeenAt   time.Time
	HashrateTHS  float64
	TempMaxC     float64
	FanRpmMax    uint32
	PowerW       uint32
	UptimeS      uint64
	RebootCount1H uint32
}

type Devices interface {
	UpsertDevice(ctx context.Context, d Device) (string, error)
	UpdateState(ctx context.Context, s DeviceState) error
}

type Events interface {
	InsertEvent(ctx context.Context, ts time.Time, subject, shardID, deviceID, ip, mac string, payloadPB []byte) error
}

type Metrics interface {
	InsertMetric(ctx context.Context, ts time.Time, deviceID, ip, shardID string, fields map[string]any) error
}

