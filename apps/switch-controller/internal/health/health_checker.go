package health

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const (
	// DefaultHTTPTimeout is the default timeout for HTTP health check requests.
	DefaultHTTPTimeout = 5 * time.Second
)

// StatusResponse represents the JSON response from /lifecycle/status.
type StatusResponse struct {
	State string `json:"state"`
}

// HealthChecker checks the lifecycle status of consumer pods via HTTP.
type HealthChecker struct {
	httpClient *http.Client
	logger     *slog.Logger
}

// NewHealthChecker creates a new HealthChecker with a configured HTTP client.
func NewHealthChecker(logger *slog.Logger) *HealthChecker {
	return &HealthChecker{
		httpClient: &http.Client{
			Timeout: DefaultHTTPTimeout,
		},
		logger: logger.With("component", "health-checker"),
	}
}

// CheckPodStatus calls /lifecycle/status on each endpoint and returns a map
// of endpoint to status string. Endpoints should be in the form "host:port".
func (hc *HealthChecker) CheckPodStatus(endpoints []string) (map[string]string, error) {
	results := make(map[string]string)
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error

	for _, ep := range endpoints {
		wg.Add(1)
		go func(endpoint string) {
			defer wg.Done()

			url := fmt.Sprintf("http://%s/lifecycle/status", endpoint)
			resp, err := hc.httpClient.Get(url)
			if err != nil {
				hc.logger.Warn("failed to check pod status", "endpoint", endpoint, "error", err)
				mu.Lock()
				results[endpoint] = "UNKNOWN"
				if firstErr == nil {
					firstErr = fmt.Errorf("failed to check status of %s: %w", endpoint, err)
				}
				mu.Unlock()
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				mu.Lock()
				results[endpoint] = "UNKNOWN"
				if firstErr == nil {
					firstErr = fmt.Errorf("unexpected status code %d from %s", resp.StatusCode, endpoint)
				}
				mu.Unlock()
				return
			}

			var status StatusResponse
			if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
				mu.Lock()
				results[endpoint] = "UNKNOWN"
				if firstErr == nil {
					firstErr = fmt.Errorf("failed to decode status from %s: %w", endpoint, err)
				}
				mu.Unlock()
				return
			}

			mu.Lock()
			results[endpoint] = status.State
			mu.Unlock()

			hc.logger.Debug("pod status checked", "endpoint", endpoint, "status", status.State)
		}(ep)
	}

	wg.Wait()
	return results, firstErr
}

// WaitForState polls all endpoints until every pod reaches the target state
// or the timeout expires. The pollInterval controls how often to check.
func (hc *HealthChecker) WaitForState(ctx context.Context, endpoints []string, targetState string, timeout time.Duration, pollInterval time.Duration) error {
	hc.logger.Info("waiting for target state", "target", targetState, "endpoints_count", len(endpoints), "timeout", timeout)

	deadline := time.After(timeout)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while waiting for state %s: %w", targetState, ctx.Err())
		case <-deadline:
			// Final check before timeout.
			statuses, _ := hc.CheckPodStatus(endpoints)
			return fmt.Errorf("timeout waiting for state %s: current statuses: %v", targetState, statuses)
		case <-ticker.C:
			statuses, err := hc.CheckPodStatus(endpoints)
			if err != nil {
				hc.logger.Warn("error during status poll, retrying", "error", err)
				continue
			}

			allReady := true
			for ep, status := range statuses {
				if status != targetState {
					allReady = false
					hc.logger.Debug("pod not yet in target state", "endpoint", ep, "current", status, "target", targetState)
					break
				}
			}

			if allReady && len(statuses) == len(endpoints) {
				hc.logger.Info("all pods reached target state", "target", targetState)
				return nil
			}
		}
	}
}

// SendLifecycleCommand sends a POST request to the specified lifecycle endpoint
// on all given pod endpoints. The command should be "pause" or "resume".
func (hc *HealthChecker) SendLifecycleCommand(endpoints []string, command string) error {
	hc.logger.Info("sending lifecycle command", "command", command, "endpoints_count", len(endpoints))

	var wg sync.WaitGroup
	var mu sync.Mutex
	var errors []error

	for _, ep := range endpoints {
		wg.Add(1)
		go func(endpoint string) {
			defer wg.Done()

			url := fmt.Sprintf("http://%s/lifecycle/%s", endpoint, command)
			resp, err := hc.httpClient.Post(url, "application/json", nil)
			if err != nil {
				hc.logger.Error("failed to send lifecycle command", "endpoint", endpoint, "command", command, "error", err)
				mu.Lock()
				errors = append(errors, fmt.Errorf("failed to send %s to %s: %w", command, endpoint, err))
				mu.Unlock()
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				hc.logger.Error("unexpected response for lifecycle command", "endpoint", endpoint, "command", command, "status_code", resp.StatusCode)
				mu.Lock()
				errors = append(errors, fmt.Errorf("unexpected status %d from %s for command %s", resp.StatusCode, endpoint, command))
				mu.Unlock()
				return
			}

			hc.logger.Info("lifecycle command sent successfully", "endpoint", endpoint, "command", command)
		}(ep)
	}

	wg.Wait()

	if len(errors) > 0 {
		return fmt.Errorf("failed to send %s to %d/%d endpoints: %v", command, len(errors), len(endpoints), errors[0])
	}
	return nil
}
