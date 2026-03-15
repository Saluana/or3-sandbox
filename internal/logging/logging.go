package logging

import (
	"log/slog"
	"os"
)

// New returns the default JSON logger used by the commands in this repository.
func New() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}
