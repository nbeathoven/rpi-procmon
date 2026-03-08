package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nbeathoven/rpi-procmon/internal/api"
	"github.com/nbeathoven/rpi-procmon/internal/command"
	"github.com/nbeathoven/rpi-procmon/internal/config"
	"github.com/nbeathoven/rpi-procmon/internal/engine"
	"github.com/nbeathoven/rpi-procmon/internal/logging"
)

var appVersion = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "rpi-procmon: config load failed: %v\n", err)
		os.Exit(1)
	}

	logger, closeLog, err := logging.Open(cfg.LogFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rpi-procmon: log open failed: %v\n", err)
		os.Exit(1)
	}
	defer closeLog()

	manager, err := engine.NewManager(cfg, logger, command.ShellRunner{}, appVersion)
	if err != nil {
		logging.Logf(logger, "startup failed: %v", err)
		os.Exit(1)
	}

	logging.Logf(logger, "start: version=%s listen=%s monitors=%d config_file=%s", appVersion, cfg.API.ListenAddress, len(cfg.Monitors), cfg.ConfigFile)

	apiServer := api.NewServer(cfg, manager)
	apiErrCh := make(chan error, 1)
	go func() {
		err := apiServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			apiErrCh <- err
			return
		}
		apiErrCh <- nil
	}()

	manager.Start(ctx)

	select {
	case <-ctx.Done():
	case err := <-apiErrCh:
		if err != nil {
			logging.Logf(logger, "api server failed: %v", err)
			stop()
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := apiServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logging.Logf(logger, "api shutdown failed: %v", err)
	}
	logging.Logf(logger, "stopped")
}
