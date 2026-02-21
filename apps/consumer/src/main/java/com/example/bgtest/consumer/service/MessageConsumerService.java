package com.example.bgtest.consumer.service;

import com.example.bgtest.consumer.listener.PauseAwareRebalanceListener;
import com.example.bgtest.consumer.model.LifecycleState;
import com.example.bgtest.consumer.model.TestMessage;
import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.Timer;
import lombok.extern.slf4j.Slf4j;
import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.context.event.ApplicationReadyEvent;
import org.springframework.context.event.EventListener;
import org.springframework.kafka.annotation.KafkaListener;
import org.springframework.kafka.config.KafkaListenerEndpointRegistry;
import org.springframework.kafka.listener.MessageListenerContainer;
import org.springframework.kafka.support.Acknowledgment;
import org.springframework.stereotype.Service;

import javax.annotation.PostConstruct;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.atomic.AtomicReference;

@Slf4j
@Service
public class MessageConsumerService {

    private static final String LISTENER_ID = "bgTestConsumerListener";

    private final KafkaListenerEndpointRegistry registry;
    private final FaultInjectionService faultInjectionService;
    private final PauseAwareRebalanceListener rebalanceListener;
    private final MeterRegistry meterRegistry;

    // rebalanceListener와 동일한 AtomicReference를 공유하여 수동 동기화 제거
    private final AtomicReference<LifecycleState> lifecycleState;
    private final AtomicLong lastSequenceNumber = new AtomicLong(0);

    @Value("${consumer.initial-state:ACTIVE}")
    private String initialState;

    @Value("${spring.kafka.consumer.group-id:bg-test-group}")
    private String groupId;

    private Counter messagesReceivedCounter;
    private Counter processingErrorsCounter;
    private Timer commitLatencyTimer;

    public MessageConsumerService(KafkaListenerEndpointRegistry registry,
                                  FaultInjectionService faultInjectionService,
                                  PauseAwareRebalanceListener rebalanceListener,
                                  MeterRegistry meterRegistry) {
        this.registry = registry;
        this.faultInjectionService = faultInjectionService;
        this.rebalanceListener = rebalanceListener;
        this.meterRegistry = meterRegistry;
        // rebalanceListener의 AtomicReference를 공유 - 단일 소스로 상태 관리
        this.lifecycleState = rebalanceListener.getLifecycleState();
    }

    @PostConstruct
    public void init() {
        // Set initial state from environment variable
        try {
            LifecycleState initial = LifecycleState.valueOf(initialState.toUpperCase());
            lifecycleState.set(initial);
        } catch (IllegalArgumentException e) {
            log.warn("Invalid INITIAL_STATE '{}', defaulting to ACTIVE", initialState);
            lifecycleState.set(LifecycleState.ACTIVE);
        }

        // Register metrics
        messagesReceivedCounter = Counter.builder("bg_consumer_messages_received_total")
                .description("Total number of messages received")
                .tag("group_id", groupId)
                .register(meterRegistry);

        meterRegistry.gauge("bg_consumer_lifecycle_state", lifecycleState,
                ref -> ref.get().getCode());

        meterRegistry.gauge("bg_consumer_last_sequence_number", lastSequenceNumber,
                AtomicLong::get);

        processingErrorsCounter = Counter.builder("bg_consumer_processing_errors_total")
                .description("Total number of processing errors")
                .tag("group_id", groupId)
                .register(meterRegistry);

        commitLatencyTimer = Timer.builder("bg_consumer_commit_latency_ms")
                .description("Offset commit latency")
                .tag("group_id", groupId)
                .register(meterRegistry);

        log.info("{\"level\":\"INFO\",\"message\":\"Consumer initialized\",\"initialState\":\"{}\",\"groupId\":\"{}\"}",
                lifecycleState.get(), groupId);
    }

    @EventListener(ApplicationReadyEvent.class)
    public void onApplicationReady() {
        if (lifecycleState.get() == LifecycleState.PAUSED) {
            pauseContainer();
            log.info("{\"level\":\"INFO\",\"message\":\"Consumer started in PAUSED state\"}");
        }
    }

    @KafkaListener(
            id = LISTENER_ID,
            groupId = "${spring.kafka.consumer.group-id:bg-test-group}",
            topics = "bg-test-topic",
            containerFactory = "kafkaListenerContainerFactory"
    )
    public void consume(ConsumerRecord<String, TestMessage> record, Acknowledgment acknowledgment) {
        LifecycleState currentState = lifecycleState.get();

        // If paused, do not process new messages
        if (currentState == LifecycleState.PAUSED) {
            log.debug("Skipping message processing - consumer is PAUSED");
            return;
        }

        TestMessage message = record.value();
        if (message == null) {
            log.warn("{\"level\":\"WARN\",\"message\":\"Received null message\",\"partition\":{},\"offset\":{}}",
                    record.partition(), record.offset());
            acknowledgment.acknowledge();
            return;
        }

        try {
            // Apply poll timeout exceed simulation (blocks the entire poll cycle)
            faultInjectionService.applyPollTimeoutExceed();

            // Apply processing delay fault injection
            faultInjectionService.applyProcessingDelay();

            // Check if fault injection should cause a failure
            if (faultInjectionService.shouldFail()) {
                processingErrorsCounter.increment();
                log.error("{\"level\":\"ERROR\",\"message\":\"Simulated processing error\",\"seq\":{},\"partition\":{},\"offset\":{},\"groupId\":\"{}\"}",
                        message.getSequenceNumber(), record.partition(), record.offset(), groupId);
                // Still acknowledge to avoid redelivery loops in testing
                acknowledgment.acknowledge();
                return;
            }

            // Process message: update sequence tracking
            lastSequenceNumber.set(message.getSequenceNumber());
            messagesReceivedCounter.increment();

            // Apply commit delay fault injection
            Timer.Sample commitSample = Timer.start(meterRegistry);
            faultInjectionService.applyCommitDelay();
            acknowledgment.acknowledge();
            commitSample.stop(commitLatencyTimer);

            log.info("{\"level\":\"INFO\",\"message\":\"Message consumed\",\"seq\":{},\"partition\":{},\"offset\":{},\"groupId\":\"{}\",\"state\":\"{}\"}",
                    message.getSequenceNumber(), record.partition(), record.offset(), groupId, currentState);

        } catch (Exception e) {
            processingErrorsCounter.increment();
            log.error("{\"level\":\"ERROR\",\"message\":\"Message processing failed\",\"seq\":{},\"partition\":{},\"offset\":{},\"error\":\"{}\"}",
                    message.getSequenceNumber(), record.partition(), record.offset(), e.getMessage());
            // Acknowledge even on error to prevent blocking in test scenarios
            acknowledgment.acknowledge();
        }
    }

    /**
     * Pause the consumer: ACTIVE -> DRAINING -> PAUSED
     */
    public void pause() {
        LifecycleState currentState = lifecycleState.get();
        if (currentState == LifecycleState.PAUSED) {
            log.info("{\"level\":\"INFO\",\"message\":\"Consumer already PAUSED\"}");
            return;
        }
        if (currentState == LifecycleState.DRAINING) {
            log.info("{\"level\":\"INFO\",\"message\":\"Consumer already DRAINING\"}");
            return;
        }

        // Transition: ACTIVE -> DRAINING
        if (lifecycleState.compareAndSet(LifecycleState.ACTIVE, LifecycleState.DRAINING)) {
            log.info("{\"level\":\"INFO\",\"message\":\"Lifecycle state changed\",\"from\":\"ACTIVE\",\"to\":\"DRAINING\"}");

            // Pause the Kafka listener container
            pauseContainer();

            // Transition: DRAINING -> PAUSED
            lifecycleState.set(LifecycleState.PAUSED);
            log.info("{\"level\":\"INFO\",\"message\":\"Lifecycle state changed\",\"from\":\"DRAINING\",\"to\":\"PAUSED\"}");
        }
    }

    /**
     * Resume the consumer: PAUSED -> ACTIVE
     */
    public void resume() {
        LifecycleState currentState = lifecycleState.get();
        if (currentState == LifecycleState.ACTIVE) {
            log.info("{\"level\":\"INFO\",\"message\":\"Consumer already ACTIVE\"}");
            return;
        }

        // Resume the Kafka listener container
        resumeContainer();

        // Transition: PAUSED -> ACTIVE
        lifecycleState.set(LifecycleState.ACTIVE);
        log.info("{\"level\":\"INFO\",\"message\":\"Lifecycle state changed\",\"from\":\"{}\",\"to\":\"ACTIVE\"}",
                currentState);
    }

    public LifecycleState getLifecycleState() {
        return lifecycleState.get();
    }

    public long getLastSequenceNumber() {
        return lastSequenceNumber.get();
    }

    public long getTotalMessagesReceived() {
        return (long) messagesReceivedCounter.count();
    }

    private void pauseContainer() {
        MessageListenerContainer container = registry.getListenerContainer(LISTENER_ID);
        if (container != null && container.isRunning()) {
            container.pause();
            log.info("{\"level\":\"INFO\",\"message\":\"Kafka listener container paused\",\"listenerId\":\"{}\"}",
                    LISTENER_ID);
        } else {
            log.warn("{\"level\":\"WARN\",\"message\":\"Kafka listener container not found or not running\",\"listenerId\":\"{}\"}",
                    LISTENER_ID);
        }
    }

    private void resumeContainer() {
        MessageListenerContainer container = registry.getListenerContainer(LISTENER_ID);
        if (container != null) {
            if (container.isPauseRequested() || !container.isRunning()) {
                container.resume();
                log.info("{\"level\":\"INFO\",\"message\":\"Kafka listener container resumed\",\"listenerId\":\"{}\"}",
                        LISTENER_ID);
            }
        } else {
            log.warn("{\"level\":\"WARN\",\"message\":\"Kafka listener container not found\",\"listenerId\":\"{}\"}",
                    LISTENER_ID);
        }
    }
}
