package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ai-volund/volund-agent/internal/config"
	"github.com/ai-volund/volund-agent/internal/runtime"
)

var version = "dev"

func main() {
	cfg := config.Load()

	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	slog.Info("starting volund-agent",
		"version", version,
		"agent_id", cfg.AgentID,
		"profile", cfg.ProfileName,
		"profile_type", cfg.ProfileType,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle OS signals for graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	rt := runtime.New(cfg)

	go func() {
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)
		cancel()
	}()

	if err := rt.Start(ctx); err != nil {
		slog.Error("runtime exited with error", "error", err)
		os.Exit(1)
	}

	if err := rt.Stop(); err != nil {
		slog.Error("error during shutdown", "error", err)
		os.Exit(1)
	}

	slog.Info("volund-agent stopped")
}
