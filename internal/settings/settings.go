package settings

import "time"

type Scanner struct {
	Concurrency int           `json:"concurrency"`
	DialTimeout time.Duration `json:"dial_timeout"`
	HTTPTimeout time.Duration `json:"http_timeout"`
}

type Subnet struct {
	CIDR    string `json:"cidr"`
	Enabled bool   `json:"enabled"`
	Note    string `json:"note"`
}

type EmbeddedNATS struct {
	Enabled  bool   `json:"enabled"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	HTTPPort int    `json:"http_port"`
	StoreDir string `json:"store_dir"`
}

type Settings struct {
	Version int `json:"version"`

	HTTPAddr string `json:"http_addr"`

	NATSURL    string `json:"nats_url"`
	NATSPrefix string `json:"nats_prefix"`

	EmbeddedNATS EmbeddedNATS `json:"embedded_nats"`

	Scanner Scanner  `json:"scanner"`
	Subnets []Subnet `json:"subnets"`

	// Scanner probes
	TryDefaultCreds bool `json:"try_default_creds"`

	// Encrypted credentials (stored in settings.json, secrets encrypted with data/secret.key)
	Credentials []Credential `json:"credentials,omitempty"`
}

type Credential struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Vendor   string `json:"vendor"`            // antminer/whatsminer/...
	Firmware string `json:"firmware,omitempty"` // stock/vnish/custom/...
	Enabled  bool   `json:"enabled"`
	Priority int    `json:"priority"`
	Note     string `json:"note,omitempty"`

	UsernameEnc string `json:"username_enc,omitempty"`
	PasswordEnc string `json:"password_enc,omitempty"`
}

func Defaults() Settings {
	return Settings{
		Version:  1,
		HTTPAddr: ":8080",

		// Matches ARCHITECTURE.md docker mapping: host 14222 -> container 4222
		NATSURL:    "nats://127.0.0.1:14222",
		NATSPrefix: "mona",

		EmbeddedNATS: EmbeddedNATS{
			Enabled:  true,
			Host:     "127.0.0.1",
			Port:     14222,
			HTTPPort: 18222,
			StoreDir: "data/nats",
		},

		Scanner: Scanner{
			Concurrency: 256,
			DialTimeout: 600 * time.Millisecond,
			HTTPTimeout: 1 * time.Second,
		},
		Subnets: nil,

		TryDefaultCreds: false,

		Credentials: nil,
	}
}
