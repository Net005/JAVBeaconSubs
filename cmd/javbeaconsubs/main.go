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

	"javbeaconsubs/internal/auth"
	"javbeaconsubs/internal/buildinfo"
	"javbeaconsubs/internal/config"
	"javbeaconsubs/internal/engine"
	"javbeaconsubs/internal/jobs"
	"javbeaconsubs/internal/profile"
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
	if saved, ok, loadErr := database.LoadTranslation(); loadErr != nil {
		fmt.Fprintln(os.Stderr, "load saved settings:", loadErr)
		os.Exit(2)
	} else if ok {
		if normalizeErr := config.NormalizeTranslation(&saved); normalizeErr != nil {
			fmt.Fprintln(os.Stderr, "validate saved translation settings:", normalizeErr)
			os.Exit(2)
		}
		cfg.Translation = saved
	}
	if saved, ok, loadErr := database.LoadPostProcessing(); loadErr != nil {
		fmt.Fprintln(os.Stderr, "load saved post-processing settings:", loadErr)
		os.Exit(2)
	} else if ok {
		cfg.PostProcessing = saved
	}
	if saved, ok, loadErr := database.LoadProfiles(); loadErr != nil {
		fmt.Fprintln(os.Stderr, "load profile settings:", loadErr)
		os.Exit(2)
	} else if ok {
		if normalizeErr := config.NormalizeProfiles(&saved); normalizeErr != nil {
			fmt.Fprintln(os.Stderr, "validate saved profile settings:", normalizeErr)
			os.Exit(2)
		}
		cfg.Profiles = saved
	}
	if saved, ok, loadErr := database.LoadJAVBeacon(); loadErr != nil {
		fmt.Fprintln(os.Stderr, "load javbeacon settings:", loadErr)
		os.Exit(2)
	} else if ok {
		if normalizeErr := config.NormalizeJAVBeacon(&saved); normalizeErr != nil {
			fmt.Fprintln(os.Stderr, "validate saved javbeacon settings:", normalizeErr)
			os.Exit(2)
		}
		cfg.JAVBeacon = saved
	}
	authManager, err := auth.New(database, cfg.WebUsername, cfg.WebPassword)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load web authentication:", err)
		os.Exit(2)
	}
	runner := engine.New(cfg, logger)
	recognition, glossary := runner.Catalogs()
	if saved, ok, loadErr := database.LoadRecognitionVocabulary(); loadErr != nil {
		fmt.Fprintln(os.Stderr, "load recognition vocabulary:", loadErr)
		os.Exit(2)
	} else if ok {
		if validateErr := profile.ValidateRecognition(saved); validateErr != nil {
			fmt.Fprintln(os.Stderr, "validate recognition vocabulary:", validateErr)
			os.Exit(2)
		}
		recognition = &saved
	}
	if saved, ok, loadErr := database.LoadTranslationGlossary(); loadErr != nil {
		fmt.Fprintln(os.Stderr, "load translation glossary:", loadErr)
		os.Exit(2)
	} else if ok {
		if validateErr := profile.ValidateTranslationGlossary(saved); validateErr != nil {
			fmt.Fprintln(os.Stderr, "validate translation glossary:", validateErr)
			os.Exit(2)
		}
		glossary = &saved
	}
	runner.UpdateCatalogs(recognition, glossary)
	postProcessor := jobs.NewPostProcessor(cfg.PostProcessing, logger)
	manager, err := jobs.New(cfg, runner, database, postProcessor, logger)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load jobs:", err)
		os.Exit(2)
	}
	api := server.New(cfg, manager, runner, authManager, postProcessor, database, logger)

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

	logger.Info("JAVBeacon Subtitles started", "version", buildinfo.Version, "listen", cfg.Listen)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
