package watcher

import (
	"context"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/dormael/kafka-consumer-bg-deployment-research/apps/switch-sidecar/internal/lifecycle"
	"github.com/dormael/kafka-consumer-bg-deployment-research/apps/switch-sidecar/internal/metrics"
)

// ConfigMapWatcher watches a ConfigMap for "active" field changes and sends
// lifecycle commands to the local consumer based on whether the active color
// matches MY_COLOR.
type ConfigMapWatcher struct {
	client          kubernetes.Interface
	namespace       string
	configMapName   string
	myColor         string
	lifecycleClient *lifecycle.Client
	metrics         *metrics.Metrics
	logger          *slog.Logger
	lastActive      string
}

// NewConfigMapWatcher creates a new ConfigMapWatcher.
func NewConfigMapWatcher(
	client kubernetes.Interface,
	namespace string,
	configMapName string,
	myColor string,
	lifecycleClient *lifecycle.Client,
	m *metrics.Metrics,
	logger *slog.Logger,
) *ConfigMapWatcher {
	return &ConfigMapWatcher{
		client:          client,
		namespace:       namespace,
		configMapName:   configMapName,
		myColor:         myColor,
		lifecycleClient: lifecycleClient,
		metrics:         m,
		logger: logger.With(
			"component", "configmap-watcher",
			"configmap", configMapName,
			"my_color", myColor,
		),
	}
}

// Watch starts watching the ConfigMap using an informer.
// It blocks until the context is cancelled.
func (w *ConfigMapWatcher) Watch(ctx context.Context) {
	w.logger.Info("starting configmap watcher",
		"namespace", w.namespace,
		"configmap", w.configMapName,
		"my_color", w.myColor,
	)

	watchList := cache.NewListWatchFromClient(
		w.client.CoreV1().RESTClient(),
		"configmaps",
		w.namespace,
		fields.OneTermEqualSelector("metadata.name", w.configMapName),
	)

	_, informer := cache.NewInformer(
		watchList,
		&corev1.ConfigMap{},
		0,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				cm, ok := obj.(*corev1.ConfigMap)
				if !ok {
					return
				}
				w.handleConfigMapChange(cm)
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				oldCM, ok1 := oldObj.(*corev1.ConfigMap)
				newCM, ok2 := newObj.(*corev1.ConfigMap)
				if !ok1 || !ok2 {
					w.logger.Error("failed to cast ConfigMap objects in update handler")
					return
				}

				oldActive := oldCM.Data["active"]
				newActive := newCM.Data["active"]

				if oldActive != newActive {
					w.logger.Info("active color changed in configmap", "from", oldActive, "to", newActive)
					w.handleConfigMapChange(newCM)
				}
			},
		},
	)

	informer.Run(ctx.Done())
}

// handleConfigMapChange processes a ConfigMap change by comparing the active
// color with MY_COLOR and sending the appropriate lifecycle command.
func (w *ConfigMapWatcher) handleConfigMapChange(cm *corev1.ConfigMap) {
	activeColor := cm.Data["active"]
	if activeColor == "" {
		w.logger.Warn("active color is empty in configmap")
		return
	}

	// Avoid sending duplicate commands.
	if activeColor == w.lastActive {
		w.logger.Debug("active color unchanged, skipping", "active", activeColor)
		return
	}
	w.lastActive = activeColor
	w.metrics.ConfigMapUpdatesTotal.Inc()

	if activeColor == w.myColor {
		w.logger.Info("I am the active color, sending resume", "active", activeColor, "my_color", w.myColor)
		w.metrics.LifecycleCommandsTotal.WithLabelValues("resume").Inc()
		w.metrics.CurrentState.Set(1) // active
		if err := w.lifecycleClient.Resume(); err != nil {
			w.logger.Error("failed to send resume command", "error", err)
			w.metrics.LifecycleCommandErrors.WithLabelValues("resume").Inc()
		}
	} else {
		w.logger.Info("I am not the active color, sending pause", "active", activeColor, "my_color", w.myColor)
		w.metrics.LifecycleCommandsTotal.WithLabelValues("pause").Inc()
		w.metrics.CurrentState.Set(0) // paused
		if err := w.lifecycleClient.Pause(); err != nil {
			w.logger.Error("failed to send pause command", "error", err)
			w.metrics.LifecycleCommandErrors.WithLabelValues("pause").Inc()
		}
	}
}
