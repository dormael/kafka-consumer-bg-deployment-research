# Task 08: Consumer 앱 구현 (Spring Boot, Lifecycle 엔드포인트 포함)

**Phase:** 2 - 애플리케이션 구현
**의존성:** task03 (Kafka Cluster), task07 (Producer와 메시지 포맷 공유)
**예상 소요:** 4시간 ~ 6시간 (가장 복잡한 구현)
**튜토리얼:** `tutorial/08-consumer-app.md`

---

## 목표

테스트용 Kafka Consumer 애플리케이션을 Spring Boot로 구현한다. 전략 C의 핵심인 `/lifecycle` 엔드포인트, AtomicBoolean 기반 pause/resume, ConsumerRebalanceListener를 포함한다. 또한 장애 주입(chaos injection) 기능을 포함하여 다양한 시나리오를 테스트할 수 있도록 한다.

---

## 프로젝트 구조

```
apps/consumer/
├── src/main/java/com/example/bgtest/consumer/
│   ├── ConsumerApplication.java
│   ├── config/
│   │   ├── KafkaConsumerConfig.java
│   │   └── ConsumerProperties.java
│   ├── listener/
│   │   ├── MessageConsumerListener.java
│   │   └── BgRebalanceListener.java
│   ├── lifecycle/
│   │   ├── LifecycleState.java           # ACTIVE / PAUSED / DRAINING enum
│   │   ├── LifecycleManager.java          # AtomicReference<LifecycleState> 관리
│   │   └── LifecycleController.java       # REST 엔드포인트
│   ├── chaos/
│   │   ├── ChaosConfig.java               # 장애 주입 설정
│   │   ├── ChaosController.java           # 장애 주입 REST API
│   │   └── ChaosInterceptor.java          # 메시지 처리 전 장애 주입
│   ├── model/
│   │   └── TestMessage.java               # Producer와 동일 모델
│   └── metrics/
│       └── ConsumerMetrics.java
├── src/main/resources/
│   └── application.yml
├── Dockerfile
├── pom.xml
└── k8s/
    ├── statefulset-blue.yaml
    ├── statefulset-green.yaml
    ├── service-blue.yaml
    ├── service-green.yaml
    └── configmap-active-version.yaml
```

---

## Subtask 08-01: Lifecycle 상태 관리 (가장 중요)

### LifecycleState enum

```java
public enum LifecycleState {
    ACTIVE,    // 정상 소비 중
    PAUSED,    // pause 완료 (소비 중단)
    DRAINING   // pause 요청 → 현재 배치 처리 완료 대기 중
}
```

### LifecycleManager

```java
@Component
public class LifecycleManager {
    private final AtomicReference<LifecycleState> state;
    private final AtomicBoolean pauseRequested = new AtomicBoolean(false);
    private final AtomicBoolean resumeRequested = new AtomicBoolean(false);
    private final KafkaListenerEndpointRegistry registry;
    private final CountDownLatch drainLatch = new CountDownLatch(1);

    /**
     * 외부(HTTP)에서 호출. Thread-safe하게 플래그만 설정.
     * 실제 consumer.pause()는 poll loop 내에서 실행됨.
     */
    public void requestPause() {
        state.set(LifecycleState.DRAINING);
        pauseRequested.set(true);
        // drain 완료 대기 (현재 배치 처리 완료)
        drainLatch.await(drainTimeoutSeconds, TimeUnit.SECONDS);
        // offset commit
        // consumer.pause() 실행 → 상태 PAUSED로 전환
    }

    public void requestResume() {
        resumeRequested.set(true);
    }

    /**
     * Poll loop 또는 KafkaListener 내에서 호출.
     * pauseRequested 플래그를 확인하여 실제 pause/resume 수행.
     */
    public void checkAndApplyStateChange(Consumer<?, ?> consumer) {
        if (pauseRequested.compareAndSet(true, false)) {
            consumer.commitSync();
            consumer.pause(consumer.assignment());
            state.set(LifecycleState.PAUSED);
        }
        if (resumeRequested.compareAndSet(true, false)) {
            consumer.resume(consumer.assignment());
            state.set(LifecycleState.ACTIVE);
        }
    }
}
```

### LifecycleController

```java
@RestController
@RequestMapping("/lifecycle")
public class LifecycleController {
    private final LifecycleManager lifecycleManager;

    @PostMapping("/pause")
    public ResponseEntity<Map<String, String>> pause() {
        lifecycleManager.requestPause();
        return ResponseEntity.ok(Map.of("status", "PAUSED"));
    }

    @PostMapping("/resume")
    public ResponseEntity<Map<String, String>> resume() {
        lifecycleManager.requestResume();
        return ResponseEntity.ok(Map.of("status", "ACTIVE"));
    }

    @GetMapping("/status")
    public ResponseEntity<Map<String, Object>> status() {
        return ResponseEntity.ok(Map.of(
            "state", lifecycleManager.getState().name(),
            "assignedPartitions", lifecycleManager.getAssignedPartitions(),
            "pausedPartitions", lifecycleManager.getPausedPartitions()
        ));
    }
}
```

## Subtask 08-02: ConsumerRebalanceListener (Rebalance 시 Pause 유지)

```java
@Component
public class BgRebalanceListener implements ConsumerRebalanceListener {
    private final LifecycleManager lifecycleManager;
    private final MeterRegistry meterRegistry;
    private final Counter rebalanceCounter;

    @Override
    public void onPartitionsAssigned(Collection<TopicPartition> partitions) {
        log.info("Partitions assigned: {}", partitions);
        rebalanceCounter.increment();

        // 핵심: Rebalance 후에도 pause 상태 유지
        if (lifecycleManager.getState() == LifecycleState.PAUSED) {
            // consumer.pause()는 poll loop에서 실행되어야 하므로 플래그 설정
            lifecycleManager.reapplyPause(partitions);
            log.warn("Re-paused assigned partitions due to PAUSED lifecycle state: {}", partitions);
        }
    }

    @Override
    public void onPartitionsRevoked(Collection<TopicPartition> partitions) {
        log.info("Partitions revoked: {}", partitions);
        // revoke 전 offset 확정
        // consumer.commitSync();  (Spring Kafka가 자동 처리)
    }
}
```

## Subtask 08-03: MessageConsumerListener (메시지 처리)

```java
@Component
public class MessageConsumerListener {
    private final LifecycleManager lifecycleManager;
    private final ChaosInterceptor chaosInterceptor;
    private final ConsumerMetrics metrics;

    @KafkaListener(
        topics = "${consumer.topic:bg-test-topic}",
        groupId = "${consumer.group-id:bg-test-consumer-group}",
        containerFactory = "kafkaListenerContainerFactory"
    )
    public void consume(ConsumerRecord<String, TestMessage> record,
                        Acknowledgment ack,
                        Consumer<?, ?> consumer) {
        // 1. Lifecycle 상태 체크 및 적용
        lifecycleManager.checkAndApplyStateChange(consumer);

        // 2. Chaos injection 적용
        chaosInterceptor.maybeInjectDelay();        // 처리 지연
        chaosInterceptor.maybeInjectError();        // 처리 실패
        chaosInterceptor.maybeInjectSlowCommit();   // 느린 커밋

        // 3. 메시지 처리 (실제 Sink는 스킵, 시퀀스 번호만 기록)
        long seq = record.value().getSequenceNumber();
        log.info("[CONSUMED][SEQ:{}][TOPIC:{}][PARTITION:{}][OFFSET:{}]",
                 seq, record.topic(), record.partition(), record.offset());

        metrics.recordConsumed(record);

        // 4. Offset commit
        ack.acknowledge();
    }
}
```

## Subtask 08-04: 장애 주입 (Chaos Injection)

### ChaosConfig

```java
@Component
@ConfigurationProperties(prefix = "chaos")
public class ChaosConfig {
    private int processingDelayMs = 0;        // 처리 지연 (ms)
    private double errorRate = 0.0;           // 처리 실패율 (0.0 ~ 1.0)
    private int slowCommitDelayMs = 0;        // 느린 커밋 지연 (ms)
    private int maxPollIntervalExceedMs = 0;  // max.poll.interval.ms 초과 시뮬레이션
    // getter/setter
}
```

### ChaosController (REST API)

```java
@RestController
@RequestMapping("/chaos")
public class ChaosController {
    @PutMapping("/processing-delay")
    public ResponseEntity<?> setProcessingDelay(@RequestParam int delayMs) { ... }

    @PutMapping("/error-rate")
    public ResponseEntity<?> setErrorRate(@RequestParam double rate) { ... }

    @PutMapping("/slow-commit")
    public ResponseEntity<?> setSlowCommitDelay(@RequestParam int delayMs) { ... }

    @PutMapping("/max-poll-exceed")
    public ResponseEntity<?> setMaxPollExceed(@RequestParam int delayMs) { ... }

    @GetMapping("/status")
    public ResponseEntity<ChaosConfig> getStatus() { ... }

    @PostMapping("/reset")
    public ResponseEntity<?> resetAll() { ... }
}
```

### ChaosInterceptor

```java
@Component
public class ChaosInterceptor {
    private final ChaosConfig config;
    private final Random random = new Random();

    public void maybeInjectDelay() {
        int delay = config.getProcessingDelayMs();
        if (delay > 0) {
            Thread.sleep(delay);
        }
    }

    public void maybeInjectError() {
        if (random.nextDouble() < config.getErrorRate()) {
            throw new RuntimeException("Chaos: simulated processing error");
        }
    }

    public void maybeInjectSlowCommit() {
        int delay = config.getSlowCommitDelayMs();
        if (delay > 0) {
            Thread.sleep(delay);  // commitSync 전에 호출
        }
    }
}
```

## Subtask 08-05: Kafka Consumer 설정 (application.yml)

```yaml
spring:
  kafka:
    bootstrap-servers: ${KAFKA_BOOTSTRAP_SERVERS:localhost:9092}
    consumer:
      group-id: ${CONSUMER_GROUP_ID:bg-test-consumer-group}
      auto-offset-reset: earliest
      enable-auto-commit: false   # 수동 커밋 (정확한 offset 관리)
      key-deserializer: org.apache.kafka.common.serialization.StringDeserializer
      value-deserializer: org.springframework.kafka.support.serializer.JsonDeserializer
      properties:
        spring.json.trusted.packages: "*"
        partition.assignment.strategy: org.apache.kafka.clients.consumer.CooperativeStickyAssignor
        group.instance.id: ${KAFKA_GROUP_INSTANCE_ID:}  # Static Membership (Pod명)
        session.timeout.ms: ${KAFKA_SESSION_TIMEOUT:45000}
        heartbeat.interval.ms: 15000
        max.poll.interval.ms: ${KAFKA_MAX_POLL_INTERVAL:300000}
        max.poll.records: 500

consumer:
  topic: ${CONSUMER_TOPIC:bg-test-topic}
  group-id: ${CONSUMER_GROUP_ID:bg-test-consumer-group}
  initial-state: ${CONSUMER_INITIAL_STATE:ACTIVE}  # ACTIVE 또는 PAUSED
  drain-timeout-seconds: 10

chaos:
  processing-delay-ms: ${CHAOS_PROCESSING_DELAY:0}
  error-rate: ${CHAOS_ERROR_RATE:0.0}
  slow-commit-delay-ms: ${CHAOS_SLOW_COMMIT_DELAY:0}

management:
  endpoints:
    web:
      exposure:
        include: health,info,prometheus,metrics
  metrics:
    tags:
      application: bg-test-consumer
      color: ${CONSUMER_COLOR:blue}
```

## Subtask 08-06: Micrometer 지표

```java
@Component
public class ConsumerMetrics {
    // 지표명:
    // - bg_consumer_messages_consumed_total{color,partition}: 소비 메시지 누적 수
    // - bg_consumer_errors_total{color}: 처리 에러 누적 수
    // - bg_consumer_processing_latency_seconds{color}: 메시지 처리 지연시간
    // - bg_consumer_lifecycle_state{color}: 라이프사이클 상태 (Gauge: 0=ACTIVE, 1=PAUSED, 2=DRAINING)
    // - bg_consumer_rebalance_count_total{color}: Rebalance 발생 횟수
    // - bg_consumer_commit_latency_seconds{color}: Offset commitSync 지연시간
    // - bg_consumer_last_consumed_sequence{color,partition}: 마지막 소비 시퀀스 번호
}
```

## Subtask 08-07: K8s StatefulSet (Blue/Green)

전략 C에서 Static Membership(`group.instance.id`)을 사용하므로 StatefulSet으로 배포한다.

```yaml
# statefulset-blue.yaml (핵심 부분)
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: consumer-blue
  namespace: kafka
spec:
  replicas: 4
  serviceName: consumer-blue
  selector:
    matchLabels:
      app: bg-test-consumer
      color: blue
  template:
    spec:
      containers:
        - name: consumer
          image: bg-test-consumer:latest
          env:
            - name: KAFKA_BOOTSTRAP_SERVERS
              value: "bg-test-cluster-kafka-bootstrap:9092"
            - name: CONSUMER_GROUP_ID
              value: "bg-test-consumer-group"
            - name: KAFKA_GROUP_INSTANCE_ID
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
            - name: CONSUMER_COLOR
              value: "blue"
            - name: CONSUMER_INITIAL_STATE
              value: "ACTIVE"
        # Switch Sidecar (task09에서 구현)
        - name: switch-sidecar
          image: bg-switch-sidecar:latest
          env:
            - name: MY_COLOR
              value: "blue"
            # ...
```

Green StatefulSet은 `CONSUMER_INITIAL_STATE: PAUSED`, `color: green`으로 설정.

## Subtask 08-08: 전략 B용 설정 (별도 Consumer Group)

전략 B에서는 Blue와 Green이 별도 Consumer Group을 사용한다:
- Blue: `group-id: bg-test-consumer-blue`
- Green: `group-id: bg-test-consumer-green`

이를 환경변수로 전환 가능하도록 설계:
```yaml
- name: CONSUMER_GROUP_ID
  value: "bg-test-consumer-blue"  # 전략 B
  # value: "bg-test-consumer-group"  # 전략 C (공유)
```

---

## 완료 기준

- [ ] Consumer 앱이 정상 빌드됨
- [ ] `/lifecycle/pause` → DRAINING → PAUSED 전환 동작 확인
- [ ] `/lifecycle/resume` → ACTIVE 전환 동작 확인
- [ ] `/lifecycle/status`에서 상태, 할당 파티션, pause된 파티션 정보 반환
- [ ] Rebalance 발생 후 pause 상태가 유지됨 (BgRebalanceListener 동작)
- [ ] 장애 주입 REST API로 처리 지연/에러율/느린 커밋 설정 가능
- [ ] 로그에 `[CONSUMED][SEQ:...][TOPIC:...][PARTITION:...][OFFSET:...]` 형식 출력
- [ ] Prometheus에서 `bg_consumer_*` 지표 수집 확인
- [ ] Static Membership(`group.instance.id = Pod명`) 동작 확인
- [ ] 수동 Offset 커밋(`enable-auto-commit: false`) 동작 확인
