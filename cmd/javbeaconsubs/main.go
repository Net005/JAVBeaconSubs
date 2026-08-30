package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"javbeaconsubs/internal/config"
	"javbeaconsubs/internal/engine"
	"javbeaconsubs/internal/jobs"
	"javbeaconsubs/internal/server"
	"javbeaconsubs/internal/store"
)

func main() {
	configPath := flag.String("config", "config.json", "path to the JSON configuration file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration error:", err)
		os.Exit(2)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	database, err := store.Open(cfg.DatabasePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "database error:", err)
		os.Exit(2)
	}
	defer database.Close()
	runner := engine.New(cfg, logger)
	manager, err := jobs.New(cfg, runner, database, logger)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load jobs:", err)
		os.Exit(2)
	}
	api := server.New(cfg, manager, runner, logger)

	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go manager.Run(ctx)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	logger.Info("JAVBeacon Subtitles started", "listen", cfg.Listen)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
