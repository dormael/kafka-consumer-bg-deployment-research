package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all Prometheus metrics for the Switch Controller.
type Metrics struct {
	SwitchDuration      prometheus.Histogram
	SwitchTotal         prometheus.Counter
	SwitchSuccessTotal  prometheus.Counter
	SwitchRollbackTotal prometheus.Counter
	ActiveColor         prometheus.Gauge
	DualActiveDetected  prometheus.Counter
}

// New creates and registers all Prometheus metrics.
func New() *Metrics {
	return &Metrics{
		SwitchDuration: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "bg_switch_duration_seconds",
			Help:    "Duration of blue/green switch operations in seconds.",
			Buckets: []float64{0.5, 1, 2, 3, 5, 10, 15, 30},
		}),
		SwitchTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "bg_switch_total",
			Help: "Total number of blue/green switch attempts.",
		}),
		SwitchSuccessTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "bg_switch_success_total",
			Help: "Total number of successful blue/green switches.",
		}),
		SwitchRollbackTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "bg_switch_rollback_total",
			Help: "Total number of blue/green switch rollbacks.",
		}),
		ActiveColor: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "bg_switch_active_color",
			Help: "Currently active color (0=blue, 1=green).",
		}),
		DualActiveDetected: promauto.NewCounter(prometheus.CounterOpts{
			Name: "bg_switch_dual_active_detected",
			Help: "Total number of dual-active situations detected.",
		}),
	}
}

// SetActiveColor sets the active color gauge. "blue" = 0, "green" = 1.
func (m *Metrics) SetActiveColor(color string) {
	switch color {
	case "blue":
		m.ActiveColor.Set(0)
	case "green":
		m.ActiveColor.Set(1)
	}
}
