# Task 07: Producer 앱 구현 (Spring Boot)

**Phase:** 2 - 애플리케이션 구현
**의존성:** task03 (Kafka Cluster)
**예상 소요:** 2시간 ~ 3시간
**튜토리얼:** `tutorial/07-producer-app.md`

---

## 목표

테스트용 Kafka Producer 애플리케이션을 Spring Boot로 구현한다. 랜덤 데이터를 생성하며, 메시지에 시퀀스 번호를 포함하여 유실/중복 검증을 지원한다.

---

## 프로젝트 구조

```
apps/producer/
├── src/main/java/com/example/bgtest/producer/
│   ├── ProducerApplication.java
│   ├── config/
│   │   └── KafkaProducerConfig.java
│   ├── service/
│   │   └── MessageProducerService.java
│   ├── controller/
│   │   └── ProducerControlController.java
│   ├── model/
│   │   └── TestMessage.java
│   └── metrics/
│       └── ProducerMetrics.java
├── src/main/resources/
│   └── application.yml
├── Dockerfile
├── pom.xml
└── k8s/
    ├── deployment.yaml
    └── service.yaml
```

---

## Subtask 07-01: 프로젝트 스캐폴딩 (pom.xml, application.yml)

**주요 의존성:**
- `spring-boot-starter-web`
- `spring-kafka`
- `spring-boot-starter-actuator`
- `micrometer-registry-prometheus`

**application.yml 주요 설정:**
```yaml
spring:
  kafka:
    bootstrap-servers: ${KAFKA_BOOTSTRAP_SERVERS:localhost:9092}
    producer:
      key-serializer: org.apache.kafka.common.serialization.StringSerializer
      value-serializer: org.springframework.kafka.support.serializer.JsonSerializer
      acks: all
      retries: 3
      properties:
        enable.idempotence: true   # 정확히 한 번 전송 보장

producer:
  topic: ${PRODUCER_TOPIC:bg-test-topic}
  rate-per-second: ${PRODUCER_RATE:100}
  message-size-bytes: ${PRODUCER_MESSAGE_SIZE:256}
  enabled: ${PRODUCER_ENABLED:true}

management:
  endpoints:
    web:
      exposure:
        include: health,info,prometheus,metrics
  metrics:
    tags:
      application: bg-test-producer
```

## Subtask 07-02: TestMessage 모델

```java
public class TestMessage {
    private long sequenceNumber;   // 유실/중복 검증용 시퀀스 번호
    private String producerId;     // Producer 인스턴스 식별자
    private long timestamp;        // 생성 시각 (epoch ms)
    private String payload;        // 랜덤 데이터 (설정된 크기만큼)
    private int partition;         // 대상 파티션 (명시적 지정 시)
}
```

## Subtask 07-03: MessageProducerService 구현

**핵심 기능:**
1. **시퀀스 번호 생성**: `AtomicLong` 기반 단조 증가 시퀀스
2. **속도 제어**: ScheduledExecutorService로 초당 N건 생성 (기본 100 TPS)
3. **메시지 크기 제어**: 지정된 바이트 크기만큼 랜덤 payload 생성
4. **파티션 제어**: 특정 파티션 지정 또는 라운드로빈
5. **로깅**: 매 메시지 전송 시 `[SEQ:{seqNum}][TOPIC:{topic}][PARTITION:{partition}]` 형식으로 로그 기록 (Validator에서 사용)

```java
@Service
public class MessageProducerService {
    private final KafkaTemplate<String, TestMessage> kafkaTemplate;
    private final AtomicLong sequenceCounter = new AtomicLong(0);
    private volatile int ratePerSecond;
    private volatile int messageSizeBytes;
    private volatile boolean enabled;

    @Scheduled(fixedRate = 10) // 10ms 간격으로 실행, rate 제어는 내부 로직
    public void produce() {
        if (!enabled) return;
        // rate 계산하여 메시지 전송
        // sequenceCounter.incrementAndGet()으로 시퀀스 번호 할당
    }
}
```

## Subtask 07-04: ProducerControlController (REST API)

**엔드포인트:**
- `GET /producer/status` — 현재 설정값 및 상태 조회
- `PUT /producer/rate` — 초당 생성률 변경
- `PUT /producer/message-size` — 메시지 크기 변경
- `POST /producer/start` — 생성 시작
- `POST /producer/stop` — 생성 중지

```java
@RestController
@RequestMapping("/producer")
public class ProducerControlController {
    @GetMapping("/status")
    public ProducerStatus getStatus() { ... }

    @PutMapping("/rate")
    public ResponseEntity<?> setRate(@RequestParam int ratePerSecond) { ... }

    @PutMapping("/message-size")
    public ResponseEntity<?> setMessageSize(@RequestParam int bytes) { ... }

    @PostMapping("/start")
    public ResponseEntity<?> start() { ... }

    @PostMapping("/stop")
    public ResponseEntity<?> stop() { ... }
}
```

## Subtask 07-05: Micrometer 지표 노출

```java
@Component
public class ProducerMetrics {
    private final Counter messagesSent;
    private final Counter sendErrors;
    private final Timer sendLatency;
    private final AtomicLong currentSequence;

    // 지표명:
    // - bg_producer_messages_sent_total: 전송된 메시지 누적 수
    // - bg_producer_send_errors_total: 전송 실패 누적 수
    // - bg_producer_send_latency_seconds: 전송 지연시간
    // - bg_producer_current_sequence: 현재 시퀀스 번호
}
```

## Subtask 07-06: Dockerfile 및 K8s 매니페스트

```dockerfile
FROM eclipse-temurin:21-jre-alpine
COPY target/*.jar app.jar
ENTRYPOINT ["java", "-jar", "app.jar"]
```

```yaml
# k8s/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: bg-test-producer
  namespace: kafka
spec:
  replicas: 1
  selector:
    matchLabels:
      app: bg-test-producer
  template:
    metadata:
      labels:
        app: bg-test-producer
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "8080"
        prometheus.io/path: "/actuator/prometheus"
    spec:
      containers:
        - name: producer
          image: bg-test-producer:latest
          ports:
            - containerPort: 8080
          env:
            - name: KAFKA_BOOTSTRAP_SERVERS
              value: "bg-test-cluster-kafka-bootstrap:9092"
            - name: PRODUCER_TOPIC
              value: "bg-test-topic"
            - name: PRODUCER_RATE
              value: "100"
```

---

## 완료 기준

- [ ] Producer 앱이 정상 빌드됨 (`mvn clean package`)
- [ ] `bg-test-topic`에 TPS 100으로 메시지 전송 가능
- [ ] 메시지에 시퀀스 번호가 포함되어 있음
- [ ] REST API로 생성률/메시지 크기 런타임 변경 가능
- [ ] Prometheus에서 `bg_producer_messages_sent_total` 지표 수집 확인
- [ ] 로그에 시퀀스 번호가 포함된 형식으로 출력됨
- [ ] Docker 이미지 빌드 및 K8s 배포 가능
