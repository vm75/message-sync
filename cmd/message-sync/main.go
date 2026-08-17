package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/vm75/message-sync/internal/app"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/version"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Println(version.Build)
			return
		case "validate-config":
			cfg, err := config.Load(config.PathFromEnv())
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid configuration: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("configuration valid: %d groups, %d sync sets\n", len(cfg.Groups), len(cfg.SyncSets))
			return
		case "run":
			// continue below
		default:
			fmt.Fprintf(os.Stderr, "usage: %s [run|validate-config|version]\n", os.Args[0])
			os.Exit(2)
		}
	}

	cfg, err := config.Load(config.PathFromEnv())
	if err != nil {
		fmt.Fprintf(os.Stderr, "load configuration: %v\n", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, cfg, logger); err != nil {
		logger.Error("service stopped", "error", err.Error())
		os.Exit(1)
	}
}
