# Task 01: 앱 수정 및 인프라 준비 (Foundation)

> **의존:** 없음 (Phase 1 인프라 위에 구축)
> **차단:** Task 02, 03, 04 (모든 테스트 Task)

---

## 목표

Phase 1에서 StatefulSet 기반으로 구현된 Consumer/Producer 앱과 K8s 매니페스트를 Argo Rollouts 기반으로 전환한다. 3가지 접근법에서 공통으로 필요한 변경 사항을 이 Task에서 처리한다.

---

## 1. Consumer 앱 수정

### 1.1 Static Membership 제거 + KIP-848 활성화

**파일:** `apps/consumer/src/main/resources/application.yaml`

```yaml
# Phase 1 (제거)
spring.kafka.consumer.properties:
  group.instance.id: ${HOSTNAME}
  partition.assignment.strategy: org.apache.kafka.clients.consumer.CooperativeStickyAssignor
  session.timeout.ms: 45000
  heartbeat.interval.ms: 3000

# Phase 2 (변경 — KIP-848 활성화)
spring.kafka.consumer.properties:
  group.protocol: consumer   # KIP-848 활성화 (서버 사이드 할당)
  # group.instance.id — 제거 (Deployment Pod 이름 랜덤)
  # partition.assignment.strategy — 제거 (서버 사이드 group.consumer.assignors)
  # session.timeout.ms — 제거 (서버 사이드 group.consumer.session.timeout.ms)
  # heartbeat.interval.ms — 제거 (서버 사이드 group.consumer.heartbeat.interval.ms)
```

> KIP-848에서는 할당 로직이 Client → Server(Group Coordinator)로 이동.
> `partition.assignment.strategy` 등 클라이언트 사이드 설정이 서버 사이드로 대체됨.

### 1.1b Spring Boot 2.7 → 3.4 마이그레이션

**영향 파일:** Consumer/Producer 앱 전체

| 항목 | 변경 |
|------|------|
| Java 버전 | 8/11 → **17+** |
| Jakarta EE | `javax.servlet.*` → `jakarta.servlet.*` |
| 의존성 | `spring-boot-starter-parent` 3.4.x |
| kafka-clients | BOM 기본 3.8.x → **4.1.x override** |
| Micrometer | 1.9.x → 1.13+ |

```xml
<!-- pom.xml 핵심 변경 -->
<parent>
    <groupId>org.springframework.boot</groupId>
    <artifactId>spring-boot-starter-parent</artifactId>
    <version>3.4.5</version>
</parent>
<properties>
    <java.version>17</java.version>
    <kafka.version>4.1.1</kafka.version>
</properties>
```

### 1.2 STOPPED 상태 추가

**파일:** `apps/consumer/src/main/java/.../service/MessageConsumerService.java`

Consumer의 Lifecycle 상태에 STOPPED를 추가한다:

| 상태 | 코드 | 그룹 멤버십 | poll() | 설명 |
|------|------|------------|--------|------|
| STOPPED | 3 | 미가입 | 중단 | Container 정지 상태, 기본 시작 상태 |
| ACTIVE | 0 | 가입 | 활성 | 메시지 소비 중 |
| PAUSED | 1 | 가입 | 빈 결과 | 그룹 유지, 소비 중단 |
| DRAINING | 2 | 가입 | 활성 | 종료 전 처리 완료 중 |

**핵심 변경 (decisions.md D3):**
- 기본 시작 상태: PAUSED → **STOPPED**
- STOPPED 상태에서는 `KafkaListenerEndpointRegistry.getListenerContainer(id).stop()` 호출
- Consumer가 그룹에 가입하지 않으므로 리밸런싱 미발생

### 1.3 Lifecycle API 확장

**파일:** `apps/consumer/src/main/java/.../controller/LifecycleController.java`

| Endpoint | Method | 용도 | Phase 1 | Phase 2 |
|----------|--------|------|---------|---------|
| `/lifecycle/pause` | POST | 소비 일시 정지 | 있음 | 유지 |
| `/lifecycle/resume` | POST | 소비 재개 | 있음 | 유지 |
| `/lifecycle/status` | GET | 현재 상태 | 있음 | 유지 (STOPPED=3 추가) |
| `/lifecycle/start` | POST | Container 시작 (STOPPED → ACTIVE) | **없음** | **신규** |
| `/lifecycle/stop` | POST | Container 정지 (→ STOPPED) | **없음** | **신규** |

`/lifecycle/start` 구현:
```java
@PostMapping("/lifecycle/start")
public ResponseEntity<String> start() {
    // 1. KafkaListenerEndpointRegistry.getListenerContainer(listenerId).start()
    // 2. 즉시 resume (ACTIVE 상태 전환)
    // 3. 상태: STOPPED → ACTIVE
}
```

`/lifecycle/stop` 구현:
```java
@PostMapping("/lifecycle/stop")
public ResponseEntity<String> stop() {
    // 1. KafkaListenerEndpointRegistry.getListenerContainer(listenerId).stop()
    // 2. Consumer가 그룹에서 탈퇴 (LeaveGroup 전송)
    // 3. 상태: * → STOPPED
}
```

### 1.4 PauseAwareRebalanceListener 유지 + KIP-848 호환 확인

기존 리밸런스 리스너는 수정 없이 유지한다. KIP-848에서도 `ConsumerRebalanceListener` 인터페이스는 동일하게 지원된다.

**KIP-848에서의 동작 차이:**
- Classic Protocol: `onPartitionsRevoked()` → 전체 Consumer 일시 정지
- KIP-848: `onPartitionsRevoked()` → **해당 파티션을 가진 Consumer만** 호출, 나머지는 계속 소비
- pause 상태 재적용 로직은 두 프로토콜 모두에서 유효

**비교 테스트 시 주의:** Classic Protocol(`group.protocol=classic`)로 전환 시 CooperativeStickyAssignor 설정이 필요:
```yaml
# Classic Protocol 비교 테스트 시
spring.kafka.consumer.properties:
  group.protocol: classic
  partition.assignment.strategy: org.apache.kafka.clients.consumer.CooperativeStickyAssignor
```

---

## 2. Argo Rollouts 매니페스트 설계

### 2.1 단일 그룹용 Rollout (A-1, B-1, C-1)

하나의 Rollout 리소스로 Blue-Green 전환을 관리한다.

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata:
  name: consumer
  namespace: bg-test
spec:
  replicas: 3
  revisionHistoryLimit: 2
  selector:
    matchLabels:
      app: consumer
  strategy:
    blueGreen:
      activeService: consumer-active-svc
      previewService: consumer-preview-svc
      autoPromotionEnabled: false
      prePromotionAnalysis:
        templates:
        - templateName: consumer-switch-analysis
        args:
        - name: preview-service
          value: consumer-preview-svc
        - name: active-service
          value: consumer-active-svc
      postPromotionAnalysis:
        templates:
        - templateName: consumer-health-analysis
      scaleDownDelaySeconds: 30
  template:
    metadata:
      labels:
        app: consumer
    spec:
      containers:
      - name: consumer
        image: bg-test-consumer:latest
        imagePullPolicy: Never
        ports:
        - containerPort: 8080
        env:
        - name: CONSUMER_INITIAL_STATE
          value: "STOPPED"  # 그룹 미가입 상태로 시작
        - name: SPRING_KAFKA_CONSUMER_GROUP_ID
          value: "bg-test-group"
        - name: SPRING_KAFKA_CONSUMER_PROPERTIES_GROUP_PROTOCOL
          value: "consumer"  # KIP-848 활성화
```

### 2.2 개별 그룹용 Rollout (A-2, B-2, C-2)

두 개의 Rollout 리소스로 Blue/Green을 독립 관리한다.

```yaml
# consumer-blue-rollout.yaml
apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata:
  name: consumer-blue
  namespace: bg-test
spec:
  replicas: 3
  strategy:
    blueGreen:
      activeService: consumer-blue-svc
      previewService: consumer-blue-preview-svc
      autoPromotionEnabled: true  # Blue는 자동 프로모션
  template:
    spec:
      containers:
      - name: consumer
        image: bg-test-consumer:latest
        env:
        - name: CONSUMER_INITIAL_STATE
          value: "ACTIVE"
        - name: SPRING_KAFKA_CONSUMER_GROUP_ID
          value: "bg-test-group-blue"

---
# consumer-green-rollout.yaml
apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata:
  name: consumer-green
  namespace: bg-test
spec:
  replicas: 0  # 초기 대기 상태
  strategy:
    blueGreen:
      activeService: consumer-green-svc
      previewService: consumer-green-preview-svc
      autoPromotionEnabled: true
  template:
    spec:
      containers:
      - name: consumer
        image: bg-test-consumer:latest
        env:
        - name: CONSUMER_INITIAL_STATE
          value: "ACTIVE"
        - name: SPRING_KAFKA_CONSUMER_GROUP_ID
          value: "bg-test-group-green"
```

### 2.3 AnalysisTemplate

#### prePromotion: 전환 실행 및 검증

```yaml
apiVersion: argoproj.io/v1alpha1
kind: AnalysisTemplate
metadata:
  name: consumer-switch-analysis
  namespace: bg-test
spec:
  args:
  - name: active-service
  - name: preview-service
  metrics:
  - name: switch-consumers
    provider:
      job:
        spec:
          template:
            spec:
              containers:
              - name: switch
                image: bg-webhook-job:latest
                command: ["./switch"]
                args:
                - "--active-service={{ args.active-service }}"
                - "--preview-service={{ args.preview-service }}"
                - "--action=switch"
                - "--namespace=bg-test"
              restartPolicy: Never
          backoffLimit: 1
  - name: consumer-lag-check
    initialDelay: 10s
    interval: 5s
    count: 6
    successCondition: "result[0] < 100"
    failureLimit: 2
    provider:
      prometheus:
        address: http://prometheus-kube-prometheus-prometheus.monitoring:9090
        query: |
          sum(kafka_consumergroup_lag{consumergroup="bg-test-group", topic="bg-test-topic"})
```

#### postPromotion: 안정성 확인

```yaml
apiVersion: argoproj.io/v1alpha1
kind: AnalysisTemplate
metadata:
  name: consumer-health-analysis
  namespace: bg-test
spec:
  metrics:
  - name: error-rate
    interval: 10s
    count: 6
    successCondition: "result[0] < 0.01"
    failureLimit: 2
    provider:
      prometheus:
        address: http://prometheus-kube-prometheus-prometheus.monitoring:9090
        query: |
          rate(bg_consumer_processing_errors_total{namespace="bg-test"}[1m])
  - name: consumer-lag-stable
    interval: 10s
    count: 6
    successCondition: "result[0] < 50"
    failureLimit: 2
    provider:
      prometheus:
        address: http://prometheus-kube-prometheus-prometheus.monitoring:9090
        query: |
          sum(kafka_consumergroup_lag{consumergroup="bg-test-group", topic="bg-test-topic"})
```

### 2.4 Service 리소스

```yaml
apiVersion: v1
kind: Service
metadata:
  name: consumer-active-svc
  namespace: bg-test
spec:
  selector:
    app: consumer
  ports:
  - port: 8080
    targetPort: 8080
---
apiVersion: v1
kind: Service
metadata:
  name: consumer-preview-svc
  namespace: bg-test
spec:
  selector:
    app: consumer
  ports:
  - port: 8080
    targetPort: 8080
```

> Argo Rollouts가 Service의 selector를 동적으로 관리하여 active/preview ReplicaSet을 구분한다.

---

## 3. Webhook Job 서비스 구현

### 3.1 역할

- Argo AnalysisTemplate의 webhook에서 호출하는 경량 Go 서비스
- K8s API로 Consumer Pod 목록 조회 → 각 Pod의 `/lifecycle/*` 엔드포인트 호출
- 전환 오케스트레이션 로직 (순서 보장, 타임아웃, 재시도)

### 3.2 구조

```
apps/webhook-job/
├── cmd/
│   └── switch/
│       └── main.go          # 전환 실행 진입점
├── internal/
│   ├── discovery/
│   │   └── pod_discovery.go # K8s API로 Pod IP 조회
│   └── lifecycle/
│       └── client.go        # Consumer lifecycle API 클라이언트
├── Dockerfile
├── go.mod
└── go.sum
```

### 3.3 전환 로직

```
switch --active-service=consumer-active-svc --preview-service=consumer-preview-svc --action=switch

1. active-svc 엔드포인트 조회 → Pod IP 목록 획득
2. preview-svc 엔드포인트 조회 → Pod IP 목록 획득
3. Active Pods: POST /lifecycle/stop (순차, 각 Pod 완료 대기)
4. Preview Pods: POST /lifecycle/start (병렬)
5. 전환 완료 확인: GET /lifecycle/status (모든 Pod ACTIVE 확인)
6. 종료 코드: 0(성공) / 1(실패)
```

---

## 4. Producer 유지

Producer 앱은 Phase 1과 동일하게 유지한다. 수정 없음.

---

## 5. Validator 재활용

`tools/validator/validator.py`는 Phase 1과 동일하게 재활용한다. 필요시 출력 포맷에 접근법/전략 조합 정보 추가.

---

## 6. 디렉토리 구조 계획

```
k8s/
├── (기존 Phase 1 매니페스트 유지)
└── rollouts/
    ├── consumer-rollout-single-group.yaml    # 단일 그룹용 Rollout
    ├── consumer-blue-rollout.yaml            # 개별 그룹 Blue Rollout
    ├── consumer-green-rollout.yaml           # 개별 그룹 Green Rollout
    ├── services.yaml                         # 관련 Service 리소스
    └── analysis/
        ├── consumer-switch-analysis.yaml     # prePromotion AnalysisTemplate
        ├── consumer-health-analysis.yaml     # postPromotion AnalysisTemplate
        └── consumer-lag-analysis.yaml        # Lag 전용 AnalysisTemplate
```

---

## 7. 완료 조건

- [ ] **인프라 업그레이드**: Minikube K8s v1.30.x 구성, Strimzi 0.50.1 설치, Kafka 4.1.1 배포
- [ ] **Consumer 앱 마이그레이션**: Spring Boot 3.4.x, kafka-clients 4.1.x, Jakarta EE 전환
- [ ] **KIP-848 설정**: `group.protocol=consumer` 적용, Classic Protocol 설정 제거
- [ ] Consumer 앱: Static Membership 제거, STOPPED 상태 추가, /lifecycle/start|stop API 구현
- [ ] Consumer Docker 이미지 재빌드 (`bg-test-consumer:v2`)
- [ ] Argo Rollouts v1.8.4 설치, Rollout 매니페스트 작성 (단일 그룹 + 개별 그룹)
- [ ] AnalysisTemplate 매니페스트 작성 (prePromotion + postPromotion)
- [ ] Service 매니페스트 작성
- [ ] Webhook Job 서비스 구현 및 Docker 이미지 빌드
- [ ] **모니터링 스택 업그레이드**: kube-prometheus-stack 69.x+, Loki 6.x
- [ ] **KEDA 업그레이드**: 2.17 설치
- [ ] 기존 Producer 배포 확인 (Spring Boot 3.4.x 마이그레이션 포함)
- [ ] `kubectl argo rollouts` CLI로 Rollout 기본 동작 확인
- [ ] KIP-848 동작 확인: Consumer 그룹 가입 시 점진적 리밸런싱 로그 확인
