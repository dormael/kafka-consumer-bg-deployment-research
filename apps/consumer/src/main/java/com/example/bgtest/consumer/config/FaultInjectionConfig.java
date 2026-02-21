package com.example.bgtest.consumer.config;

import lombok.Data;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.context.annotation.Configuration;

@Data
@Configuration
@ConfigurationProperties(prefix = "fault")
public class FaultInjectionConfig {

    /**
     * Additional processing delay in milliseconds per message.
     */
    private long processingDelayMs = 0;

    /**
     * Percentage of messages that will fail processing (0-100).
     */
    private int errorRatePercent = 0;

    /**
     * Additional delay in milliseconds before committing offsets.
     */
    private long commitDelayMs = 0;

    /**
     * Whether to simulate exceeding max.poll.interval.ms by blocking
     * for a long duration during message processing.
     */
    private boolean pollTimeoutExceed = false;
}
