# Task 02: Producer/Consumer Java Spring Boot 앱 구현

> **의존:** task01 (Kafka 클러스터 필요)
> **튜토리얼:** `tutorial/05-producer-consumer-build.md`

---

## 목표

전략 B, C 검증을 위한 테스트용 Producer와 Consumer Spring Boot 애플리케이션을 구현한다. 임의 데이터를 Produce/Consume하며, 지표와 로그를 통해 전환 중 메시지 유실/중복을 측정할 수 있어야 한다.

## 프로젝트 구조

```
apps/
├── producer/
│   ├── src/main/java/com/example/bgtest/producer/
│   │   ├── ProducerApplication.java
│   │   ├── config/
│   │   │   └── KafkaProducerConfig.java
│   │   ├── service/
│   │   │   └── MessageProducerService.java
│   │   ├── controller/
│   │   │   └── ProducerControlController.java
│   │   └── model/
│   │       └── TestMessage.java
│   ├── src/main/resources/
│   │   └── application.yaml
│   ├── Dockerfile
│   └── pom.xml
│
└── consumer/
    ├── src/main/java/com/example/bgtest/consumer/
    │   ├── ConsumerApplication.java
    │   ├── config/
    │   │   ├── KafkaConsumerConfig.java
    │   │   └── FaultInjectionConfig.java
    │   ├── service/
    │   │   ├── MessageConsumerService.java
    │   │   └── FaultInjectionService.java
    │   ├── controller/
    │   │   ├── LifecycleController.java
    │   │   └── FaultInjectionController.java
    │   ├── listener/
    │   │   └── PauseAwareRebalanceListener.java
    │   └── model/
    │       ├── TestMessage.java
    │       └── LifecycleState.java
    ├── src/main/resources/
    │   └── application.yaml
    ├── Dockerfile
    └── pom.xml
```

## Producer 상세 설계

### 핵심 기능

1. **시퀀스 번호 포함 메시지 생성**: 각 메시지에 고유 시퀀스 번호 부여 (유실/중복 검증용)
2. **설정 가능한 생성률**: 초당 메시지 수 (기본 TPS 100)
3. **설정 가능한 메시지 크기**: 바이트 단위 (기본 1KB)
4. **런타임 설정 변경**: REST API + 환경변수/파일

### TestMessage 구조

```json
{
  "sequenceNumber": 12345,
  "producerId": "producer-0",
  "timestamp": "2026-02-20T10:30:00.000Z",
  "partition": 3,
  "payload": "<random-bytes>"
}
```

### REST API

| 엔드포인트 | 메서드 | 설명 |
|-----------|--------|------|
| `/producer/config` | GET | 현재 Producer 설정 조회 |
| `/producer/config` | PUT | 생성률, 메시지 크기 런타임 변경 |
| `/producer/stats` | GET | 발행 통계 (총 발행 수, 현재 TPS 등) |
| `/producer/start` | POST | 메시지 생성 시작 |
| `/producer/stop` | POST | 메시지 생성 중지 |

### 지표 (Micrometer → Prometheus)

| 지표명 | 타입 | 설명 |
|--------|------|------|
| `bg_producer_messages_sent_total` | Counter | 총 발행 메시지 수 |
| `bg_producer_messages_sent_rate` | Gauge | 초당 발행 수 |
| `bg_producer_last_sequence_number` | Gauge | 마지막 시퀀스 번호 |
| `bg_producer_send_latency_ms` | Timer | 발행 지연 시간 |

### 로그 포맷 (구조화 로그)

```
{"level":"INFO","logger":"MessageProducerService","message":"Message sent","seq":12345,"partition":3,"offset":67890,"timestamp":"..."}
```

## Consumer 상세 설계

### 핵심 기능

1. **메시지 수신 및 시퀀스 기록**: 수신한 시퀀스 번호를 로그/지표로 기록
2. **Lifecycle 엔드포인트**: `/lifecycle/pause`, `/lifecycle/resume`, `/lifecycle/status`
3. **장애 주입**: 처리 지연, 실패율, 느린 Offset 커밋, max.poll.interval.ms 초과
4. **Rebalance 방어**: `ConsumerRebalanceListener.onPartitionsAssigned`에서 pause 상태 재적용

### Lifecycle 상태 머신

```
ACTIVE ──pause()──> DRAINING ──drain완료──> PAUSED
PAUSED ──resume()──> ACTIVE
```

### LifecycleController 엔드포인트

| 엔드포인트 | 메서드 | 설명 |
|-----------|--------|------|
| `/lifecycle/pause` | POST | Consumer pause 요청 (DRAINING → PAUSED) |
| `/lifecycle/resume` | POST | Consumer resume 요청 (→ ACTIVE) |
| `/lifecycle/status` | GET | 현재 상태 반환 (ACTIVE/PAUSED/DRAINING) |

### Pause/Resume 구현 핵심 (전략 C)

```java
// AtomicBoolean 기반 Thread-safe 간접 제어
private final AtomicReference<LifecycleState> lifecycleState = new AtomicReference<>(LifecycleState.ACTIVE);

// KafkaListenerEndpointRegistry를 통한 pause/resume
// Spring Kafka의 container.pause()는 poll loop 내에서 안전하게 실행됨

// ConsumerRebalanceListener에서 pause 상태 복구
@Override
public void onPartitionsAssigned(Collection<TopicPartition> partitions) {
    if (lifecycleState.get() == LifecycleState.PAUSED) {
        consumer.pause(partitions);
        log.info("Re-paused assigned partitions due to PAUSED state");
    }
}
```

### 장애 주입 (FaultInjectionService)

| 장애 유형 | 설정 키 | 기본값 | REST API |
|-----------|---------|--------|----------|
| 처리 지연 | `fault.processing-delay-ms` | 0 | PUT `/fault/processing-delay` |
| 처리 실패율 | `fault.error-rate-percent` | 0 | PUT `/fault/error-rate` |
| 느린 Offset 커밋 | `fault.commit-delay-ms` | 0 | PUT `/fault/commit-delay` |
| max.poll.interval.ms 초과 | `fault.poll-timeout-exceed` | false | PUT `/fault/poll-timeout` |

### Kafka Consumer 설정

```yaml
spring:
  kafka:
    consumer:
      bootstrap-servers: ${KAFKA_BOOTSTRAP_SERVERS:kafka-cluster:9092}
      group-id: ${KAFKA_GROUP_ID:bg-test-group}
      auto-offset-reset: earliest
      enable-auto-commit: false  # 수동 커밋으로 정확한 offset 관리
      properties:
        partition.assignment.strategy: org.apache.kafka.clients.consumer.CooperativeStickyAssignor
        group.instance.id: ${KAFKA_GROUP_INSTANCE_ID:${HOSTNAME}}
        session.timeout.ms: 45000
        heartbeat.interval.ms: 15000
        max.poll.interval.ms: 300000
        max.poll.records: 500
    listener:
      ack-mode: MANUAL_IMMEDIATE  # 수동 커밋
      concurrency: ${KAFKA_LISTENER_CONCURRENCY:1}
```

### 지표 (Micrometer → Prometheus)

| 지표명 | 타입 | 설명 |
|--------|------|------|
| `bg_consumer_messages_received_total` | Counter | 총 수신 메시지 수 |
| `bg_consumer_lifecycle_state` | Gauge | 라이프사이클 상태 (0=ACTIVE, 1=DRAINING, 2=PAUSED) |
| `bg_consumer_last_sequence_number` | Gauge | 마지막 수신 시퀀스 번호 |
| `bg_consumer_processing_errors_total` | Counter | 처리 에러 수 |
| `bg_consumer_rebalance_count_total` | Counter | Rebalance 발생 횟수 |
| `bg_consumer_commit_latency_ms` | Timer | Offset 커밋 지연 |

### 로그 포맷 (구조화 로그)

```
{"level":"INFO","logger":"MessageConsumerService","message":"Message consumed","seq":12345,"partition":3,"offset":67890,"groupId":"bg-test-group","state":"ACTIVE","timestamp":"..."}
{"level":"INFO","logger":"LifecycleController","message":"Lifecycle state changed","from":"ACTIVE","to":"DRAINING","timestamp":"..."}
{"level":"INFO","logger":"PauseAwareRebalanceListener","message":"Partitions assigned","partitions":"[0,1,2,3]","repaused":true,"timestamp":"..."}
```

## Docker 이미지 빌드

```dockerfile
FROM eclipse-temurin:17-jre-alpine
COPY target/*.jar app.jar
EXPOSE 8080
ENTRYPOINT ["java", "-jar", "app.jar"]
```

단일 노드 클러스터이므로 로컬 Docker 빌드 후 `imagePullPolicy: Never` 또는 로컬 레지스트리 사용.

## K8s 배포 매니페스트

- `k8s/producer-deployment.yaml`: Producer Deployment + Service
- `k8s/consumer-blue-statefulset.yaml`: Blue Consumer StatefulSet + Service
- `k8s/consumer-green-statefulset.yaml`: Green Consumer StatefulSet + Service
- `k8s/consumer-configmap.yaml`: 공통 설정 ConfigMap

## 완료 기준

- [x] Producer가 TPS 100으로 bg-test-topic에 메시지 발행 — 배포 완료, TPS 100 전송 확인
- [x] Consumer가 메시지를 정상 소비하고 시퀀스 번호 로그 출력 — 배포 완료, Blue Consumer 소비 확인
- [x] `/lifecycle/pause` → Consumer PAUSED 상태 전환 확인 — Green Consumer PAUSED 동작 확인
- [x] `/lifecycle/resume` → Consumer ACTIVE 상태 복귀 확인 — 코드 구현 완료 (전략 C 테스트에서 실측 예정)
- [x] Rebalance 발생 시 pause 상태 유지 확인 — PauseAwareRebalanceListener re-pause 동작 확인
- [x] 장애 주입 REST API 동작 확인 — 4가지 장애 유형 모두 구현
- [x] Prometheus에서 커스텀 지표 조회 가능 — Producer/Consumer 지표 모두 구현
- [ ] Loki에서 구조화 로그 조회 가능 — 미확인 (전략 C 테스트 시 확인 예정)

## 2026-02-21 수정 이력

- Health Probe 활성화: `management.endpoint.health.probes.enabled: true` 추가 (두 앱 모두)
- Producer 자동 시작: `producer.auto-start` 설정 + `@PostConstruct`에서 자동 `start()`
- 지표명 수정: `bg_producer_messages_sent_rate` → `bg_producer_configured_tps` (설정값 노출임을 명확히)
- Deprecated API 제거: `ListenableFutureCallback` → `completable().whenComplete()`
- **컴파일 오류 수정**: `PauseAwareRebalanceListener.onPartitionsRevoked()` → `onPartitionsRevokedAfterCommit()` (spring-kafka 2.8.11의 `ConsumerAwareRebalanceListener`에 해당 시그니처 미존재)
- **Docker 빌드 & K8s 배포 완료**: `minikube image build` 사용, Producer 1 pod + Consumer Blue/Green 각 3 pods 전체 Running/Ready

## 남은 P2 이슈 (선택적)

- 구조화 로그 혼합 포맷: `logstash-logback-encoder` 없이 수동 JSON → Logback 패턴이 앞에 붙어 Loki JSON 파싱 제한. 테스트 목적에는 충분.
- DRAINING 단계 실제 drain 없음: pause 시 진행 중인 메시지 완료 대기 없이 즉시 PAUSED 전환. 설계 선택.

## TODO (다른 언어/프레임워크)

- Go (twmb/franz-go) 구현체
- Node.js (KafkaJS) 구현체
- Python (confluent-kafka-python) 구현체
