package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/vm75/message-sync/internal/app"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/safelog"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/version"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Println(version.Build)
			return
		case "validate-config":
			dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
			if dataDir == "" {
				dataDir = "/data"
			}
			st, err := store.Open(context.Background(), filepath.Join(dataDir, app.SyncDBName))
			if err != nil {
				fmt.Fprintln(os.Stderr, "failed to open sync database")
				os.Exit(1)
			}
			defer st.Close()
			cfg, err := config.Load(context.Background(), st.DB())
			if err != nil {
				fmt.Fprintln(os.Stderr, "invalid configuration")
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

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, nil, logger); err != nil {
		safelog.Error(logger, "service stopped", "run", err)
		os.Exit(1)
	}
}
