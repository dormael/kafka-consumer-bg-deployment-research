package reconciler

// DesiredState represents the desired lifecycle state and optional fault
// injection configuration for a Consumer pod.
type DesiredState struct {
	Lifecycle string       `json:"lifecycle"`
	Fault     *FaultConfig `json:"fault,omitempty"`
}

// FaultConfig represents fault injection settings to apply to the Consumer.
type FaultConfig struct {
	ProcessingDelayMs int64 `json:"processingDelayMs,omitempty"`
	ErrorRatePercent  int   `json:"errorRatePercent,omitempty"`
	CommitDelayMs     int64 `json:"commitDelayMs,omitempty"`
}

// Equal returns true if two DesiredState values are equivalent.
func (d *DesiredState) Equal(other *DesiredState) bool {
	if d == nil || other == nil {
		return d == other
	}
	if d.Lifecycle != other.Lifecycle {
		return false
	}
	return d.Fault.Equal(other.Fault)
}

// Equal returns true if two FaultConfig values are equivalent.
func (f *FaultConfig) Equal(other *FaultConfig) bool {
	if f == nil && other == nil {
		return true
	}
	if f == nil || other == nil {
		return false
	}
	return f.ProcessingDelayMs == other.ProcessingDelayMs &&
		f.ErrorRatePercent == other.ErrorRatePercent &&
		f.CommitDelayMs == other.CommitDelayMs
}
