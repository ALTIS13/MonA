package embeddednats

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	natssrv "github.com/nats-io/nats-server/v2/server"
)

type Config struct {
	Host     string
	Port     int
	HTTPPort int
	StoreDir string
}

type Server struct {
	s *natssrv.Server
}

func Start(cfg Config) (*Server, error) {
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	if cfg.Port == 0 {
		cfg.Port = 14222
	}
	if cfg.HTTPPort == 0 {
		cfg.HTTPPort = 18222
	}
	if cfg.StoreDir == "" {
		cfg.StoreDir = "data/nats"
	}
	if err := os.MkdirAll(cfg.StoreDir, 0o755); err != nil {
		return nil, err
	}
	abs, _ := filepath.Abs(cfg.StoreDir)

	opts := &natssrv.Options{
		ServerName: "mona-embedded-nats",
		Host:       cfg.Host,
		Port:       cfg.Port,
		HTTPHost:   cfg.Host,
		HTTPPort:   cfg.HTTPPort,

		JetStream: true,
		StoreDir:  abs,

		NoSigs: true,
		// Keep embedded quiet; app logger will expose status via /api/status
		NoLog:   true,
	}

	s, err := natssrv.NewServer(opts)
	if err != nil {
		return nil, err
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		s.Shutdown()
		return nil, fmt.Errorf("embedded nats not ready on %s:%d", cfg.Host, cfg.Port)
	}
	return &Server{s: s}, nil
}

func (s *Server) Shutdown() {
	if s == nil || s.s == nil {
		return
	}
	s.s.Shutdown()
}

