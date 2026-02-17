# Kafka Consumer Blue/Green 배포 전략 설계서

> **버전:** v1.0  
> **작성일:** 2026-02-15  
> **적용 환경:** Kubernetes (온프레미스 / 클라우드 공통)

---

## 목차

1. [프로젝트 목표](#1-프로젝트-목표)
2. [핵심 도전과제](#2-핵심-도전과제)
3. [아키텍처 개요](#3-아키텍처-개요)
4. [배포 전략 비교 및 선택](#4-배포-전략-비교-및-선택)
5. [상세 설계: 전략 A — 단일 Consumer Group 방식](#5-상세-설계-전략-a--단일-consumer-group-방식)
6. [상세 설계: 전략 B — 별도 Consumer Group 방식](#6-상세-설계-전략-b--별도-consumer-group-방식)
7. [창의적 접근: Pause/Resume 기반 Zero-Lag Switch](#7-창의적-접근-pauseresume-기반-zero-lag-switch)
8. [K8s 매니페스트 예시](#8-k8s-매니페스트-예시)
9. [운영 절차 (Runbook)](#9-운영-절차-runbook)
10. [모니터링 및 알람 설계](#10-모니터링-및-알람-설계)
11. [참조 자료](#11-참조-자료)

---

## 1. 프로젝트 목표

| 목표 항목 | 설명 |
|-----------|------|
| **빠른 Blue/Green 전환** | 수 초 이내(< 30s) 컨슈머 트래픽 절체 |
| **즉시 롤백** | 이상 감지 시 1분 이내 이전 버전 복구 |
| **메시지 유실 Zero** | 전환 과정 중 Kafka 메시지 미처리 또는 중복 최소화 |
| **운영 자동화** | 수동 개입 없는 자동 헬스체크 기반 전환 |

---

## 2. 핵심 도전과제

Kafka Consumer의 Blue/Green 배포는 일반 HTTP 서비스와 근본적으로 다릅니다.

| 일반 HTTP 서비스 | Kafka Consumer |
|:---:|:---:|
| 로드밸런서로 트래픽 → X | 파티션 할당(Rebalance)으로 트래픽 |
| Ingress 전환으로 완료 | Consumer Group 상태 관리 필요 |
| Active/Standby 단순 | Partition Ownership 복잡성 |

**주요 문제점:**

- **Rebalance Stop-The-World:** Eager Rebalance 시 모든 컨슈머가 처리를 일시 중단
- **Partition Ownership 충돌:** Blue/Green이 동일 Group ID 사용 시 파티션 나눔 발생
- **Offset 동기화:** 전환 시점의 Offset 정합성 보장 필요
- **Pause/Resume 불안정성:** Rebalance 발생 시 Pause 상태가 초기화되는 문제 ([Confluent Kafka-Go #193](https://github.com/confluentinc/confluent-kafka-go/issues/193))

---

## 3. 아키텍처 개요

### 3.1 전체 구성도

```
┌─────────────────────────────────────────────────────────────────┐
│                        Kubernetes Cluster                        │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │                    Kafka Cluster                          │   │
│  │  Topic: order-events  [P0][P1][P2][P3][P4][P5][P6][P7]  │   │
│  └─────────────────────────┬─────────────────────┬──────────┘   │
│                             │                     │              │
│            ┌────────────────┘                     │              │
│            │                                      │              │
│  ┌─────────▼──────────┐             ┌─────────────▼──────────┐  │
│  │  Blue Deployment   │             │  Green Deployment       │  │
│  │  (v1.0 - Active)   │             │  (v2.0 - Standby)      │  │
│  │                    │             │                         │  │
│  │  [Pod-B1][Pod-B2]  │  전환 →     │  [Pod-G1][Pod-G2]      │  │
│  │  [Pod-B3][Pod-B4]  │  ←롤백      │  [Pod-G3][Pod-G4]      │  │
│  │                    │             │                         │  │
│  │  group.id: app-grp │             │  group.id: app-grp-v2  │  │
│  │  replica: 4        │             │  replica: 4 (준비완료)  │  │
│  └────────────────────┘             └─────────────────────────┘  │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  BG-Controller (Custom CRD / Argo Rollouts 확장)          │   │
│  │  - Offset Sync Watch        - Health Probe Check          │   │
│  │  - Partition Reassignment   - Rollback Trigger            │   │
│  └──────────────────────────────────────────────────────────┘   │
│                                                                  │
│  ┌────────────────────┐  ┌────────────────────┐                 │
│  │  Prometheus         │  │  ConfigMap          │                │
│  │  consumer_lag       │  │  active-version:    │                │
│  │  switch_duration    │  │    "blue"           │                │
│  └────────────────────┘  └────────────────────┘                 │
└─────────────────────────────────────────────────────────────────┘
```

### 3.2 전환 흐름도

```
     Blue Active                     Switch 트리거                   Green Active
         │                               │                               │
         ▼                               ▼                               ▼
  ┌──────────────┐    1. Pre-Check   ┌──────────────┐   5. Offset    ┌──────────────┐
  │  Blue: 소비  │ ─────────────────▶│  전환 준비    │ ─────────────▶│ Green: 소비  │
  │  [P0~P7]     │                   │  Validation   │                │  [P0~P7]     │
  └──────────────┘                   └──────┬───────┘                └──────────────┘
                                            │ 2. Green Deploy
                                            │    & 워밍업 대기
                                            ▼
                                     ┌──────────────┐
                                     │ Green Ready   │
                                     │ Partition=0   │
                                     │ (Standby)     │
                                     └──────┬───────┘
                                            │ 3. Blue Pause
                                            │    (consumer.pause())
                                            ▼
                                     ┌──────────────┐
                                     │ Blue: Paused  │
                                     │ Lag 소진 대기  │
                                     └──────┬───────┘
                                            │ 4. Lag = 0 확인
                                            │    Offset Commit
                                            ▼
                                     ┌──────────────┐
                                     │ Group 전환    │
                                     │ Green 활성화  │
                                     └──────┬───────┘
                                            │ 6. Health Probe
                                            │    성공 확인
                                            ▼
                                     ┌──────────────┐
                                     │ Blue Scale=0  │
                                     │ (보관 72h)    │
                                     └──────────────┘
```

---

## 4. 배포 전략 비교 및 선택

### 4.1 전략 옵션 비교표

| 전략 | Group ID | 전환 방식 | 전환 시간 | 메시지 안전성 | 복잡도 | 롤백 속도 |
|------|----------|-----------|-----------|---------------|--------|-----------|
| **A. 단일 Group + Cooperative Rebalance** | 공유 | 파티션 재분배 | 10~30초 | 높음 | 낮음 | 빠름 |
| **B. 별도 Group + Offset 동기화** | 분리 | Offset 복사 후 전환 | 30~120초 | 매우 높음 | 중간 | 중간 |
| **C. Pause/Resume + Atomic Switch** | 공유 | Pause → Group 전환 → Resume | 5~15초 | 최고 | 높음 | 매우 빠름 |
| **D. Shadow Consumer (Blackhole Sink)** | 분리 | 병렬 소비 → 검증 → 전환 | 분 단위 | 최고 | 매우 높음 | 빠름 |

### 4.2 권장 전략

> **운영 목표(빠른 전환 + 즉시 롤백)** 기준으로 **전략 C (Pause/Resume + Atomic Switch)** 를 주 전략으로 채택하되, Consumer Group 운영 방식에 따라 A 또는 B를 보조 전략으로 활용

---

## 5. 상세 설계: 전략 A — 단일 Consumer Group 방식

### 5.1 개요

Blue/Green이 **동일한 Consumer Group ID**를 사용하며, Cooperative Sticky Assignor를 통해 파티션을 점진적으로 Green으로 이전합니다.

### 5.2 핵심 설정

```yaml
# Kafka Consumer 필수 설정
partition.assignment.strategy: CooperativeStickyAssignor  # 점진적 파티션 이전
group.instance.id: "${POD_NAME}"                          # Static Membership (K8s StatefulSet)
session.timeout.ms: 45000                                 # Pod 재시작 시 Rebalance 방지
heartbeat.interval.ms: 3000
max.poll.interval.ms: 300000
```

### 5.3 전환 순서

```
단계 1: Green Pods 배포 (replica=N, group.id=동일)
   ↓ Cooperative Rebalance 자동 발생
   ↓ Blue [P0~P3] 유지, Green [P4~P7] 할당

단계 2: Blue Scale Down to 0
   ↓ 남은 파티션 [P0~P3] → Green으로 자동 이전
   ↓ Total 전환 완료

롤백: Green Scale Down → Blue Scale Up
   ↓ 파티션 자동 복귀
```

### 5.4 주의사항

- Blue와 Green의 `group.instance.id`가 겹치지 않도록 Pod명 기반 설정 필수
- Spring Kafka 버전 간 Rebalance Protocol 호환성 확인 필요 ([Spring Kafka #2277](https://github.com/spring-projects/spring-kafka/issues/2277))
- 토폴로지 변경이 큰 경우 Cooperative Rebalance 충돌 가능성 있음 ([Airwallex Engineering](https://medium.com/airwallex-engineering/kafka-streams-iterative-development-and-blue-green-deployment-fae88b26e75e))

---

## 6. 상세 설계: 전략 B — 별도 Consumer Group 방식

### 6.1 개요

Blue(`app-consumer-blue`)와 Green(`app-consumer-green`)이 **별도 Consumer Group**을 사용하며, Offset 동기화 후 트래픽을 전환합니다.

### 6.2 파티션 전략 (Rebalancing Partition Technique)

파티션 수를 컨슈머 수의 2배 이상으로 구성하여 Blue/Green 모두 활성 소비 가능 상태를 만들 수 있습니다. ([Technical Disclosure Commons - Blue Green for Kafka](https://www.tdcommons.org/dpubs_series/6318/))

```
Topic Partitions: 8개
Blue Consumers: 4개  → 각 2 파티션
Green Consumers: 4개 → 각 2 파티션 (별도 Group으로 중복 소비)

전환 시: Green Group Offset = Blue Group 현재 Offset
         Blue Group Scale = 0
```

### 6.3 Offset 동기화 절차

```bash
# Green Consumer Group의 Offset을 Blue의 현재 값으로 강제 설정
kafka-consumer-groups.sh \
  --bootstrap-server kafka:9092 \
  --group app-consumer-green \
  --topic order-events \
  --reset-offsets \
  --to-offset <blue-current-offset> \
  --execute
```

### 6.4 ConfigMap 기반 Active 버전 관리

```yaml
# active-version ConfigMap으로 애플리케이션이 읽어 활성/비활성 결정
apiVersion: v1
kind: ConfigMap
metadata:
  name: kafka-consumer-active-version
data:
  active: "blue"        # "blue" | "green"
  switch-timestamp: ""
  rollback-allowed: "true"
```

---

## 7. 창의적 접근: Pause/Resume 기반 Zero-Lag Switch

> 이 방식은 기존 Rebalance 방식의 한계를 극복하는 **독창적 설계**입니다.

### 7.1 핵심 아이디어

Kafka Consumer API의 `pause()`/`resume()` 기능과 Kubernetes의 ConfigMap Watch를 결합하여, **Rebalance 없이** 컨슈머 소비를 원자적(Atomic)으로 전환합니다.

> **문제점 해결:** Rebalance 발생 시 Pause 상태가 초기화되는 문제를 Static Membership + 별도 Group ID 조합으로 해결

### 7.2 BG-Switch Controller 설계

```
┌─────────────────────────────────────────────────────┐
│              BG-Switch Controller Pod                │
│                                                      │
│  ┌─────────────────┐    ┌──────────────────────┐   │
│  │  ConfigMap       │    │  Kafka Admin Client   │   │
│  │  Watcher         │    │  - describeGroups()   │   │
│  │  (active버전감시) │    │  - listOffsets()      │   │
│  └────────┬────────┘    └──────────┬───────────┘   │
│           │                        │                  │
│           ▼                        ▼                  │
│  ┌─────────────────────────────────────────────┐    │
│  │         Switch Orchestrator                  │    │
│  │                                              │    │
│  │  1. Blue에 HTTP POST /pause 신호             │    │
│  │  2. Lag Monitor → lag == 0 대기              │    │
│  │  3. Offset Snapshot 기록                    │    │
│  │  4. Green에 HTTP POST /activate 신호         │    │
│  │  5. Green Health Probe 성공 확인             │    │
│  │  6. Blue Scale → 0 (또는 Standby 유지)       │    │
│  └─────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────┘
```

### 7.3 애플리케이션 내 Pause/Resume 엔드포인트

각 Consumer Pod는 아래 HTTP 엔드포인트를 노출하여 Controller로부터 신호를 받습니다:

```kotlin
// Spring Boot + Spring Kafka 예시
@RestController
class ConsumerLifecycleController(
    private val listenerContainer: KafkaListenerEndpointRegistry
) {
    // BG-Switch Controller가 호출
    @PostMapping("/actuator/kafka/pause")
    fun pause(): ResponseEntity<String> {
        listenerContainer.allListenerContainers.forEach { it.pause() }
        return ResponseEntity.ok("paused")
    }

    @PostMapping("/actuator/kafka/resume")
    fun resume(): ResponseEntity<String> {
        listenerContainer.allListenerContainers.forEach { it.resume() }
        return ResponseEntity.ok("resumed")
    }

    // Health 상태 노출 (Prometheus 수집)
    @GetMapping("/actuator/kafka/status")
    fun status(): Map<String, Any> = mapOf(
        "paused" to listenerContainer.allListenerContainers.any { it.isContainerPaused },
        "lag" to getCurrentLag(),
        "assignedPartitions" to getAssignedPartitions()
    )
}
```

### 7.4 Shawarma 패턴 응용 (Sidecar 방식)

CenterEdge의 [Shawarma 패턴](https://btburnett.com/kubernetes/microservices/continuous%20delivery/2019/08/12/shawarma.html)을 응용하여, 애플리케이션 코드 수정 없이 Sidecar가 Pause/Resume을 중재합니다:

```
┌─────────────────────────────────────┐
│           Consumer Pod               │
│                                      │
│  ┌──────────────────┐               │
│  │  App Container   │               │
│  │  (Consumer Logic)│◀──HTTP POST──┐│
│  └──────────────────┘              ││
│                                    ││
│  ┌──────────────────┐              ││
│  │  BG-Sidecar       │─────────────┘│
│  │  (Go Container)   │              │
│  │  - K8s API Watch  │              │
│  │  - ConfigMap 감시 │              │
│  │  - Active 여부 판단│              │
│  └──────────────────┘              │
└─────────────────────────────────────┘
```

### 7.5 KIP-848 (New Rebalance Protocol) 활용 전망

Kafka 4.0+에서 지원되는 KIP-848은 브로커 사이드 협업 리밸런싱으로, 대규모 Consumer Group(10개 이상)의 리밸런싱 시간을 103초 → 5초로 단축합니다. ([Karafka KIP-848 문서](https://karafka.io/docs/Kafka-New-Rebalance-Protocol/))

```yaml
# KIP-848 활성화 (Kafka 4.0+)
group.protocol: consumer
group.remote.assignor: uniform
```

---

## 8. K8s 매니페스트 예시

### 8.1 Blue Deployment (현재 활성)

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: order-consumer-blue
  namespace: kafka-consumers
  labels:
    app: order-consumer
    version: blue
    environment: production
spec:
  replicas: 4
  selector:
    matchLabels:
      app: order-consumer
      version: blue
  template:
    metadata:
      labels:
        app: order-consumer
        version: blue
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "8080"
        prometheus.io/path: "/actuator/prometheus"
    spec:
      terminationGracePeriodSeconds: 60  # graceful shutdown 보장
      containers:
      - name: order-consumer
        image: registry.example.com/order-consumer:v1.0.0
        ports:
        - containerPort: 8080
          name: http
        env:
        - name: KAFKA_BOOTSTRAP_SERVERS
          value: "kafka-broker:9092"
        - name: KAFKA_GROUP_ID
          value: "order-consumer-blue"
        - name: KAFKA_GROUP_INSTANCE_ID
          valueFrom:
            fieldRef:
              fieldPath: metadata.name    # Pod명으로 Static Membership
        - name: KAFKA_AUTO_OFFSET_RESET
          value: "earliest"
        - name: KAFKA_PARTITION_ASSIGNMENT_STRATEGY
          value: "org.apache.kafka.clients.consumer.CooperativeStickyAssignor"
        - name: BG_ACTIVE_VERSION
          valueFrom:
            configMapKeyRef:
              name: kafka-consumer-active-version
              key: active
        resources:
          requests:
            memory: "512Mi"
            cpu: "500m"
          limits:
            memory: "1Gi"
            cpu: "1000m"
        livenessProbe:
          httpGet:
            path: /actuator/health/liveness
            port: 8080
          initialDelaySeconds: 30
          periodSeconds: 10
          failureThreshold: 3
        readinessProbe:
          httpGet:
            path: /actuator/health/readiness
            port: 8080
          initialDelaySeconds: 20
          periodSeconds: 5
          failureThreshold: 3
        lifecycle:
          preStop:
            exec:
              command: ["/bin/sh", "-c", "sleep 10"]  # graceful drain
---
apiVersion: v1
kind: Service
metadata:
  name: order-consumer-blue-svc
  namespace: kafka-consumers
spec:
  selector:
    app: order-consumer
    version: blue
  ports:
  - name: http
    port: 8080
    targetPort: 8080
  type: ClusterIP
```

### 8.2 Green Deployment (신 버전 대기)

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: order-consumer-green
  namespace: kafka-consumers
  labels:
    app: order-consumer
    version: green
    environment: production
spec:
  replicas: 0  # 초기 비활성 (전환 시 4로 변경)
  selector:
    matchLabels:
      app: order-consumer
      version: green
  template:
    metadata:
      labels:
        app: order-consumer
        version: green
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "8080"
        prometheus.io/path: "/actuator/prometheus"
    spec:
      terminationGracePeriodSeconds: 60
      containers:
      - name: order-consumer
        image: registry.example.com/order-consumer:v2.0.0  # 신 버전
        ports:
        - containerPort: 8080
          name: http
        env:
        - name: KAFKA_BOOTSTRAP_SERVERS
          value: "kafka-broker:9092"
        - name: KAFKA_GROUP_ID
          value: "order-consumer-green"     # 별도 Group ID
        - name: KAFKA_GROUP_INSTANCE_ID
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: KAFKA_AUTO_OFFSET_RESET
          value: "none"  # 반드시 외부에서 Offset 주입
        - name: KAFKA_PARTITION_ASSIGNMENT_STRATEGY
          value: "org.apache.kafka.clients.consumer.CooperativeStickyAssignor"
        - name: BG_ACTIVE_VERSION
          valueFrom:
            configMapKeyRef:
              name: kafka-consumer-active-version
              key: active
        resources:
          requests:
            memory: "512Mi"
            cpu: "500m"
          limits:
            memory: "1Gi"
            cpu: "1000m"
        livenessProbe:
          httpGet:
            path: /actuator/health/liveness
            port: 8080
          initialDelaySeconds: 30
          periodSeconds: 10
        readinessProbe:
          httpGet:
            path: /actuator/health/readiness
            port: 8080
          initialDelaySeconds: 20
          periodSeconds: 5
```

### 8.3 Active 버전 ConfigMap

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kafka-consumer-active-version
  namespace: kafka-consumers
  labels:
    app: order-consumer
    managed-by: bg-controller
data:
  active: "blue"                    # "blue" | "green"
  switch-timestamp: ""
  previous-version: ""
  rollback-allowed: "true"
  rollback-window: "3600"          # 롤백 허용 시간(초), 1시간
```

### 8.4 BG-Switch Controller Job

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: bg-switch-controller
  namespace: kafka-consumers
spec:
  template:
    spec:
      serviceAccountName: bg-controller-sa
      restartPolicy: Never
      containers:
      - name: bg-controller
        image: registry.example.com/bg-controller:latest
        env:
        - name: KAFKA_BOOTSTRAP_SERVERS
          value: "kafka-broker:9092"
        - name: BLUE_SERVICE
          value: "order-consumer-blue-svc.kafka-consumers.svc.cluster.local:8080"
        - name: GREEN_SERVICE
          value: "order-consumer-green-svc.kafka-consumers.svc.cluster.local:8080"
        - name: TARGET_VERSION
          value: "green"            # 전환 대상
        - name: LAG_THRESHOLD
          value: "0"                # Lag=0 확인 후 전환
        - name: HEALTH_CHECK_RETRIES
          value: "10"
        - name: NAMESPACE
          value: "kafka-consumers"
---
# BG Controller ServiceAccount & RBAC
apiVersion: v1
kind: ServiceAccount
metadata:
  name: bg-controller-sa
  namespace: kafka-consumers
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: bg-controller-role
  namespace: kafka-consumers
rules:
- apiGroups: ["apps"]
  resources: ["deployments", "deployments/scale"]
  verbs: ["get", "list", "update", "patch"]
- apiGroups: [""]
  resources: ["configmaps"]
  verbs: ["get", "list", "update", "patch"]
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: bg-controller-rolebinding
  namespace: kafka-consumers
subjects:
- kind: ServiceAccount
  name: bg-controller-sa
  namespace: kafka-consumers
roleRef:
  kind: Role
  name: bg-controller-role
  apiGroup: rbac.authorization.k8s.io
```

### 8.5 KEDA ScaledObject (Kafka Lag 기반 자동 스케일링)

```yaml
# KEDA로 Consumer Lag에 따른 자동 스케일링 (전환 후 Green에 적용)
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: order-consumer-green-scaler
  namespace: kafka-consumers
spec:
  scaleTargetRef:
    name: order-consumer-green
  minReplicaCount: 2
  maxReplicaCount: 8
  triggers:
  - type: kafka
    metadata:
      bootstrapServers: kafka-broker:9092
      consumerGroup: order-consumer-green
      topic: order-events
      lagThreshold: "100"           # Lag 100 초과 시 스케일 업
      offsetResetPolicy: earliest
```

### 8.6 Argo Rollouts (고급 자동화, 선택 사항)

```yaml
# Argo Rollouts를 사용한 Blue/Green 자동화
# 참고: HTTP 기반 트래픽이 아닌 Kafka Consumer이므로
#       activeService/previewService는 내부 관리용으로만 사용
apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata:
  name: order-consumer-rollout
  namespace: kafka-consumers
spec:
  replicas: 4
  selector:
    matchLabels:
      app: order-consumer
  template:
    metadata:
      labels:
        app: order-consumer
    spec:
      terminationGracePeriodSeconds: 60
      containers:
      - name: order-consumer
        image: registry.example.com/order-consumer:v2.0.0
        env:
        - name: KAFKA_GROUP_INSTANCE_ID
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
  strategy:
    blueGreen:
      activeService: order-consumer-active-svc
      previewService: order-consumer-preview-svc
      autoPromotionEnabled: false   # 수동 승인 후 전환
      scaleDownDelaySeconds: 300    # 전환 후 5분간 Blue 유지 (롤백 대비)
      prePromotionAnalysis:
        templates:
        - templateName: kafka-consumer-health-check
        args:
        - name: service-name
          value: order-consumer-preview-svc
      postPromotionAnalysis:
        templates:
        - templateName: kafka-lag-analysis
---
# 사전 전환 분석 템플릿
apiVersion: argoproj.io/v1alpha1
kind: AnalysisTemplate
metadata:
  name: kafka-consumer-health-check
  namespace: kafka-consumers
spec:
  args:
  - name: service-name
  metrics:
  - name: consumer-error-rate
    interval: 30s
    count: 5
    successCondition: result[0] < 0.01    # 에러율 1% 미만
    provider:
      prometheus:
        address: http://prometheus:9090
        query: |
          rate(kafka_consumer_errors_total{
            service="{{args.service-name}}"
          }[2m])
```

---

## 9. 운영 절차 (Runbook)

### 9.1 사전 준비 체크리스트

```
배포 전 확인사항 (D-1)
──────────────────────────────────────────────────────
□ Green 이미지 빌드 완료 및 레지스트리 등록 확인
□ 스테이징 환경에서 Green 버전 기능 검증 완료
□ Blue Consumer Lag 정상 수준 확인 (< 100)
□ Kafka 브로커 상태 정상 확인
□ Prometheus/Grafana 모니터링 대시보드 접근 확인
□ 롤백 절차 숙지 및 담당자 대기
□ 작업 시간대 확인 (저트래픽 시간대 권장)
```

### 9.2 Blue → Green 전환 절차 (상세)

```
STEP 1: Green 배포 준비 (예상 소요: 3~5분)
────────────────────────────────────────────
1.1 Green Deployment image 업데이트
    kubectl set image deployment/order-consumer-green \
      order-consumer=registry.example.com/order-consumer:v2.0.0 \
      -n kafka-consumers

1.2 Green Deployment Replica 확장
    kubectl scale deployment order-consumer-green \
      --replicas=4 -n kafka-consumers

1.3 Green Pod 기동 완료 확인 (모든 Pod Ready 상태)
    kubectl rollout status deployment/order-consumer-green \
      -n kafka-consumers

1.4 Green 애플리케이션 자체 헬스체크 확인
    kubectl exec -n kafka-consumers \
      $(kubectl get pod -l version=green -n kafka-consumers \
        -o jsonpath='{.items[0].metadata.name}') \
      -- curl -s localhost:8080/actuator/health | jq .

    ✅ 기대 결과: {"status":"UP"}

────────────────────────────────────────────
STEP 2: Blue Consumer Pause (예상 소요: 1~2분)
────────────────────────────────────────────
2.1 Blue 모든 Pod에 Pause 신호 전송
    for pod in $(kubectl get pods -n kafka-consumers \
      -l version=blue -o name); do
      kubectl exec -n kafka-consumers $pod -- \
        curl -X POST localhost:8080/actuator/kafka/pause
    done

2.2 Blue Consumer Lag 소진 확인 (Lag = 0 대기)
    # 30초 간격으로 Lag 모니터링 (최대 5분 대기)
    watch -n 5 "kafka-consumer-groups.sh \
      --bootstrap-server kafka-broker:9092 \
      --group order-consumer-blue \
      --describe | grep order-events"

    ✅ 기대 결과: LAG 컬럼 = 0

    ⚠️  5분 내 Lag 미소진 시: STEP 2 대기 연장 또는 중단 검토

────────────────────────────────────────────
STEP 3: Green Offset 동기화 (예상 소요: 1분)
────────────────────────────────────────────
3.1 Blue Consumer 현재 Offset 스냅샷 저장
    kafka-consumer-groups.sh \
      --bootstrap-server kafka-broker:9092 \
      --group order-consumer-blue \
      --describe > /tmp/blue-offset-snapshot-$(date +%Y%m%d%H%M%S).txt

3.2 Green Consumer Group Offset을 Blue 현재값으로 동기화
    # 파티션별 현재 Offset 확인 후 Green Group에 설정
    kafka-consumer-groups.sh \
      --bootstrap-server kafka-broker:9092 \
      --group order-consumer-green \
      --topic order-events \
      --reset-offsets \
      --to-current \
      --execute

    # 또는 특정 Offset으로 지정 시
    # --to-offset <OFFSET_VALUE>

3.3 Offset 동기화 결과 확인
    kafka-consumer-groups.sh \
      --bootstrap-server kafka-broker:9092 \
      --group order-consumer-green \
      --describe

────────────────────────────────────────────
STEP 4: Active 버전 전환 (예상 소요: 10초)
────────────────────────────────────────────
4.1 ConfigMap 업데이트 (활성 버전: blue → green)
    kubectl patch configmap kafka-consumer-active-version \
      -n kafka-consumers \
      --type merge \
      -p '{"data":{"active":"green",
                    "previous-version":"blue",
                    "switch-timestamp":"'"$(date -u +%Y-%m-%dT%H:%M:%SZ)"'"}}'

4.2 Green Consumer 활성화 확인
    kubectl exec -n kafka-consumers \
      $(kubectl get pod -l version=green -n kafka-consumers \
        -o jsonpath='{.items[0].metadata.name}') \
      -- curl -s localhost:8080/actuator/kafka/status | jq .

    ✅ 기대 결과: {"paused": false, "assignedPartitions": [...]}

────────────────────────────────────────────
STEP 5: 전환 후 검증 (예상 소요: 5~10분)
────────────────────────────────────────────
5.1 Green Consumer Lag 모니터링
    watch -n 5 "kafka-consumer-groups.sh \
      --bootstrap-server kafka-broker:9092 \
      --group order-consumer-green \
      --describe"

    ✅ 기대 결과: LAG 정상 수준 유지 (< 100)

5.2 에러율 확인 (Prometheus)
    curl -s 'http://prometheus:9090/api/v1/query' \
      --data-urlencode \
      'query=rate(kafka_consumer_errors_total{group="order-consumer-green"}[5m])' \
      | jq '.data.result'

    ✅ 기대 결과: 에러율 < 1%

5.3 비즈니스 메트릭 확인 (서비스별 기준 적용)
    - 처리량(TPS)이 전환 전 수준으로 회복
    - DB/외부 API 오류 없음

────────────────────────────────────────────
STEP 6: Blue Scale Down (예상 소요: 1분)
────────────────────────────────────────────
6.1 Blue Deployment Scale Down (보관: replicas=0)
    kubectl scale deployment order-consumer-blue \
      --replicas=0 -n kafka-consumers

    # ⚠️ 72시간 보관 후 삭제 (롤백 윈도우)
    # 완전 삭제 시: kubectl delete deployment order-consumer-blue -n kafka-consumers

6.2 배포 완료 공지 및 기록
    - 전환 완료 시간 기록
    - 모니터링 대시보드 기준선 업데이트
```

### 9.3 롤백 절차 (Green → Blue 복구)

```
[긴급 롤백 - 이상 감지 즉시]
──────────────────────────────────────────────────
⏱️ 목표 롤백 시간: 2분 이내

R-1: Blue Scale Up 즉시 실행
     kubectl scale deployment order-consumer-blue \
       --replicas=4 -n kafka-consumers

R-2: Blue Pod Ready 확인 (Static Membership으로 Rebalance 최소화)
     kubectl rollout status deployment/order-consumer-blue \
       -n kafka-consumers --timeout=60s

R-3: Green Consumer Pause
     for pod in $(kubectl get pods -n kafka-consumers \
       -l version=green -o name); do
       kubectl exec -n kafka-consumers $pod -- \
         curl -X POST localhost:8080/actuator/kafka/pause
     done

R-4: Offset 복원 (스냅샷 파일 사용)
     # 전환 시 저장한 Blue Offset으로 복원
     kafka-consumer-groups.sh \
       --bootstrap-server kafka-broker:9092 \
       --group order-consumer-blue \
       --topic order-events \
       --reset-offsets \
       --to-current \
       --execute

R-5: ConfigMap 롤백
     kubectl patch configmap kafka-consumer-active-version \
       -n kafka-consumers \
       --type merge \
       -p '{"data":{"active":"blue",
                     "switch-timestamp":"'"$(date -u +%Y-%m-%dT%H:%M:%SZ)"'"}}'

R-6: Blue Consumer 정상 확인 후 Green Scale Down
     kubectl scale deployment order-consumer-green \
       --replicas=0 -n kafka-consumers

R-7: 장애 원인 분석 및 인시던트 기록
```

### 9.4 전환 판단 기준 (Go/No-Go)

| 지표 | Go 기준 | No-Go 기준 |
|------|---------|------------|
| Blue Consumer Lag | = 0 | > 0 |
| Green Pod 상태 | 모두 Running+Ready | 1개라도 Not Ready |
| Green 에러율 | < 1% | ≥ 1% |
| Green 처리 TPS | ≥ Blue 처리량의 90% | < 90% |
| Kafka 브로커 상태 | 정상 | ISR 감소 or 브로커 다운 |

---

## 10. 모니터링 및 알람 설계

### 10.1 핵심 메트릭

```yaml
# Prometheus Rules
groups:
- name: kafka-consumer-bluegreen
  rules:

  # 전환 중 Lag 급증 알람
  - alert: KafkaConsumerLagHigh
    expr: |
      kafka_consumer_group_lag{group=~"order-consumer-.*"} > 500
    for: 2m
    labels:
      severity: critical
    annotations:
      summary: "Kafka Consumer Lag이 임계값 초과"
      description: "Consumer Group {{ $labels.group }} lag: {{ $value }}"

  # Blue/Green 전환 소요시간 알람
  - alert: BGSwitchTooLong
    expr: |
      bg_switch_duration_seconds > 60
    for: 0m
    labels:
      severity: warning
    annotations:
      summary: "Blue/Green 전환이 60초 초과"

  # Green Consumer 미기동 알람 (전환 후)
  - alert: GreenConsumerNotRunning
    expr: |
      kube_deployment_status_replicas_ready{deployment="order-consumer-green"} == 0
      and on() kafka_consumer_active_version{version="green"} == 1
    for: 1m
    labels:
      severity: critical
    annotations:
      summary: "Active 버전이 Green이지만 Green Pod가 없음"
```

### 10.2 Grafana 대시보드 구성 (권장 패널)

```
Row 1: Blue/Green 상태 개요
  - Active Version Badge (blue/green)
  - Blue Pod Count / Green Pod Count
  - 전환 상태 (준비중 / 전환중 / 완료)

Row 2: Consumer Lag 비교
  - Blue Consumer Lag (시계열)
  - Green Consumer Lag (시계열)
  - 파티션별 Lag 히트맵

Row 3: 처리 성능
  - Messages/sec (Blue vs Green)
  - Consumer 에러율
  - 평균 처리 지연시간(ms)

Row 4: 롤백 가능성 지표
  - Rollback Window 남은 시간
  - Blue Offset Snapshot 신선도
  - 마지막 전환 타임스탬프
```

---

## 11. 참조 자료

| 출처 | 내용 | URL |
|------|------|-----|
| Expedia Group Tech | Kafka Blue-Green Cluster 배포 전략 | https://medium.com/expedia-group-tech/kafka-blue-green-deployment-212065b7fee7 |
| Airwallex Engineering | Kafka Streams Blue/Green 배포 및 Cooperative Rebalance | https://medium.com/airwallex-engineering/kafka-streams-iterative-development-and-blue-green-deployment-fae88b26e75e |
| Technical Disclosure Commons | Kafka 파티션 Rebalancing 기반 Blue/Green 전략 | https://www.tdcommons.org/dpubs_series/6318/ |
| CenterEdge / btburnett | Shawarma 사이드카 패턴 (메시지 버스 Blue/Green) | https://btburnett.com/kubernetes/microservices/continuous%20delivery/2019/08/12/shawarma.html |
| Streaming Data Tech | Blackhole Sink Pattern (Lyft 사례 포함) | https://www.streamingdata.tech/p/blackhole-sink-pattern-for-blue-green |
| ASF JIRA KAFKA-2350 | Kafka Consumer pause/resume API 설계 배경 | https://issues.apache.org/jira/browse/KAFKA-2350 |
| Confluent Kafka-Go #193 | Rebalance 시 Pause 상태 초기화 문제 | https://github.com/confluentinc/confluent-kafka-go/issues/193 |
| Spring Kafka #2277 | Spring Kafka 버전 간 Rebalance Protocol 호환성 | https://github.com/spring-projects/spring-kafka/issues/2277 |
| Argo Rollouts 공식 문서 | Blue/Green 배포 CRD 및 AnalysisTemplate | https://argo-rollouts.readthedocs.io/en/stable/features/bluegreen/ |
| KEDA 공식 문서 | Kafka Scaler + Argo Rollouts 연동 | https://keda.sh/blog/2020-11-04-keda-2.0-release/ |
| Karafka 문서 | KIP-848 새로운 Rebalance 프로토콜 (Kafka 4.0+) | https://karafka.io/docs/Kafka-New-Rebalance-Protocol/ |
| Confluent | Kafka Rebalancing 상세 설명 | https://www.confluent.io/learn/kafka-rebalancing/ |
| bakdata Medium | K8s 환경 Kafka Static Membership 활용 | https://medium.com/bakdata/solving-my-weird-kafka-rebalancing-problems-c05e99535435 |
| Cloudflare Blog | K8s Kafka Consumer 자동 재시작 및 헬스체크 | https://blog.cloudflare.com/intelligent-automatic-restarts-for-unhealthy-kafka-consumers/ |

---

*본 설계서는 참조 문서 및 실제 운영 사례를 기반으로 작성되었으며, 실제 환경 적용 전 스테이징 검증을 권장합니다.*
