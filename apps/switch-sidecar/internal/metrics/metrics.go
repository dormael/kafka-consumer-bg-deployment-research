package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds Prometheus metrics for the Switch Sidecar.
type Metrics struct {
	LifecycleCommandsTotal *prometheus.CounterVec
	LifecycleCommandErrors *prometheus.CounterVec
	ConfigMapUpdatesTotal  prometheus.Counter
	CurrentState           prometheus.Gauge
}

// New creates and registers all sidecar Prometheus metrics.
func New() *Metrics {
	return &Metrics{
		LifecycleCommandsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "sidecar_lifecycle_commands_total",
			Help: "Total number of lifecycle commands sent to the consumer.",
		}, []string{"command"}),
		LifecycleCommandErrors: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "sidecar_lifecycle_command_errors_total",
			Help: "Total number of failed lifecycle commands.",
		}, []string{"command"}),
		ConfigMapUpdatesTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "sidecar_configmap_updates_total",
			Help: "Total number of ConfigMap update events received.",
		}),
		CurrentState: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "sidecar_current_state",
			Help: "Current state of this sidecar (0=paused, 1=active).",
		}),
	}
}
