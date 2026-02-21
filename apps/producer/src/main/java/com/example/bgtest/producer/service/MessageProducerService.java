package com.example.bgtest.producer.service;

import com.example.bgtest.producer.model.TestMessage;
import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.Timer;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.kafka.core.KafkaTemplate;
import org.springframework.kafka.support.SendResult;
import org.springframework.stereotype.Service;

import javax.annotation.PostConstruct;
import javax.annotation.PreDestroy;
import java.time.Instant;
import java.util.Random;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.ScheduledFuture;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicLong;

@Slf4j
@Service
public class MessageProducerService {

    private static final String TOPIC = "bg-test-topic";

    private final KafkaTemplate<String, TestMessage> kafkaTemplate;
    private final MeterRegistry meterRegistry;
    private final Random random = new Random();

    private final AtomicLong sequenceCounter = new AtomicLong(0);
    private final AtomicBoolean running = new AtomicBoolean(false);
    private final AtomicLong currentTps = new AtomicLong();
    private final AtomicLong currentMessageSize = new AtomicLong();

    private final ScheduledExecutorService scheduler = Executors.newScheduledThreadPool(2);
    private volatile ScheduledFuture<?> producerTask;

    @Value("${producer.id:producer-0}")
    private String producerId;

    @Value("${producer.tps:100}")
    private long defaultTps;

    @Value("${producer.message-size-bytes:1024}")
    private long defaultMessageSize;

    @Value("${producer.auto-start:true}")
    private boolean autoStart;

    private Counter messagesSentCounter;
    private Timer sendLatencyTimer;

    public MessageProducerService(KafkaTemplate<String, TestMessage> kafkaTemplate,
                                  MeterRegistry meterRegistry) {
        this.kafkaTemplate = kafkaTemplate;
        this.meterRegistry = meterRegistry;
    }

    @PostConstruct
    public void init() {
        currentTps.set(defaultTps);
        currentMessageSize.set(defaultMessageSize);

        messagesSentCounter = Counter.builder("bg_producer_messages_sent_total")
                .description("Total number of messages sent")
                .tag("producer_id", producerId)
                .register(meterRegistry);

        meterRegistry.gauge("bg_producer_configured_tps",
                currentTps, AtomicLong::get);

        meterRegistry.gauge("bg_producer_last_sequence_number",
                sequenceCounter, AtomicLong::get);

        sendLatencyTimer = Timer.builder("bg_producer_send_latency_ms")
                .description("Message send latency")
                .tag("producer_id", producerId)
                .register(meterRegistry);

        log.info("Producer initialized: producerId={}, defaultTps={}, defaultMessageSize={}, autoStart={}",
                producerId, defaultTps, defaultMessageSize, autoStart);

        if (autoStart) {
            start();
        }
    }

    @PreDestroy
    public void shutdown() {
        stop();
        scheduler.shutdown();
        try {
            if (!scheduler.awaitTermination(5, TimeUnit.SECONDS)) {
                scheduler.shutdownNow();
            }
        } catch (InterruptedException e) {
            scheduler.shutdownNow();
            Thread.currentThread().interrupt();
        }
    }

    public void start() {
        if (running.compareAndSet(false, true)) {
            scheduleProduction();
            log.info("Producer started: tps={}, messageSize={}", currentTps.get(), currentMessageSize.get());
        }
    }

    public void stop() {
        if (running.compareAndSet(true, false)) {
            if (producerTask != null) {
                producerTask.cancel(false);
                producerTask = null;
            }
            log.info("Producer stopped: lastSequence={}", sequenceCounter.get());
        }
    }

    public void updateConfig(Long tps, Long messageSizeBytes) {
        boolean needsReschedule = false;
        if (tps != null && tps > 0) {
            currentTps.set(tps);
            needsReschedule = true;
        }
        if (messageSizeBytes != null && messageSizeBytes > 0) {
            currentMessageSize.set(messageSizeBytes);
        }
        if (needsReschedule && running.get()) {
            if (producerTask != null) {
                producerTask.cancel(false);
            }
            scheduleProduction();
        }
        log.info("Producer config updated: tps={}, messageSize={}", currentTps.get(), currentMessageSize.get());
    }

    private void scheduleProduction() {
        long intervalMicros = 1_000_000L / currentTps.get();
        producerTask = scheduler.scheduleAtFixedRate(
                this::sendMessage,
                0,
                intervalMicros,
                TimeUnit.MICROSECONDS
        );
    }

    private void sendMessage() {
        if (!running.get()) {
            return;
        }

        long seq = sequenceCounter.incrementAndGet();

        byte[] payload = new byte[(int) currentMessageSize.get()];
        random.nextBytes(payload);

        TestMessage message = TestMessage.builder()
                .sequenceNumber(seq)
                .producerId(producerId)
                .timestamp(Instant.now())
                .partition(-1) // will be set after send
                .payload(payload)
                .build();

        String key = String.valueOf(seq);

        Timer.Sample sample = Timer.start(meterRegistry);

        kafkaTemplate.send(TOPIC, key, message).completable().whenComplete((result, ex) -> {
            sample.stop(sendLatencyTimer);
            if (ex != null) {
                log.error("{\"level\":\"ERROR\",\"message\":\"Message send failed\",\"seq\":{},\"error\":\"{}\"}",
                        seq, ex.getMessage());
            } else {
                messagesSentCounter.increment();

                int partition = result.getRecordMetadata().partition();
                long offset = result.getRecordMetadata().offset();

                log.info("{\"level\":\"INFO\",\"message\":\"Message sent\",\"seq\":{},\"partition\":{},\"offset\":{}}",
                        seq, partition, offset);
            }
        });
    }

    public boolean isRunning() {
        return running.get();
    }

    public long getSequenceCounter() {
        return sequenceCounter.get();
    }

    public long getCurrentTps() {
        return currentTps.get();
    }

    public long getCurrentMessageSize() {
        return currentMessageSize.get();
    }

    public String getProducerId() {
        return producerId;
    }
}
