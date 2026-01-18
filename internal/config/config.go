package config

import "time"

type NATS struct {
	URL      string        `env:"NATS_URL" envDefault:"nats://127.0.0.1:4222"`
	Prefix   string        `env:"NATS_PREFIX" envDefault:"mona"`
	Timeout  time.Duration `env:"NATS_TIMEOUT" envDefault:"5s"`
	StreamKV string        `env:"NATS_KV_BUCKET" envDefault:"mona_kv"`
}

type Postgres struct {
	DSN     string        `env:"PG_DSN" envDefault:"postgres://mona:mona@127.0.0.1:5432/mona?sslmode=disable"`
	Timeout time.Duration `env:"PG_TIMEOUT" envDefault:"5s"`
}

type MikroTik struct {
	Address           string        `env:"MT_ADDRESS" envDefault:"127.0.0.1:8728"`
	Username          string        `env:"MT_USERNAME" envDefault:""`
	Password          string        `env:"MT_PASSWORD" envDefault:""`
	TLS               bool          `env:"MT_TLS" envDefault:"false"`
	PollInterval      time.Duration `env:"MT_POLL_INTERVAL" envDefault:"10s"`
	DiscoverySubnets  string        `env:"MT_DISCOVERY_SUBNETS" envDefault:""`
	ShardID           string        `env:"MT_SHARD_ID" envDefault:"shard-1"`
	AllowedVLANsCSV   string        `env:"MT_ALLOWED_VLANS" envDefault:""`
	AllowedIfacesCSV  string        `env:"MT_ALLOWED_IFACES" envDefault:""`
	ASICOUIsCSV       string        `env:"ASIC_OUIS" envDefault:""`
	PublishAllLeases  bool          `env:"MT_PUBLISH_ALL_LEASES" envDefault:"false"`
	ReadOnlySNMP      bool          `env:"MT_SNMP_FALLBACK" envDefault:"false"`
}

type Collector struct {
	ShardID        string        `env:"COLLECTOR_SHARD_ID" envDefault:"shard-1"`
	Concurrency    int           `env:"COLLECTOR_CONCURRENCY" envDefault:"256"`
	PollTimeout    time.Duration `env:"COLLECTOR_POLL_TIMEOUT" envDefault:"5s"`
	ConsumerName   string        `env:"COLLECTOR_CONSUMER" envDefault:"collector"`
	QueueBatchSize int           `env:"COLLECTOR_BATCH" envDefault:"64"`
}

