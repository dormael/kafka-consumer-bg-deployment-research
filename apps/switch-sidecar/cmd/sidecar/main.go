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
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/dormael/kafka-consumer-bg-deployment-research/apps/switch-sidecar/internal/lifecycle"
	"github.com/dormael/kafka-consumer-bg-deployment-research/apps/switch-sidecar/internal/metrics"
	"github.com/dormael/kafka-consumer-bg-deployment-research/apps/switch-sidecar/internal/watcher"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// Parse configuration from environment variables.
	myColor := envOrDefault("MY_COLOR", "blue")
	myPodName := envOrDefault("MY_POD_NAME", "unknown")
	myNamespace := envOrDefault("MY_NAMESPACE", "kafka-bg-test")
	consumerLifecycleURL := envOrDefault("CONSUMER_LIFECYCLE_URL", "http://localhost:8080")
	configMapName := envOrDefault("CONFIGMAP_NAME", "kafka-consumer-active-version")

	logger.Info("starting switch-sidecar",
		"my_color", myColor,
		"my_pod_name", myPodName,
		"my_namespace", myNamespace,
		"consumer_lifecycle_url", consumerLifecycleURL,
		"configmap_name", configMapName,
	)

	// Initialize Kubernetes client using in-cluster config.
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		logger.Error("failed to get in-cluster config", "error", err)
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		logger.Error("failed to create kubernetes client", "error", err)
		os.Exit(1)
	}

	// Initialize lifecycle client.
	lifecycleClient := lifecycle.NewClient(consumerLifecycleURL, logger)

	// Initialize metrics (registers Prometheus collectors via promauto).
	m := metrics.New()

	// Initialize ConfigMap watcher.
	cmWatcher := watcher.NewConfigMapWatcher(
		clientset,
		myNamespace,
		configMapName,
		myColor,
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

	// Start ConfigMap watcher.
	wg.Add(1)
	go func() {
		defer wg.Done()
		cmWatcher.Watch(ctx)
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
