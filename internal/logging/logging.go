package logging

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type Config struct {
	Level string `env:"LOG_LEVEL" envDefault:"info"`
}

func New(cfg Config) (*zap.Logger, error) {
	lvl := zapcore.InfoLevel
	if err := lvl.Set(cfg.Level); err != nil {
		return nil, err
	}

	zcfg := zap.NewProductionConfig()
	zcfg.Level = zap.NewAtomicLevelAt(lvl)
	zcfg.EncoderConfig.TimeKey = "ts"
	zcfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	return zcfg.Build()
}

