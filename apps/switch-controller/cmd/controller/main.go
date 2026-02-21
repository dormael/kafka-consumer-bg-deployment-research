package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/dormael/kafka-consumer-bg-deployment-research/apps/switch-controller/internal/controller"
	"github.com/dormael/kafka-consumer-bg-deployment-research/apps/switch-controller/internal/health"
	"github.com/dormael/kafka-consumer-bg-deployment-research/apps/switch-controller/internal/lease"
	"github.com/dormael/kafka-consumer-bg-deployment-research/apps/switch-controller/internal/metrics"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	logger.Info("starting switch-controller")

	// Parse configuration from environment variables.
	cfg := controller.Config{
		Namespace:     envOrDefault("NAMESPACE", "kafka-bg-test"),
		ConfigMapName: envOrDefault("CONFIGMAP_NAME", "kafka-consumer-active-version"),
		BlueService:   envOrDefault("BLUE_SERVICE", "consumer-blue-svc"),
		GreenService:  envOrDefault("GREEN_SERVICE", "consumer-green-svc"),
		LeaseName:     envOrDefault("LEASE_NAME", "bg-consumer-active-lease"),
		LifecyclePort: envOrDefault("LIFECYCLE_PORT", "8080"),
	}

	drainTimeoutSec, err := strconv.Atoi(envOrDefault("DRAIN_TIMEOUT_SECONDS", "10"))
	if err != nil {
		logger.Error("invalid DRAIN_TIMEOUT_SECONDS", "error", err)
		os.Exit(1)
	}
	cfg.DrainTimeout = time.Duration(drainTimeoutSec) * time.Second

	healthCheckIntervalMs, err := strconv.Atoi(envOrDefault("HEALTH_CHECK_INTERVAL_MS", "500"))
	if err != nil {
		logger.Error("invalid HEALTH_CHECK_INTERVAL_MS", "error", err)
		os.Exit(1)
	}
	cfg.HealthCheckInterval = time.Duration(healthCheckIntervalMs) * time.Millisecond

	logger.Info("configuration loaded",
		"namespace", cfg.Namespace,
		"configmap", cfg.ConfigMapName,
		"blue_service", cfg.BlueService,
		"green_service", cfg.GreenService,
		"drain_timeout", cfg.DrainTimeout,
		"health_check_interval", cfg.HealthCheckInterval,
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

	// Initialize components.
	m := metrics.New()
	leaseManager := lease.NewLeaseManager(clientset, cfg.Namespace, cfg.LeaseName, logger)
	healthChecker := health.NewHealthChecker(logger)

	switchController := controller.NewSwitchController(
		clientset,
		cfg,
		leaseManager,
		healthChecker,
		m,
		logger,
	)

	// Set up context with signal handling for graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var wg sync.WaitGroup

	// Start Prometheus metrics server on :9090.
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsServer := &http.Server{
		Addr:              ":9090",
		Handler:           metricsMux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Info("starting metrics server", "addr", ":9090")
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("metrics server error", "error", err)
		}
	}()

	// Start health check server on :8081.
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	healthMux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	healthServer := &http.Server{
		Addr:              ":8081",
		Handler:           healthMux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Info("starting health server", "addr", ":8081")
		if err := healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("health server error", "error", err)
		}
	}()

	// Start the switch controller.
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := switchController.Run(ctx); err != nil {
			logger.Error("switch controller error", "error", err)
		}
	}()

	// Wait for shutdown signal.
	sig := <-sigCh
	logger.Info("received shutdown signal", "signal", sig)
	cancel()

	// Graceful shutdown of HTTP servers.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := metricsServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("metrics server shutdown error", "error", err)
	}
	if err := healthServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("health server shutdown error", "error", err)
	}

	wg.Wait()
	logger.Info("switch-controller stopped")
}

// envOrDefault returns the value of the environment variable or the default.
func envOrDefault(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}
