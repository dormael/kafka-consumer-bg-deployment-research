package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/dormael/kafka-consumer-bg-deployment-research/apps/switch-sidecar/internal/lifecycle"
	"github.com/dormael/kafka-consumer-bg-deployment-research/apps/switch-sidecar/internal/metrics"
	"github.com/dormael/kafka-consumer-bg-deployment-research/apps/switch-sidecar/internal/reconciler"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// Parse configuration from environment variables.
	myColor := envOrDefault("MY_COLOR", "blue")
	myPodName := envOrDefault("MY_POD_NAME", "unknown")
	consumerLifecycleURL := envOrDefault("CONSUMER_LIFECYCLE_URL", "http://localhost:8080")
	stateFilePath := envOrDefault("STATE_FILE_PATH", reconciler.DefaultStatePath)

	logger.Info("starting switch-sidecar",
		"my_color", myColor,
		"my_pod_name", myPodName,
		"consumer_lifecycle_url", consumerLifecycleURL,
		"state_file_path", stateFilePath,
	)

	// Initialize lifecycle client.
	lifecycleClient := lifecycle.NewClient(consumerLifecycleURL, logger)

	// Initialize metrics.
	m := metrics.New()

	// Initialize Reconciler (replaces ConfigMap Watcher).
	rec := reconciler.New(
		stateFilePath,
		myPodName,
		lifecycleClient,
		m,
		logger,
	)

	// Set up context with signal handling for graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var wg sync.WaitGroup

	// Start health and metrics server on :8082.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Check consumer status to determine readiness.
		status, err := lifecycleClient.GetStatus()
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"not_ready","error":"` + err.Error() + `"}`))
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","consumer_status":"` + status + `"}`))
	})
	mux.Handle("/metrics", promhttp.Handler())

	// L2: HTTP Push endpoint for Controller → Sidecar desired state delivery.
	mux.HandleFunc("/desired-state", rec.HandleDesiredState)

	server := &http.Server{
		Addr:              ":8082",
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Info("starting health/metrics server", "addr", ":8082")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("health/metrics server error", "error", err)
		}
	}()

	// Start Reconciler (replaces ConfigMap Watcher).
	wg.Add(1)
	go func() {
		defer wg.Done()
		rec.Run(ctx)
	}()

	// Wait for shutdown signal.
	sig := <-sigCh
	logger.Info("received shutdown signal", "signal", sig)
	cancel()

	// Graceful shutdown of HTTP server.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("server shutdown error", "error", err)
	}

	wg.Wait()
	logger.Info("switch-sidecar stopped")
}

// envOrDefault returns the value of the environment variable or the default.
func envOrDefault(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}
