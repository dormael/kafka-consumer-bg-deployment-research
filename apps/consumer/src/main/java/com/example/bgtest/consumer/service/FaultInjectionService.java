package com.example.bgtest.consumer.service;

import com.example.bgtest.consumer.config.FaultInjectionConfig;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

import java.util.concurrent.ThreadLocalRandom;

@Slf4j
@Service
@RequiredArgsConstructor
public class FaultInjectionService {

    private final FaultInjectionConfig config;

    /**
     * Applies configured processing delay. Blocks the calling thread.
     */
    public void applyProcessingDelay() {
        long delayMs = config.getProcessingDelayMs();
        if (delayMs > 0) {
            try {
                Thread.sleep(delayMs);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                log.warn("Processing delay interrupted");
            }
        }
    }

    /**
     * Checks if the current message should fail based on the configured error rate.
     *
     * @return true if the message should fail
     */
    public boolean shouldFail() {
        int errorRate = config.getErrorRatePercent();
        if (errorRate <= 0) {
            return false;
        }
        if (errorRate >= 100) {
            return true;
        }
        return ThreadLocalRandom.current().nextInt(100) < errorRate;
    }

    /**
     * Applies configured commit delay. Blocks the calling thread.
     */
    public void applyCommitDelay() {
        long delayMs = config.getCommitDelayMs();
        if (delayMs > 0) {
            try {
                Thread.sleep(delayMs);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                log.warn("Commit delay interrupted");
            }
        }
    }

    /**
     * Simulates exceeding max.poll.interval.ms by blocking for a long time.
     * This will cause a rebalance.
     */
    public void applyPollTimeoutExceed() {
        if (config.isPollTimeoutExceed()) {
            log.warn("{\"level\":\"WARN\",\"message\":\"Simulating poll timeout exceed - blocking for 360 seconds\"}");
            try {
                // Block for 6 minutes (exceeds default max.poll.interval.ms of 300000ms = 5 minutes)
                Thread.sleep(360_000);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                log.warn("Poll timeout exceed simulation interrupted");
            }
        }
    }

    // --- Configuration mutators for runtime control ---

    public void setProcessingDelayMs(long delayMs) {
        config.setProcessingDelayMs(delayMs);
        log.info("{\"level\":\"INFO\",\"message\":\"Fault injection updated\",\"type\":\"processing-delay\",\"value\":{}}", delayMs);
    }

    public void setErrorRatePercent(int percent) {
        config.setErrorRatePercent(Math.max(0, Math.min(100, percent)));
        log.info("{\"level\":\"INFO\",\"message\":\"Fault injection updated\",\"type\":\"error-rate\",\"value\":{}}", percent);
    }

    public void setCommitDelayMs(long delayMs) {
        config.setCommitDelayMs(delayMs);
        log.info("{\"level\":\"INFO\",\"message\":\"Fault injection updated\",\"type\":\"commit-delay\",\"value\":{}}", delayMs);
    }

    public void setPollTimeoutExceed(boolean enabled) {
        config.setPollTimeoutExceed(enabled);
        log.info("{\"level\":\"INFO\",\"message\":\"Fault injection updated\",\"type\":\"poll-timeout-exceed\",\"value\":{}}", enabled);
    }

    public FaultInjectionConfig getConfig() {
        return config;
    }
}
