package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/clock"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/config"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/httpapi"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/logging"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/runtimeconfig"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/workflowcheck"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "fermentctl:", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		return usageError()
	}
	switch arguments[0] {
	case "serve":
		if len(arguments) != 1 {
			return usageError()
		}
		return serve()
	case "check":
		if len(arguments) != 2 {
			return usageError()
		}
		return workflowcheck.Run(arguments[1])
	default:
		return usageError()
	}
}

func usageError() error {
	return errors.New("usage: fermentctl serve | fermentctl check <workflow>")
}

func serve() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	if err := cfg.EnsureDataDir(); err != nil {
		return err
	}
	store, err := storage.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	runtimeStore, err := runtimeconfig.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	active := runtimeStore.CurrentView()
	logger := logging.New(os.Stdout, logging.Level(active.LogLevel))
	logger.Infof("runtime configuration loaded: revision=%d timeout=%s body_limit=%d level=%s",
		active.Revision, active.RequestTimeout, active.MaxRequestBodyBytes, active.LogLevel)

	app := service.New(store, clock.System{}, runtimeStore, logger)
	app.OnRuntimeConfigChange(func(view runtimeconfig.View) {
		logger.SetLevel(logging.Level(view.LogLevel))
	})
	server := &http.Server{
		Addr:              cfg.Address,
		Handler:           httpapi.NewHandler(app, logger),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("graceful shutdown failed: %v", err)
		}
	}()

	log.Printf("cellar run control listening on %s with data %s", cfg.Address, cfg.DataDir)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("serve HTTP: %w", err)
}

func init() {
	log.SetFlags(log.LstdFlags | log.LUTC)
}
