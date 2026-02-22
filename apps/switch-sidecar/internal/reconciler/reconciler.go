package reconciler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dormael/kafka-consumer-bg-deployment-research/apps/switch-sidecar/internal/lifecycle"
	"github.com/dormael/kafka-consumer-bg-deployment-research/apps/switch-sidecar/internal/metrics"
)

const (
	// DefaultPollInterval is the default interval between reconcile cycles.
	DefaultPollInterval = 5 * time.Second
	// DefaultStatePath is the default path for Volume-mounted ConfigMap files.
	DefaultStatePath = "/etc/consumer-state"
)

// Reconciler periodically reads desired state and reconciles it with the
// actual consumer lifecycle state. It supports two input sources:
//
//	L2: cachedDesired — set via HTTP push from Controller (/desired-state)
//	L4: Volume Mount file — read from statePath/{myHostname}
//
// Priority: cachedDesired > file (cachedDesired is cleared on Sidecar restart).
type Reconciler struct {
	statePath       string
	myHostname      string
	lifecycleClient *lifecycle.Client
	logger          *slog.Logger
	metrics         *metrics.Metrics
	pollInterval    time.Duration
	lastApplied     *DesiredState
	cachedDesired   *DesiredState
	mu              sync.RWMutex
}

// New creates a new Reconciler.
func New(
	statePath string,
	myHostname string,
	lifecycleClient *lifecycle.Client,
	m *metrics.Metrics,
	logger *slog.Logger,
) *Reconciler {
	if statePath == "" {
		statePath = DefaultStatePath
	}

	return &Reconciler{
		statePath:       statePath,
		myHostname:      myHostname,
		lifecycleClient: lifecycleClient,
		logger: logger.With(
			"component", "reconciler",
			"hostname", myHostname,
		),
		metrics:      m,
		pollInterval: DefaultPollInterval,
	}
}

// Run starts the reconcile loop. It blocks until the context is cancelled.
func (r *Reconciler) Run(ctx context.Context) {
	r.logger.Info("starting reconciler",
		"state_path", r.statePath,
		"poll_interval", r.pollInterval,
	)

	// Run an initial reconcile immediately.
	r.reconcileOnce()

	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("reconciler stopped")
			return
		case <-ticker.C:
			r.reconcileOnce()
		}
	}
}

// HandleDesiredState is an HTTP handler for POST /desired-state.
// It receives a DesiredState JSON body from the Controller (L2 push).
func (r *Reconciler) HandleDesiredState(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		r.logger.Error("failed to read desired-state body", "error", err)
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer req.Body.Close()

	var desired DesiredState
	if err := json.Unmarshal(body, &desired); err != nil {
		r.logger.Error("failed to parse desired-state JSON", "error", err, "body", string(body))
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	r.mu.Lock()
	r.cachedDesired = &desired
	r.mu.Unlock()

	r.logger.Info("received desired state push",
		"lifecycle", desired.Lifecycle,
		"has_fault", desired.Fault != nil,
	)

	// Trigger immediate reconcile after receiving a push.
	go r.reconcileOnce()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"accepted"}`))
}

// reconcileOnce executes a single reconciliation cycle.
func (r *Reconciler) reconcileOnce() {
	// Step 1: Determine desired state (priority: cache > file).
	desired := r.determineDesiredState()
	if desired == nil {
		r.logger.Debug("no desired state available, skipping reconcile")
		return
	}

	// Step 2: Compare with lastApplied — skip if identical.
	if r.lastApplied != nil && r.lastApplied.Equal(desired) {
		r.logger.Debug("desired matches last applied, skipping",
			"lifecycle", desired.Lifecycle,
		)
		return
	}

	// Step 3: Query actual consumer status.
	actualStatus, err := r.lifecycleClient.GetStatus()
	if err != nil {
		r.logger.Warn("failed to get consumer status, will retry next cycle", "error", err)
		return
	}

	// Step 4: Compare lifecycle and apply if different.
	desiredLifecycle := strings.ToUpper(desired.Lifecycle)
	actualLifecycle := strings.ToUpper(actualStatus)

	if desiredLifecycle != actualLifecycle {
		r.logger.Info("lifecycle mismatch, applying correction",
			"desired", desiredLifecycle,
			"actual", actualLifecycle,
		)

		switch desiredLifecycle {
		case "ACTIVE":
			r.metrics.LifecycleCommandsTotal.WithLabelValues("resume").Inc()
			if err := r.lifecycleClient.Resume(); err != nil {
				r.logger.Error("failed to resume consumer", "error", err)
				r.metrics.LifecycleCommandErrors.WithLabelValues("resume").Inc()
				return
			}
			r.metrics.CurrentState.Set(1)
		case "PAUSED":
			r.metrics.LifecycleCommandsTotal.WithLabelValues("pause").Inc()
			if err := r.lifecycleClient.Pause(); err != nil {
				r.logger.Error("failed to pause consumer", "error", err)
				r.metrics.LifecycleCommandErrors.WithLabelValues("pause").Inc()
				return
			}
			r.metrics.CurrentState.Set(0)
		default:
			r.logger.Warn("unknown desired lifecycle state", "desired", desiredLifecycle)
			return
		}
	} else {
		r.logger.Debug("lifecycle already matches desired", "state", desiredLifecycle)
		if desiredLifecycle == "ACTIVE" {
			r.metrics.CurrentState.Set(1)
		} else {
			r.metrics.CurrentState.Set(0)
		}
	}

	// Step 5: Apply fault injection (if present).
	if desired.Fault != nil {
		if err := r.lifecycleClient.ApplyFaultConfig(
			desired.Fault.ProcessingDelayMs,
			desired.Fault.ErrorRatePercent,
			desired.Fault.CommitDelayMs,
		); err != nil {
			r.logger.Warn("failed to apply fault config", "error", err)
		}
	}

	// Step 6: Update lastApplied on success.
	r.lastApplied = desired
	r.metrics.ConfigMapUpdatesTotal.Inc()

	r.logger.Info("reconcile succeeded",
		"lifecycle", desired.Lifecycle,
		"has_fault", desired.Fault != nil,
	)
}

// determineDesiredState returns the desired state from the highest-priority
// source. Priority: cachedDesired (L2) > Volume Mount file (L4).
func (r *Reconciler) determineDesiredState() *DesiredState {
	// L2: Check cached desired state (from Controller HTTP push).
	r.mu.RLock()
	cached := r.cachedDesired
	r.mu.RUnlock()

	if cached != nil {
		return cached
	}

	// L4: Read from Volume Mount file.
	filePath := filepath.Join(r.statePath, r.myHostname)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			r.logger.Warn("failed to read state file", "path", filePath, "error", err)
		}
		return nil
	}

	var desired DesiredState
	if err := json.Unmarshal(data, &desired); err != nil {
		r.logger.Warn("failed to parse state file", "path", filePath, "error", err, "raw", string(data))
		return nil
	}

	return &desired
}
