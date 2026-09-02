// Package logging builds the structured JSON logger used by every process.
// Log lines carry service/version/environment and correlation IDs only; they
// must never carry personal data, tokens or request bodies (v1.2 section 30.2).
package logging

import (
	"log/slog"
	"os"

	"github.com/celikbros/kapsora/internal/platform/config"
)

// New returns a JSON slog.Logger configured from cfg.
func New(cfg config.Config) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(handler).With(
		slog.String("service", cfg.ServiceName),
		slog.String("version", cfg.Version),
		slog.String("environment", cfg.Environment),
	)
}
