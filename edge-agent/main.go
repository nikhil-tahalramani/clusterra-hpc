package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	cfg, err := configFromEnv()
	if err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}

	// Start a minimal HTTP server for the liveness probe
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		mux.HandleFunc("GET /logs/{jobId}", handleGetLog)
		mux.HandleFunc("GET /logs/{jobId}/stream", handleStreamLog)
		// Private-repo credential staging from cluster-api. Writes a
		// per-job credential bundle into EFS for the slurmd Prolog to
		// pick up. See git_creds.go for the contract.
		mux.HandleFunc("POST /internal/agent/git-credentials", handleStageGitCreds)
		if err := http.ListenAndServe(":6001", mux); err != nil {
			slog.Error("health server error", "err", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := run(ctx, cfg); err != nil {
		slog.Error("agent exited with error", "err", err)
		os.Exit(1)
	}
	slog.Info("agent stopped")
}
