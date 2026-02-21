package com.example.bgtest.consumer.listener;

import com.example.bgtest.consumer.model.LifecycleState;
import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.MeterRegistry;
import lombok.extern.slf4j.Slf4j;
import org.apache.kafka.clients.consumer.Consumer;
import org.apache.kafka.common.TopicPartition;
import org.springframework.kafka.listener.ConsumerAwareRebalanceListener;
import org.springframework.stereotype.Component;

import javax.annotation.PostConstruct;
import java.util.Collection;
import java.util.concurrent.atomic.AtomicReference;
import java.util.stream.Collectors;

@Slf4j
@Component
public class PauseAwareRebalanceListener implements ConsumerAwareRebalanceListener {

    private final AtomicReference<LifecycleState> lifecycleState;
    private final MeterRegistry meterRegistry;
    private Counter rebalanceCounter;

    public PauseAwareRebalanceListener(MeterRegistry meterRegistry) {
        this.meterRegistry = meterRegistry;
        this.lifecycleState = new AtomicReference<>(LifecycleState.ACTIVE);
    }

    @PostConstruct
    public void init() {
        rebalanceCounter = Counter.builder("bg_consumer_rebalance_count_total")
                .description("Total number of consumer rebalances")
                .register(meterRegistry);
    }

    public AtomicReference<LifecycleState> getLifecycleState() {
        return lifecycleState;
    }

    @Override
    public void onPartitionsRevokedAfterCommit(Consumer<?, ?> consumer, Collection<TopicPartition> partitions) {
        if (!partitions.isEmpty()) {
            String partitionStr = partitions.stream()
                    .map(tp -> String.valueOf(tp.partition()))
                    .collect(Collectors.joining(","));
            log.info("{\"level\":\"INFO\",\"message\":\"Partitions revoked\",\"partitions\":[{}],\"state\":\"{}\"}",
                    partitionStr, lifecycleState.get());
        }
    }

    @Override
    public void onPartitionsAssigned(Consumer<?, ?> consumer, Collection<TopicPartition> partitions) {
        rebalanceCounter.increment();

        LifecycleState currentState = lifecycleState.get();
        boolean repaused = false;

        String partitionStr = partitions.stream()
                .map(tp -> String.valueOf(tp.partition()))
                .collect(Collectors.joining(","));

        if (currentState == LifecycleState.PAUSED || currentState == LifecycleState.DRAINING) {
            // Rebalance 발생 시 PAUSED/DRAINING 상태면 새로 할당된 파티션을 즉시 pause
            consumer.pause(partitions);
            repaused = true;
            log.info("{\"level\":\"INFO\",\"message\":\"Partitions assigned - re-paused due to lifecycle state\",\"partitions\":[{}],\"repaused\":true,\"state\":\"{}\"}",
                    partitionStr, currentState);
        } else {
            log.info("{\"level\":\"INFO\",\"message\":\"Partitions assigned\",\"partitions\":[{}],\"repaused\":false,\"state\":\"{}\"}",
                    partitionStr, currentState);
        }
    }

    public long getRebalanceCount() {
        return (long) rebalanceCounter.count();
    }
}
