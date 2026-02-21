package controller

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/dormael/kafka-consumer-bg-deployment-research/apps/switch-controller/internal/health"
	"github.com/dormael/kafka-consumer-bg-deployment-research/apps/switch-controller/internal/lease"
	"github.com/dormael/kafka-consumer-bg-deployment-research/apps/switch-controller/internal/metrics"
)

const (
	// DefaultDrainTimeout is the default time to wait for pods to reach PAUSED state.
	DefaultDrainTimeout = 10 * time.Second
	// DefaultHealthCheckInterval is the default interval for health check polling.
	DefaultHealthCheckInterval = 500 * time.Millisecond
	// DefaultLifecyclePort is the default port for the consumer lifecycle HTTP endpoint.
	DefaultLifecyclePort = "8080"
)

// Config holds the configuration for the SwitchController.
type Config struct {
	Namespace           string
	ConfigMapName       string
	BlueService         string
	GreenService        string
	LeaseName           string
	DrainTimeout        time.Duration
	HealthCheckInterval time.Duration
	LifecyclePort       string
}

// SwitchController watches a ConfigMap for "active" field changes and
// orchestrates the blue/green switch sequence using the
// "Pause First, Resume Second" principle.
type SwitchController struct {
	client        kubernetes.Interface
	config        Config
	leaseManager  *lease.LeaseManager
	healthChecker *health.HealthChecker
	metrics       *metrics.Metrics
	logger        *slog.Logger
	lastActive    string
}

// NewSwitchController creates a new SwitchController.
func NewSwitchController(
	client kubernetes.Interface,
	config Config,
	leaseManager *lease.LeaseManager,
	healthChecker *health.HealthChecker,
	m *metrics.Metrics,
	logger *slog.Logger,
) *SwitchController {
	if config.DrainTimeout == 0 {
		config.DrainTimeout = DefaultDrainTimeout
	}
	if config.HealthCheckInterval == 0 {
		config.HealthCheckInterval = DefaultHealthCheckInterval
	}
	if config.LifecyclePort == "" {
		config.LifecyclePort = DefaultLifecyclePort
	}

	return &SwitchController{
		client:        client,
		config:        config,
		leaseManager:  leaseManager,
		healthChecker: healthChecker,
		metrics:       m,
		logger:        logger.With("component", "switch-controller"),
	}
}

// Run starts watching the ConfigMap and processes switch events.
// It blocks until the context is cancelled.
func (sc *SwitchController) Run(ctx context.Context) error {
	sc.logger.Info("starting switch controller",
		"namespace", sc.config.Namespace,
		"configmap", sc.config.ConfigMapName,
		"blue_service", sc.config.BlueService,
		"green_service", sc.config.GreenService,
	)

	// Read initial state from ConfigMap.
	if err := sc.initializeActiveColor(ctx); err != nil {
		sc.logger.Warn("failed to read initial active color", "error", err)
	}

	watchList := cache.NewListWatchFromClient(
		sc.client.CoreV1().RESTClient(),
		"configmaps",
		sc.config.Namespace,
		fields.OneTermEqualSelector("metadata.name", sc.config.ConfigMapName),
	)

	_, informer := cache.NewInformer(
		watchList,
		&corev1.ConfigMap{},
		0,
		cache.ResourceEventHandlerFuncs{
			UpdateFunc: func(oldObj, newObj interface{}) {
				oldCM, ok1 := oldObj.(*corev1.ConfigMap)
				newCM, ok2 := newObj.(*corev1.ConfigMap)
				if !ok1 || !ok2 {
					sc.logger.Error("failed to cast ConfigMap objects")
					return
				}

				oldActive := oldCM.Data["active"]
				newActive := newCM.Data["active"]

				if oldActive != newActive {
					sc.logger.Info("active color changed", "from", oldActive, "to", newActive)
					sc.handleSwitch(ctx, oldActive, newActive)
				}
			},
			AddFunc: func(obj interface{}) {
				cm, ok := obj.(*corev1.ConfigMap)
				if !ok {
					return
				}
				active := cm.Data["active"]
				if active != "" && active != sc.lastActive {
					sc.logger.Info("configmap added with active color", "active", active)
					sc.metrics.SetActiveColor(active)
					sc.lastActive = active
				}
			},
		},
	)

	informer.Run(ctx.Done())
	return nil
}

// initializeActiveColor reads the current active color from the ConfigMap.
func (sc *SwitchController) initializeActiveColor(ctx context.Context) error {
	cm, err := sc.client.CoreV1().ConfigMaps(sc.config.Namespace).Get(ctx, sc.config.ConfigMapName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get configmap %s: %w", sc.config.ConfigMapName, err)
	}

	active := cm.Data["active"]
	if active != "" {
		sc.lastActive = active
		sc.metrics.SetActiveColor(active)
		sc.logger.Info("initialized active color", "active", active)
	}
	return nil
}

// handleSwitch orchestrates the full switch sequence from oldColor to newColor.
func (sc *SwitchController) handleSwitch(ctx context.Context, oldColor, newColor string) {
	startTime := time.Now()
	sc.metrics.SwitchTotal.Inc()

	sc.logger.Info("starting switch sequence", "from", oldColor, "to", newColor)

	// Step 1: Acquire Lease.
	if err := sc.leaseManager.AcquireLease(ctx, fmt.Sprintf("switch-%s-to-%s", oldColor, newColor)); err != nil {
		sc.logger.Error("failed to acquire lease, aborting switch", "error", err)
		return
	}

	// Step 2: Get endpoints for old (current active) and new services.
	oldServiceName := sc.serviceForColor(oldColor)
	newServiceName := sc.serviceForColor(newColor)

	oldEndpoints, err := sc.getServiceEndpoints(ctx, oldServiceName)
	if err != nil {
		sc.logger.Error("failed to get old service endpoints", "service", oldServiceName, "error", err)
		sc.releaseLease(ctx)
		return
	}

	newEndpoints, err := sc.getServiceEndpoints(ctx, newServiceName)
	if err != nil {
		sc.logger.Error("failed to get new service endpoints", "service", newServiceName, "error", err)
		sc.releaseLease(ctx)
		return
	}

	sc.logger.Info("resolved endpoints", "old_endpoints", len(oldEndpoints), "new_endpoints", len(newEndpoints))

	// Step 3: Pause current active (old color).
	sc.logger.Info("pausing current active pods", "color", oldColor)
	if err := sc.healthChecker.SendLifecycleCommand(oldEndpoints, "pause"); err != nil {
		sc.logger.Error("failed to pause old pods, aborting switch", "color", oldColor, "error", err)
		sc.resumeAndAbort(ctx, oldEndpoints, oldColor)
		return
	}

	// Step 4: Wait for all old pods to reach PAUSED state.
	sc.logger.Info("waiting for old pods to reach PAUSED state", "color", oldColor, "timeout", sc.config.DrainTimeout)
	if err := sc.healthChecker.WaitForState(ctx, oldEndpoints, "PAUSED", sc.config.DrainTimeout, sc.config.HealthCheckInterval); err != nil {
		sc.logger.Error("timeout waiting for old pods to pause, rolling back", "color", oldColor, "error", err)
		sc.metrics.SwitchRollbackTotal.Inc()
		sc.resumeAndAbort(ctx, oldEndpoints, oldColor)
		return
	}

	// Step 5: Resume new active (new color).
	sc.logger.Info("resuming new active pods", "color", newColor)
	if err := sc.healthChecker.SendLifecycleCommand(newEndpoints, "resume"); err != nil {
		sc.logger.Error("failed to resume new pods, executing rollback", "color", newColor, "error", err)
		sc.rollback(ctx, oldEndpoints, newEndpoints, oldColor, newColor)
		sc.releaseLease(ctx)
		return
	}

	// Step 6: Verify all new pods reach ACTIVE state.
	sc.logger.Info("waiting for new pods to reach ACTIVE state", "color", newColor)
	if err := sc.healthChecker.WaitForState(ctx, newEndpoints, "ACTIVE", sc.config.DrainTimeout, sc.config.HealthCheckInterval); err != nil {
		sc.logger.Error("new pods failed to become ACTIVE, executing rollback", "color", newColor, "error", err)
		sc.rollback(ctx, oldEndpoints, newEndpoints, oldColor, newColor)
		sc.releaseLease(ctx)
		return
	}

	// Step 7: Detect dual-active.
	if err := sc.detectDualActive(ctx, oldEndpoints, newEndpoints); err != nil {
		sc.logger.Error("dual active detected after switch, executing rollback", "error", err)
		sc.rollback(ctx, oldEndpoints, newEndpoints, oldColor, newColor)
		sc.releaseLease(ctx)
		return
	}

	// Step 8: Update Lease holder to new color.
	if err := sc.leaseManager.AcquireLease(ctx, newColor); err != nil {
		sc.logger.Error("failed to update lease holder", "color", newColor, "error", err)
	}

	// Step 9: Release Lease after successful switch.
	sc.releaseLease(ctx)

	// Success: record metrics.
	duration := time.Since(startTime)
	sc.metrics.SwitchDuration.Observe(duration.Seconds())
	sc.metrics.SwitchSuccessTotal.Inc()
	sc.metrics.SetActiveColor(newColor)
	sc.lastActive = newColor

	sc.logger.Info("switch completed successfully",
		"from", oldColor,
		"to", newColor,
		"duration_seconds", duration.Seconds(),
	)
}

// rollback executes a full rollback: pause new, resume old, restore ConfigMap.
func (sc *SwitchController) rollback(ctx context.Context, oldEndpoints, newEndpoints []string, oldColor, newColor string) {
	sc.logger.Info("executing rollback", "restoring", oldColor, "pausing", newColor)
	sc.metrics.SwitchRollbackTotal.Inc()

	// Step 1: Pause new (Green) pods.
	if err := sc.healthChecker.SendLifecycleCommand(newEndpoints, "pause"); err != nil {
		sc.logger.Error("rollback: failed to pause new pods", "color", newColor, "error", err)
	}

	// Step 2: Wait for new pods to reach PAUSED state.
	if err := sc.healthChecker.WaitForState(ctx, newEndpoints, "PAUSED", sc.config.DrainTimeout, sc.config.HealthCheckInterval); err != nil {
		sc.logger.Error("rollback: new pods did not reach PAUSED state", "color", newColor, "error", err)
	}

	// Step 3: Resume old (Blue) pods.
	if err := sc.healthChecker.SendLifecycleCommand(oldEndpoints, "resume"); err != nil {
		sc.logger.Error("rollback: failed to resume old pods", "color", oldColor, "error", err)
	}

	// Step 4: Wait for old pods to reach ACTIVE state.
	if err := sc.healthChecker.WaitForState(ctx, oldEndpoints, "ACTIVE", sc.config.DrainTimeout, sc.config.HealthCheckInterval); err != nil {
		sc.logger.Error("rollback: old pods did not become ACTIVE", "color", oldColor, "error", err)
	}

	// Step 5: Restore ConfigMap to old color.
	sc.restoreConfigMap(ctx, oldColor)

	// Step 6: Update Lease holder to old color.
	if err := sc.leaseManager.AcquireLease(ctx, oldColor); err != nil {
		sc.logger.Error("rollback: failed to update lease holder", "color", oldColor, "error", err)
	}

	sc.metrics.SetActiveColor(oldColor)
	sc.lastActive = oldColor

	sc.logger.Info("rollback completed", "active", oldColor)
}

// resumeAndAbort sends resume to the given endpoints and releases the lease.
// Used when the pause step fails and we need to restore the old active.
func (sc *SwitchController) resumeAndAbort(ctx context.Context, endpoints []string, color string) {
	sc.logger.Info("aborting switch, resuming pods", "color", color)

	if err := sc.healthChecker.SendLifecycleCommand(endpoints, "resume"); err != nil {
		sc.logger.Error("failed to resume pods during abort", "color", color, "error", err)
	}

	sc.releaseLease(ctx)
}

// restoreConfigMap updates the ConfigMap to set the "active" field back to the
// specified color. This is used during rollback.
func (sc *SwitchController) restoreConfigMap(ctx context.Context, color string) {
	cm, err := sc.client.CoreV1().ConfigMaps(sc.config.Namespace).Get(ctx, sc.config.ConfigMapName, metav1.GetOptions{})
	if err != nil {
		sc.logger.Error("rollback: failed to get configmap for restore", "error", err)
		return
	}

	cm.Data["active"] = color
	if _, err := sc.client.CoreV1().ConfigMaps(sc.config.Namespace).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		sc.logger.Error("rollback: failed to update configmap", "color", color, "error", err)
		return
	}

	sc.logger.Info("configmap restored", "active", color)
}

// detectDualActive checks if any old pods are still ACTIVE when new pods are ACTIVE.
func (sc *SwitchController) detectDualActive(ctx context.Context, oldEndpoints, newEndpoints []string) error {
	oldStatuses, _ := sc.healthChecker.CheckPodStatus(oldEndpoints)
	newStatuses, _ := sc.healthChecker.CheckPodStatus(newEndpoints)

	oldActive := false
	newActive := false

	for _, status := range oldStatuses {
		if status == "ACTIVE" {
			oldActive = true
			break
		}
	}
	for _, status := range newStatuses {
		if status == "ACTIVE" {
			newActive = true
			break
		}
	}

	if oldActive && newActive {
		sc.logger.Error("DUAL ACTIVE DETECTED: both old and new pods are ACTIVE")
		sc.metrics.DualActiveDetected.Inc()
		return fmt.Errorf("dual active detected")
	}

	return nil
}

// releaseLease is a helper that releases the lease and logs any error.
func (sc *SwitchController) releaseLease(ctx context.Context) {
	if err := sc.leaseManager.ReleaseLease(ctx); err != nil {
		sc.logger.Error("failed to release lease", "error", err)
	}
}

// serviceForColor returns the Kubernetes Service name for the given color.
func (sc *SwitchController) serviceForColor(color string) string {
	switch color {
	case "blue":
		return sc.config.BlueService
	case "green":
		return sc.config.GreenService
	default:
		sc.logger.Warn("unknown color, defaulting to blue service", "color", color)
		return sc.config.BlueService
	}
}

// getServiceEndpoints returns the list of pod endpoints (IP:port) for the
// given Kubernetes Service by reading its Endpoints resource.
func (sc *SwitchController) getServiceEndpoints(ctx context.Context, serviceName string) ([]string, error) {
	ep, err := sc.client.CoreV1().Endpoints(sc.config.Namespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get endpoints for service %s: %w", serviceName, err)
	}

	var endpoints []string
	for _, subset := range ep.Subsets {
		for _, addr := range subset.Addresses {
			endpoints = append(endpoints, fmt.Sprintf("%s:%s", addr.IP, sc.config.LifecyclePort))
		}
	}

	if len(endpoints) == 0 {
		return nil, fmt.Errorf("no endpoints found for service %s", serviceName)
	}

	return endpoints, nil
}
