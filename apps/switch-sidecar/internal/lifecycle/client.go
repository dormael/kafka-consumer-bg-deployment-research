package lifecycle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const (
	// DefaultBaseURL is the default base URL for the consumer lifecycle endpoint.
	DefaultBaseURL = "http://localhost:8080"
	// DefaultHTTPTimeout is the default timeout for HTTP requests.
	DefaultHTTPTimeout = 5 * time.Second
	// DefaultMaxRetries is the default number of retry attempts.
	DefaultMaxRetries = 3
	// DefaultRetryBaseDelay is the base delay for exponential backoff.
	DefaultRetryBaseDelay = 500 * time.Millisecond
)

// StatusResponse represents the JSON response from /lifecycle/status.
type StatusResponse struct {
	Status string `json:"status"`
}

// Client communicates with the Kafka consumer application's lifecycle
// HTTP endpoints (/lifecycle/pause, /lifecycle/resume, /lifecycle/status).
type Client struct {
	baseURL        string
	httpClient     *http.Client
	maxRetries     int
	retryBaseDelay time.Duration
	logger         *slog.Logger
}

// NewClient creates a new lifecycle Client.
func NewClient(baseURL string, logger *slog.Logger) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: DefaultHTTPTimeout,
		},
		maxRetries:     DefaultMaxRetries,
		retryBaseDelay: DefaultRetryBaseDelay,
		logger:         logger.With("component", "lifecycle-client", "base_url", baseURL),
	}
}

// Pause sends a POST request to /lifecycle/pause.
func (c *Client) Pause() error {
	c.logger.Info("sending pause command")
	return c.doWithRetry("POST", "/lifecycle/pause")
}

// Resume sends a POST request to /lifecycle/resume.
func (c *Client) Resume() error {
	c.logger.Info("sending resume command")
	return c.doWithRetry("POST", "/lifecycle/resume")
}

// GetStatus sends a GET request to /lifecycle/status and returns the status string.
func (c *Client) GetStatus() (string, error) {
	c.logger.Debug("getting lifecycle status")

	url := c.baseURL + "/lifecycle/status"
	var lastErr error

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			delay := c.retryBaseDelay * time.Duration(1<<uint(attempt-1))
			c.logger.Debug("retrying status request", "attempt", attempt, "delay", delay)
			time.Sleep(delay)
		}

		resp, err := c.httpClient.Get(url)
		if err != nil {
			lastErr = fmt.Errorf("GET %s failed: %w", url, err)
			c.logger.Warn("status request failed", "attempt", attempt, "error", err)
			continue
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()

		if readErr != nil {
			lastErr = fmt.Errorf("failed to read response body from %s: %w", url, readErr)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("unexpected status code %d from GET %s: %s", resp.StatusCode, url, string(body))
			c.logger.Warn("unexpected status code", "status_code", resp.StatusCode, "attempt", attempt)
			continue
		}

		var status StatusResponse
		if err := json.Unmarshal(body, &status); err != nil {
			lastErr = fmt.Errorf("failed to decode status response from %s: %w", url, err)
			continue
		}

		c.logger.Debug("status retrieved", "status", status.Status)
		return status.Status, nil
	}

	return "", fmt.Errorf("failed to get status after %d retries: %w", c.maxRetries, lastErr)
}

// ApplyFaultConfig sends fault injection configuration to the consumer.
// It calls the PUT endpoints for each non-zero fault parameter.
func (c *Client) ApplyFaultConfig(processingDelayMs int64, errorRatePercent int, commitDelayMs int64) error {
	var errs []error

	if processingDelayMs > 0 {
		body, _ := json.Marshal(map[string]int64{"delayMs": processingDelayMs})
		if err := c.doPut("/fault/processing-delay", body); err != nil {
			errs = append(errs, fmt.Errorf("processing-delay: %w", err))
		}
	}

	if errorRatePercent > 0 {
		body, _ := json.Marshal(map[string]int{"errorRatePercent": errorRatePercent})
		if err := c.doPut("/fault/error-rate", body); err != nil {
			errs = append(errs, fmt.Errorf("error-rate: %w", err))
		}
	}

	if commitDelayMs > 0 {
		body, _ := json.Marshal(map[string]int64{"delayMs": commitDelayMs})
		if err := c.doPut("/fault/commit-delay", body); err != nil {
			errs = append(errs, fmt.Errorf("commit-delay: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("fault injection errors: %v", errs)
	}
	return nil
}

// doPut sends a PUT request with a JSON body and retries on failure.
func (c *Client) doPut(path string, body []byte) error {
	url := c.baseURL + path
	var lastErr error

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			delay := c.retryBaseDelay * time.Duration(1<<uint(attempt-1))
			time.Sleep(delay)
		}

		req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("failed to create PUT request %s: %w", url, err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("PUT %s failed: %w", url, err)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			c.logger.Info("fault injection applied", "path", path)
			return nil
		}
		lastErr = fmt.Errorf("unexpected status %d from PUT %s", resp.StatusCode, url)
	}

	return fmt.Errorf("failed after %d retries: %w", c.maxRetries, lastErr)
}

// doWithRetry executes an HTTP request with retry logic and exponential backoff.
func (c *Client) doWithRetry(method, path string) error {
	url := c.baseURL + path
	var lastErr error

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			delay := c.retryBaseDelay * time.Duration(1<<uint(attempt-1))
			c.logger.Debug("retrying request", "method", method, "path", path, "attempt", attempt, "delay", delay)
			time.Sleep(delay)
		}

		req, err := http.NewRequest(method, url, nil)
		if err != nil {
			return fmt.Errorf("failed to create request %s %s: %w", method, url, err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%s %s failed: %w", method, url, err)
			c.logger.Warn("request failed", "method", method, "path", path, "attempt", attempt, "error", err)
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("unexpected status code %d from %s %s: %s", resp.StatusCode, method, url, string(body))
			c.logger.Warn("unexpected status code", "method", method, "path", path, "status_code", resp.StatusCode, "attempt", attempt)
			continue
		}

		c.logger.Info("request succeeded", "method", method, "path", path)
		return nil
	}

	return fmt.Errorf("failed after %d retries: %w", c.maxRetries, lastErr)
}
