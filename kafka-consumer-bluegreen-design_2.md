# Kafka Consumer Blue/Green 배포 전략 설계서

## Pause/Resume Atomic Switch 방식 심층 분석

> **문서 목적**: Kafka Consumer의 `pause()`/`resume()` API를 활용한 Blue/Green Atomic Switch 전략의 실현 가능성, 잠재적 문제점, 기존 유사 사례를 종합적으로 분석하고, 이를 기반으로 실전 적용 가능한 설계안을 제시한다.

---

## 1. 핵심 질문: 왜 Pause/Resume Atomic Switch가 일반화되지 않았는가?

### 1.1 조사 결과 요약

광범위한 리서치를 통해 Pause/Resume 기반 Blue/Green 전환 방식은 **이론적으로 매우 유효하며, 실제로 이 방식을 구현한 사례가 존재**함을 확인했다. 그러나 범용 도구로 일반화되지 못한 데에는 구조적 이유들이 있다.

#### 발견된 유사 사례 및 도구

| 프로젝트/사례 | 방식 | 상태 |
|---|---|---|
| **Shawarma** (CenterEdge Software) | K8s Sidecar가 Service Endpoint 상태를 감시하여 HTTP POST로 앱에 active/inactive 통지 → 앱이 메시지 버스 처리를 시작/중지 | 오픈소스, 실제 프로덕션 사용. 단, .NET 에코시스템 중심 |
| **Spring Kafka Pause/Resume** | `KafkaListenerEndpointRegistry`를 통해 런타임에 Consumer를 pause/resume | Spring 프레임워크 내장 기능. 배포 오케스트레이션 도구와의 통합은 별도 구현 필요 |
| **Feature Flag 기반 Pause** (Improving사 사례) | Unleash 등 Feature Flag 도구로 poll loop 내에서 동적으로 pause/resume 제어 | 블로그 레벨 사례. 범용 도구화되지 않음 |
| **Lyft Blackhole Sink Pattern** | Flink/Kafka Streams에서 Blue/Green 전환 시 sink를 비활성화하여 출력 차단 | Flink Kubernetes Operator에 기여됨. Consumer가 아닌 Streaming Job 대상 |

> **참조**: [Shawarma GitHub](https://github.com/CenterEdge/shawarma) / [Shawarma 블로그](https://btburnett.com/kubernetes/microservices/continuous%20delivery/2019/08/12/shawarma.html) / [Feature Flag + Kafka](https://www.improving.com/thoughts/unleashing-feature-flags-onto-kafka-consumers/) / [Blackhole Sink Pattern](https://www.streamingdata.tech/p/blackhole-sink-pattern-for-blue-green)

---

### 1.2 일반화되지 못한 구조적 이유

#### 이유 1: Kafka Consumer의 Thread-Safety 제약

Kafka Consumer API는 **단일 스레드에서만 안전**하게 동작한다. 외부에서 HTTP 엔드포인트를 통해 pause/resume을 호출하면 `ConcurrentModificationException`이 발생한다.

```
java.util.ConcurrentModificationException: KafkaConsumer is not safe for multi-threaded access
```

이를 해결하려면 `AtomicBoolean` 플래그를 두고 poll loop 내에서 간접적으로 pause/resume을 실행해야 하며, 이는 프레임워크별로 구현 방식이 달라 범용화가 어렵다.

> **참조**: [Micronaut Kafka Issue #19](https://github.com/micronaut-projects/micronaut-kafka/issues/19) / [Red Hat Developer - Pause/Resume](https://developers.redhat.com/articles/2023/12/01/how-avoid-rebalances-and-disconnections-kafka-consumers)

#### 이유 2: Rebalance 시 Pause 상태 유실

Kafka Consumer의 `pause()`는 **파티션 할당에 종속적**이다. Consumer Group에서 rebalance가 발생하면:

- 기존에 pause된 파티션이 revoke되고 새로 assign될 때 **pause 상태가 리셋**된다
- 새로 할당된 파티션은 자동으로 resume 상태가 되어, **의도치 않게 메시지를 소비**할 수 있다

이 문제는 Spring Kafka 프로젝트에서도 공식 이슈로 등록되어 있다.

> **참조**: [Spring Kafka Issue #2222 - Do Not Resume Paused Partitions After Rebalance](https://github.com/spring-projects/spring-kafka/issues/2222) / [Confluent Kafka Go Issue #193](https://github.com/confluentinc/confluent-kafka-go/issues/193)

#### 이유 3: 애플리케이션 침투적(Intrusive) 설계

Pause/Resume 전환은 **Consumer 애플리케이션 코드에 변경이 필요**하다. HTTP 기반 트래픽 전환과 달리, 메시지 버스 Consumer에 대한 제어는 애플리케이션 내부에서 이루어져야 한다. 이는 다양한 프레임워크(Spring, Micronaut, Quarkus, Node.js 등)마다 별도 구현이 필요하여 범용 도구로 만들기 어렵다.

> Shawarma 프로젝트의 Brant Burnett도 이 점을 인식하고, Sidecar(인프라) + 간단한 HTTP 엔드포인트(앱)로 관심사를 분리하는 접근을 택했다.

#### 이유 4: Argo Rollouts의 명시적 한계

Argo Rollouts 공식 문서에서 다음과 같이 명시하고 있다:

> *"Argo Rollouts doesn't control traffic flow for connections it doesn't understand (i.e. binary/queue channels)."*

즉, HTTP/gRPC가 아닌 Kafka Consumer와 같은 pull 기반 워크로드에 대해서는 Argo Rollouts가 직접 제어하지 않는다. Blue/Green은 지원하지만, **파티션 할당이라는 Kafka 고유의 트래픽 라우팅**은 범위 밖이다.

> **참조**: [Argo Rollouts Concepts](https://argo-rollouts.readthedocs.io/en/stable/concepts/) / [Argo Rollouts Issue #3539](https://github.com/argoproj/argo-rollouts/issues/3539)

---

## 2. Pause/Resume Atomic Switch의 잠재적 문제점 분석

### 2.1 Critical 위험요소

| # | 문제점 | 심각도 | 설명 |
|---|---|---|---|
| 1 | **Rebalance에 의한 Pause 상태 유실** | 🔴 Critical | 새 파티션 할당 시 pause 상태가 리셋되어 Blue/Green 양쪽 모두 소비 가능 |
| 2 | **Thread-Safety 위반** | 🔴 Critical | 외부 HTTP 호출로 직접 pause/resume 시 ConcurrentModificationException 발생 |
| 3 | **Pause 전파 지연** | 🟡 High | poll loop 주기에 따라 pause 명령 반영에 수 ms~수 초 지연 발생 가능 |
| 4 | **In-flight 메시지 처리** | 🟡 High | pause 시점에 이미 fetch된 메시지는 여전히 처리 중일 수 있어 완벽한 Atomic Switch 불가 |
| 5 | **같은 Consumer Group 사용 시 파티션 경합** | 🟡 High | Blue/Green이 동일 group.id를 사용하면 양쪽에 파티션이 분배됨 |
| 6 | **Offset 커밋 타이밍** | 🟡 High | pause 직전 처리 완료된 메시지의 offset 커밋이 보장되지 않으면 중복/누락 발생 |

### 2.2 각 문제에 대한 대응 전략

**문제 1 대응 - Rebalance Listener 활용**:
```java
consumer.subscribe(topics, new ConsumerRebalanceListener() {
    @Override
    public void onPartitionsAssigned(Collection<TopicPartition> partitions) {
        if (shouldBePaused) {
            consumer.pause(partitions);  // 재할당 후에도 pause 유지
        }
    }
    @Override
    public void onPartitionsRevoked(Collection<TopicPartition> partitions) {
        consumer.commitSync();  // revoke 전 offset 확정
    }
});
```

**문제 2 대응 - 플래그 기반 간접 제어**:
```java
private final AtomicBoolean pauseRequested = new AtomicBoolean(false);

// Poll loop 내에서
while (running) {
    if (pauseRequested.compareAndSet(true, false)) {
        consumer.pause(consumer.assignment());
    }
    consumer.poll(Duration.ofMillis(100));
}

// HTTP 엔드포인트에서
@PostMapping("/pause")
public void pause() {
    pauseRequested.set(true);  // Thread-safe 플래그 설정
}
```

**문제 4 대응 - Graceful Drain**:
```
1. Blue에 pause 신호 전송
2. Blue가 현재 배치 처리 완료 대기 (drain timeout)
3. Blue의 offset commit 확인
4. Green에 resume 신호 전송
```

---

## 3. 전략별 비교 분석

### 3.1 네 가지 Blue/Green 전략 비교

```
┌──────────────────────────────────────────────────────────────────────────────────┐
│                   Kafka Consumer Blue/Green 전략 스펙트럼                          │
│                                                                                    │
│  간단 ◄──────────────────────────────────────────────────────────────────► 정교    │
│                                                                                    │
│  전략A          전략B            전략E             전략C            전략D           │
│  Recreate       Consumer Group   Kafka Connect     Pause/Resume     Zero-Lag       │
│  Deploy         분리 방식         REST API 방식     Atomic Switch    Offset Sync    │
│                                                                                    │
│  • 다운타임 有   • 라그 발생      • 프레임워크 해결  • 거의 무중단     • 완벽한 무중단│
│  • 가장 간단     • 구현 쉬움      • 앱 수정 불필요   • 앱 수정 필요    • 가장 복잡    │
│  • 롤백 느림     • 롤백 보통      • 롤백 빠름        • 롤백 빠름       • 롤백 즉시    │
│                                  • JVM 필요         │                               │
└──────────────────────────────────────────────────────────────────────────────────┘
```

| 항목 | 전략 A: Recreate | 전략 B: CG 분리 | 전략 E: Kafka Connect | 전략 C: Pause/Resume Atomic | 전략 D: Zero-Lag Offset Sync |
|---|---|---|---|---|---|
| **전환 속도** | 30초~수 분 | 10초~1분 | **2~5초** | **1~3초** | **<1초** |
| **롤백 속도** | 수 분 | 30초~1분 | **2~5초** | **1~3초** | **<1초** |
| **메시지 중복/누락** | 재시작 시 중복 가능 | 이중 소비 | drain 후 최소화 | 드레인 시 최소화 | Offset Sync로 제거 |
| **앱 수정 필요** | ❌ 없음 | ❌ 없음 | ❌ **없음** | ⚠️ Pause/Resume 엔드포인트 | ⚠️ 커스텀 컨트롤러 |
| **인프라 복잡도** | 낮음 | 중간 | **중간 (JVM Worker 필요)** | 중간 | 높음 |
| **Rebalance 영향** | 재시작마다 발생 | Green 시작 시 발생 | **프레임워크 내부 관리** | 미발생 (같은 인스턴스 유지) | 미발생 |
| **Thread-Safety** | 해당 없음 | 해당 없음 | ✅ **프레임워크 해결** | ⚠️ AtomicBoolean 필요 | ⚠️ 커스텀 구현 |
| **Pause 영구 저장** | 해당 없음 | 해당 없음 | ✅ **config topic 저장** | ❌ 인메모리 | ❌ 인메모리 |
| **다국어 지원** | 모든 언어 | 모든 언어 | ⚠️ **Connector는 JVM, 제어는 모든 언어** | ⚠️ 언어별 직접 구현 | ⚠️ 언어별 직접 구현 |
| **적합 시나리오** | 개발/스테이징 | 일반 프로덕션 | **데이터 파이프라인형 워크로드** | **빠른 전환 필요 프로덕션** | 미션 크리티컬 |

> **참조**: [Expedia Kafka Blue/Green](https://medium.com/expedia-group-tech/kafka-blue-green-deployment-212065b7fee7) / [Airwallex Kafka Streams B/G](https://medium.com/airwallex-engineering/kafka-streams-iterative-development-and-blue-green-deployment-fae88b26e75e) / [Confluent Kafka Connect](https://docs.confluent.io/platform/current/connect/index.html)

---

## 4. 전략 E: Kafka Connect REST API 기반 Blue/Green (신규 전략)

### 4.1 핵심 아이디어: 프레임워크가 문제를 해결한다

앞서 분석한 Pause/Resume 방식의 4가지 구조적 문제(Thread-Safety, Rebalance Pause 유실, 앱 침투적 설계, Argo Rollouts 한계)에 대해, **Kafka Connect는 3가지를 프레임워크 레벨에서 이미 해결**하고 있다.

**일반 Consumer vs Kafka Connect 문제 해결 비교**

| 문제 | 일반 Consumer | Kafka Connect |
|:---:|:---:|:---:|
| Thread-Safety | ❌ 수동 우회 필요 | ✅ REST→config topic |
| Rebalance Pause 유실 | ❌ RebalanceListener | ✅ config topic 영구저장 |
| 앱 코드 수정 | ❌ 프레임워크별 별도 | ✅ Connector 수정 불필요 |
| Argo Rollouts 연동 | ❌ 커스텀 Sidecar | ⚠️ REST API로 용이 |

#### 문제 1 해결: Thread-Safety → REST API + Config Topic 비동기 전파

일반 Consumer에서는 `KafkaConsumer`가 단일 스레드 전용이라 외부 HTTP 호출 시 `ConcurrentModificationException`이 발생한다. Kafka Connect는 **REST API 호출이 config topic(`connect-configs`)에 기록**되고, 각 Worker의 백그라운드 스레드가 이를 비동기로 소비하여 해당 Task를 안전하게 pause/resume한다.

```bash
# 어떤 언어, 어떤 환경에서든 동일하게 동작
curl -X PUT http://connect-worker:8083/connectors/my-sink/pause
curl -X PUT http://connect-worker:8083/connectors/my-sink/resume
curl -X GET http://connect-worker:8083/connectors/my-sink/status
```

> **참조**: [Confluent - Monitoring Connectors](https://docs.confluent.io/platform/current/connect/monitoring.html) / [Kafka Connect REST API 101](https://developer.confluent.io/courses/kafka-connect/rest-api/)

#### 문제 2 해결: Rebalance Pause 유실 → Config Topic에 영구 저장

일반 Consumer의 `pause()`는 인메모리 상태이므로 rebalance 시 유실된다. Kafka Connect의 pause 상태는 **config topic에 영구 저장(persistent)**되어, Worker 재시작이나 rebalance 후에도 자동 복원된다.

> *"The pause state is persistent, so even if you restart the cluster, the connector will not begin message processing again until the task has been resumed."* — Confluent 공식 문서

> **참조**: [KIP-875: First-class Offsets Support](https://cwiki.apache.org/confluence/display/KAFKA/KIP-875:+First-class+offsets+support+in+Kafka+Connect)

#### 문제 3 해결: 앱 침투적 설계 → Connector 코드 수정 불필요

일반 Consumer에서는 각 프레임워크(Spring, Micronaut, Node.js 등)마다 `/lifecycle/pause` 엔드포인트와 플래그 로직을 구현해야 한다. Kafka Connect에서는 **Connector/Task 코드에 아무런 수정 없이** 표준 REST API로 어떤 Connector든 동일하게 제어 가능하다.

#### 문제 4 부분 해결: Argo Rollouts 연동

Kafka Connect 자체가 Argo Rollouts 한계를 해결하지는 않지만, REST API가 있으므로 `prePromotionAnalysis`에서 호출하는 Job 작성이 훨씬 간단해진다.

### 4.2 아키텍처 개요

```
                    ┌──────────────────────────────────┐
                    │      Switch Orchestrator          │
                    │   (K8s Job / CronJob / Operator)  │
                    └──────────────┬───────────────────┘
                                   │ REST API 호출
                    ┌──────────────┼──────────────────┐
                    ▼                                   ▼
        ┌───────────────────────┐        ┌───────────────────────┐
        │  Connect Cluster BLUE │        │ Connect Cluster GREEN  │
        │  (Worker Pool)        │        │ (Worker Pool)          │
        │                       │        │                        │
        │  ┌─────────────────┐  │        │  ┌─────────────────┐   │
        │  │ my-sink-blue    │  │        │  │ my-sink-green   │   │
        │  │ State: RUNNING  │  │ Kafka  │  │ State: PAUSED   │   │
        │  │ Group: connect- │◄─┤ Topic  ├─►│ Group: connect- │   │
        │  │  my-sink-blue   │  │        │  │  my-sink-green  │   │
        │  └─────────────────┘  │        │  └─────────────────┘   │
        │                       │        │                        │
        │  config topic에       │        │  config topic에        │
        │  RUNNING 상태 저장    │        │  PAUSED 상태 저장      │
        └───────────────────────┘        └───────────────────────┘
```

### 4.3 두 가지 운영 모드

#### 모드 A: 단일 Connect Cluster + Connector 이름 분리

같은 Connect Cluster에서 Blue/Green Connector를 별도 이름으로 운영한다.

```bash
# Blue Connector 생성 (RUNNING)
curl -X POST http://connect:8083/connectors -H "Content-Type: application/json" -d '{
  "name": "my-sink-blue",
  "config": {
    "connector.class": "io.confluent.connect.jdbc.JdbcSinkConnector",
    "topics": "orders",
    "connection.url": "jdbc:postgresql://db:5432/orders",
    "tasks.max": "3",
    "consumer.override.group.id": "connect-my-sink-blue"
  }
}'

# Green Connector 생성 (STOPPED 상태로 생성 - KIP-980, Kafka 3.5+)
curl -X POST http://connect:8083/connectors -H "Content-Type: application/json" -d '{
  "name": "my-sink-green",
  "config": {
    "connector.class": "io.confluent.connect.jdbc.JdbcSinkConnector",
    "topics": "orders",
    "connection.url": "jdbc:postgresql://db:5432/orders",
    "tasks.max": "3",
    "consumer.override.group.id": "connect-my-sink-green"
  },
  "initial_state": "STOPPED"
}'
```

> **참조**: [KIP-980: Allow Creating Connectors in a Stopped State](https://cwiki.apache.org/confluence/display/KAFKA/KIP-980:+Allow+creating+connectors+in+a+stopped+state)

#### 모드 B: 별도 Connect Cluster (물리적 분리)

Blue/Green을 완전히 별도의 Connect Cluster로 운영하여 장애 격리를 강화한다.

```yaml
# Blue Connect Cluster (Strimzi 예시)
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaConnect
metadata:
  name: connect-blue
spec:
  replicas: 3
  bootstrapServers: kafka-cluster:9092
  config:
    group.id: connect-cluster-blue
    config.storage.topic: connect-configs-blue
    offset.storage.topic: connect-offsets-blue
    status.storage.topic: connect-status-blue
---
# Green Connect Cluster
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaConnect
metadata:
  name: connect-green
spec:
  replicas: 3
  bootstrapServers: kafka-cluster:9092
  config:
    group.id: connect-cluster-green
    config.storage.topic: connect-configs-green
    offset.storage.topic: connect-offsets-green
    status.storage.topic: connect-status-green
```

### 4.4 전환 시퀀스 (Switch Sequence)

```
시간 ──────────────────────────────────────────────────────────────►

[Blue Connector: RUNNING, Green Connector: PAUSED/STOPPED]

  T0: Switch Orchestrator 트리거 (수동 또는 CI/CD)
      │
  T1: Green Connector 설정 업데이트 (새 버전 config 적용)
      │   curl -X PUT .../connectors/my-sink-green/config -d '{새 설정}'
      │
  T2: Blue Connector PAUSE 요청
      │   curl -X PUT .../connectors/my-sink-blue/pause
      │   (비동기 - Task들이 현재 배치 처리 후 PAUSED 전이)
      │
  T3: Blue PAUSED 상태 확인 (폴링)
      │   while status != "PAUSED": 
      │     curl -X GET .../connectors/my-sink-blue/status
      │     sleep 0.5
      │
  T4: (선택) Offset 동기화
      │   Blue의 consumer group offset을 Green에 복제
      │   kafka-consumer-groups.sh --reset-offsets ...
      │
  T5: Green Connector RESUME 요청
      │   curl -X PUT .../connectors/my-sink-green/resume
      │
  T6: Green RUNNING 상태 확인
      │   전환 완료. 총 소요시간: 2~5초
      │
[Blue Connector: PAUSED, Green Connector: RUNNING]

  롤백 필요 시:
  ─────────────
  T7: Green PAUSE → Blue RESUME (동일 절차, 방향만 반대)
      총 롤백 시간: 2~5초
```

### 4.5 Offset 동기화 전략

Blue와 Green이 별도 Consumer Group을 사용하므로, 전환 시 offset 동기화가 필요하다.

```bash
#!/bin/bash
# switch-connector.sh - Kafka Connect Blue/Green 전환 스크립트

CONNECT_URL="http://connect-worker:8083"
BLUE_CONNECTOR="my-sink-blue"
GREEN_CONNECTOR="my-sink-green"
BLUE_GROUP="connect-my-sink-blue"
GREEN_GROUP="connect-my-sink-green"
KAFKA_BOOTSTRAP="kafka-cluster:9092"
TOPICS="orders"

echo "=== Step 1: Pause Blue Connector ==="
curl -s -X PUT "$CONNECT_URL/connectors/$BLUE_CONNECTOR/pause"

echo "=== Step 2: Wait for Blue PAUSED ==="
while true; do
  STATE=$(curl -s "$CONNECT_URL/connectors/$BLUE_CONNECTOR/status" | jq -r '.connector.state')
  echo "Blue state: $STATE"
  [ "$STATE" = "PAUSED" ] && break
  sleep 0.5
done

echo "=== Step 3: Get Blue's current offsets ==="
kafka-consumer-groups.sh --bootstrap-server $KAFKA_BOOTSTRAP \
  --group $BLUE_GROUP --describe --offsets 2>/dev/null > /tmp/blue-offsets.txt

echo "=== Step 4: Reset Green's offsets to match Blue ==="
# Green이 STOPPED 상태일 때만 offset reset 가능 (KIP-875)
curl -s -X PUT "$CONNECT_URL/connectors/$GREEN_CONNECTOR/stop"
sleep 2

# Kafka Connect 3.6+ REST API로 offset 조작
curl -s -X PATCH "$CONNECT_URL/connectors/$GREEN_CONNECTOR/offsets" \
  -H "Content-Type: application/json" \
  -d '{"offsets": [
    {"partition": {"kafka_topic": "orders", "kafka_partition": 0}, "offset": {"kafka_offset": 12345}},
    {"partition": {"kafka_topic": "orders", "kafka_partition": 1}, "offset": {"kafka_offset": 67890}}
  ]}'

echo "=== Step 5: Resume Green Connector ==="
curl -s -X PUT "$CONNECT_URL/connectors/$GREEN_CONNECTOR/resume"

echo "=== Step 6: Verify Green RUNNING ==="
while true; do
  STATE=$(curl -s "$CONNECT_URL/connectors/$GREEN_CONNECTOR/status" | jq -r '.connector.state')
  echo "Green state: $STATE"
  [ "$STATE" = "RUNNING" ] && break
  sleep 0.5
done

echo "=== Switch Complete ==="
```

> **참조**: [KIP-875: Offset Alter/Reset](https://cwiki.apache.org/confluence/display/KAFKA/KIP-875:+First-class+offsets+support+in+Kafka+Connect)

### 4.6 Strimzi Operator와의 통합 (Kubernetes Native)

Strimzi는 Kafka 3.5+부터 Connector의 STOPPED 상태를 CRD로 관리할 수 있다.

```yaml
# Blue Connector - Running
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaConnector
metadata:
  name: my-sink-blue
  labels:
    strimzi.io/cluster: connect-blue
spec:
  class: io.confluent.connect.jdbc.JdbcSinkConnector
  tasksMax: 3
  state: running          # ← Strimzi가 REST API 호출을 대행
  config:
    topics: orders
    connection.url: "jdbc:postgresql://db:5432/orders"
---
# Green Connector - Stopped
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaConnector
metadata:
  name: my-sink-green
  labels:
    strimzi.io/cluster: connect-green
spec:
  class: io.confluent.connect.jdbc.JdbcSinkConnector
  tasksMax: 3
  state: stopped           # ← 대기 상태
  config:
    topics: orders
    connection.url: "jdbc:postgresql://db:5432/orders"
```

전환 시 **`state` 필드만 변경**하면 Strimzi Operator가 자동으로 REST API를 호출한다:

```bash
# kubectl patch로 Blue/Green 전환
kubectl patch kafkaconnector my-sink-blue --type merge -p '{"spec":{"state":"stopped"}}'
kubectl patch kafkaconnector my-sink-green --type merge -p '{"spec":{"state":"running"}}'
```

> **참조**: [Strimzi Proposal #054 - Stopping Connectors](https://github.com/strimzi/proposals/blob/main/054-stopping-kafka-connect-connectors.md) / [Strimzi Issue #8713](https://github.com/strimzi/strimzi-kafka-operator/issues/8713)

### 4.7 주의사항: Strimzi REST API 직접 호출 vs CRD 제어 충돌

Strimzi 환경에서는 **REST API를 직접 호출하면 Strimzi Operator가 상태를 덮어쓸 수 있다**. Strimzi Issue #3277에서 보고된 바와 같이:

```
1. 사용자가 REST API로 pause 호출 → Connector PAUSED
2. Strimzi Operator가 주기적으로 CRD와 실제 상태를 reconcile
3. CRD에는 여전히 "running"으로 되어 있으므로 → 자동으로 RUNNING 복원
```

따라서 **Strimzi 환경에서는 반드시 CRD의 `spec.state`를 통해 제어**해야 한다.

> **참조**: [Strimzi Issue #3277 - REST API vs CRD Conflict](https://github.com/strimzi/strimzi-kafka-operator/issues/3277)

### 4.8 전략 E의 적합/부적합 시나리오

**적합한 경우:**
- Kafka → DB, Kafka → Elasticsearch, Kafka → S3 등 **데이터 파이프라인형 워크로드**
- 이미 Kafka Connect로 운영 중인 Sink/Source Connector
- 다양한 언어의 팀이 **통일된 운영 인터페이스**를 원하는 경우
- Strimzi 등 **Kubernetes Operator를 이미 사용** 중인 경우

**부적합한 경우:**
- Consumer 내부에 **복잡한 비즈니스 로직**(외부 API 호출, 복잡한 변환, 상태 관리)이 필요한 경우
- **JVM 의존성을 추가할 수 없는** 환경
- 기존 Connector 플러그인이 없는 커스텀 sink 대상

---

## 5. 다국어 Kafka Consumer Pause/Resume 지원 현황

### 5.1 Kafka Connect를 사용할 수 없을 때: 언어별 네이티브 구현

Kafka Connect가 적합하지 않아 직접 Consumer를 구현해야 하는 경우, 각 언어의 Kafka 클라이언트가 `pause()`/`resume()` API를 지원하는지가 Blue/Green 전략의 실현 가능성을 결정한다.

### 5.2 언어별 Kafka 클라이언트 및 pause/resume 지원

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                    Kafka Client 생태계 계층 구조                                  │
│                                                                                   │
│  ┌──────────────────────────────────────────────────────────────────────┐         │
│  │                    Apache Kafka Java Client (표준)                    │         │
│  │                    • 완전한 프로토콜 구현                             │         │
│  │                    • pause/resume ✅ (단일 스레드 제약)               │         │
│  └──────────────────────────────────────────────────────────────────────┘         │
│          │                              │                                         │
│          ▼                              ▼                                         │
│  ┌────────────────────┐    ┌───────────────────────────────────┐                 │
│  │ Spring Kafka        │    │  librdkafka (C/C++)               │                 │
│  │ container.pause()   │    │  • 대부분 non-JVM 언어의 기반     │                 │
│  │ ✅ 추상화 우수      │    │  • pause/resume ✅                │                 │
│  └────────────────────┘    │  • 백그라운드 스레드로 부분 안전   │                 │
│                             └───────────┬───────────────────────┘                 │
│                          ┌──────────────┼──────────────────┐                      │
│                          ▼              ▼                   ▼                      │
│              ┌──────────────┐ ┌──────────────┐ ┌──────────────────┐               │
│              │confluent-    │ │confluent-    │ │confluent-kafka-  │               │
│              │kafka-python  │ │kafka-go      │ │dotnet            │               │
│              │ ✅ pause     │ │ ✅ pause     │ │ ✅ pause         │               │
│              └──────────────┘ └──────────────┘ └──────────────────┘               │
│                                                                                   │
│  ┌─────────────── 네이티브 구현 (librdkafka 비의존) ───────────────────┐          │
│  │                                                                      │          │
│  │  kafka-python    KafkaJS        segmentio/     twmb/franz-go         │          │
│  │  (Pure Python)   (Pure JS)      kafka-go       (Pure Go)             │          │
│  │  ✅ pause        ✅ pause       ❌ 미지원       ✅ 부분지원          │          │
│  │  ⚠️ 버그 보고    ✅ 안정적                     ✅ goroutine-safe    │          │
│  └──────────────────────────────────────────────────────────────────────┘          │
└─────────────────────────────────────────────────────────────────────────────────┘
```

### 5.3 상세 비교표

| 언어 | 라이브러리 | pause/resume | Thread-Safety | Rebalance 후 pause 유지 | Blue/Green 적합도 | 비고 |
|---|---|---|---|---|---|---|
| **Java** | Apache Kafka Client | ✅ | ❌ 단일 스레드 | ❌ 수동 복구 필요 | ⭐⭐⭐⭐ | 표준 구현 |
| **Java** | Spring Kafka | ✅ `container.pause()` | ✅ 내부 관리 | ⚠️ [Issue #2222](https://github.com/spring-projects/spring-kafka/issues/2222) | ⭐⭐⭐⭐⭐ | 가장 추상화 우수 |
| **Python** | confluent-kafka-python | ✅ | ⚠️ librdkafka 부분 안전 | ❌ [Issue #371](https://github.com/confluentinc/confluent-kafka-python/issues/371) deadlock 보고 | ⭐⭐⭐ | librdkafka 래퍼 |
| **Python** | kafka-python | ✅ | ❌ 단일 스레드 | ❌ [Issue #2011](https://github.com/dpkp/kafka-python/issues/2011) offset 점프 버그 | ⭐⭐ | Pure Python, 유지보수 느림 |
| **Go** | confluent-kafka-go | ✅ | ⚠️ librdkafka 기반 | ❌ [Issue #193](https://github.com/confluentinc/confluent-kafka-go/issues/193) | ⭐⭐⭐ | CGO 의존성 |
| **Go** | segmentio/kafka-go | ❌ **미지원** | - | - | ⭐ | pause API 없음 |
| **Go** | twmb/franz-go | ✅ 부분 | ✅ goroutine-safe | ⚠️ 직접 구현 필요 | ⭐⭐⭐⭐ | 가장 현대적인 Go 클라이언트 |
| **Node.js** | KafkaJS | ✅ | ✅ 이벤트루프 단일스레드 | ⚠️ 직접 구현 필요 | ⭐⭐⭐⭐ | Node 특성상 thread-safety 자연 해결 |
| **Node.js** | node-rdkafka | ✅ | ⚠️ librdkafka 기반 | ❌ | ⭐⭐⭐ | KafkaJS보다 복잡 |
| **C#/.NET** | confluent-kafka-dotnet | ✅ | ⚠️ librdkafka 기반 | ❌ | ⭐⭐⭐ | [Shawarma](https://github.com/CenterEdge/shawarma)가 .NET 기반으로 검증 |
| **Rust** | rust-rdkafka | ✅ | ⚠️ librdkafka 기반 | ❌ | ⭐⭐⭐ | Rust 타입시스템으로 안전성 보강 |

> **참조**: [Apache Kafka Clients Wiki](https://cwiki.apache.org/confluence/display/KAFKA/Clients) / [Kafka Client Library Comparison](https://www.lydtechconsulting.com/blog/kafka-client-apache-kafka-vs-kafkajs)

### 5.4 핵심 관찰: librdkafka 기반 클라이언트의 공통 한계

Python, Go, C#, Rust 등 non-JVM 언어의 주요 클라이언트는 대부분 **librdkafka(C/C++)를 래핑**한다. 이들은 모두 `pause()`/`resume()`를 지원하지만, **Kafka Connect가 프레임워크 레벨에서 해결해 주는 3가지 문제는 여전히 수동 구현이 필요**하다:

1. **Rebalance 시 pause 유실** → 모든 언어에서 `on_assign` 콜백에서 수동 re-pause 로직 필요
2. **영구 저장 없음** → pause 상태가 인메모리. 프로세스 재시작 시 외부 저장소에서 복구 필요
3. **앱 침투적** → 각 언어/프레임워크마다 HTTP 엔드포인트 + 플래그 로직을 직접 구현

### 5.5 비-JVM 언어를 위한 권장 경로

```
                    ┌─────────────────────────────────┐
                    │  Kafka Connect Sink/Source로     │
                    │  해결 가능한 워크로드인가?        │
                    └──────────┬──────────────────────┘
                               │
                    ┌──────────┴──────────┐
                   Yes                    No
                    │                      │
        ┌───────────┴───────────┐    ┌────┴─────────────────┐
        │ 전략 E: Kafka Connect │    │ 커스텀 Consumer 필요   │
        │ (JVM) + REST API 제어 │    │ (비즈니스 로직 내장)   │
        │                       │    └────┬─────────────────┘
        │ 어떤 언어에서든       │         │
        │ curl/HTTP로 pause/    │    ┌────┴──────────────────────┐
        │ resume 가능           │    │ 언어별 최적 경로           │
        │                       │    │                            │
        │ ✅ 가장 권장          │    │ Java  → Spring Kafka       │
        └───────────────────────┘    │         container.pause()  │
                                     │                            │
                                     │ Go    → twmb/franz-go      │
                                     │         goroutine-safe     │
                                     │                            │
                                     │ Node  → KafkaJS            │
                                     │         이벤트루프 안전     │
                                     │                            │
                                     │ Python→ confluent-kafka-py │
                                     │         + AtomicBoolean 패턴│
                                     │                            │
                                     │ C#    → Shawarma Sidecar   │
                                     │         패턴 참고          │
                                     │                            │
                                     │ 공통: Sidecar 패턴 적용    │
                                     │ (전략 C 참조)              │
                                     └────────────────────────────┘
```

### 5.6 Kafka Connect 동등 프레임워크 부재

Kafka Connect의 핵심 가치(관리형 lifecycle, persistent pause, REST API, config topic 기반 분산 조정)를 동등하게 제공하는 **non-JVM 프레임워크는 현재 존재하지 않는다**.

| 프로젝트 | 언어 | 상태 | Kafka Connect 대비 |
|---|---|---|---|
| [amient/goconnect](https://github.com/amient/goconnect) | Go | ⚠️ 실험적, 비활성 | at-least-once 보장만. pause/resume lifecycle 없음 |
| [networknt/kafka-sidecar](https://github.com/networknt/kafka-sidecar) | Java (Sidecar) | 활성 | HTTP↔Kafka 브릿지. lifecycle 관리 아님 |
| Confluent REST Proxy | Java (서비스) | 프로덕션 가능 | produce/consume만. pause/resume lifecycle 없음 |

Confluent 공식 튜토리얼에서도 이 점을 명시한다: 직접 consumer를 만들면 결국 장애 처리, 재시작, 스케일링, 직렬화 등을 모두 구현하게 되며, 이는 **Kafka Connect를 처음부터 다시 만드는 것**과 동일하다고 설명한다.

> **참조**: [Kafka Connect Tutorial - Why Not Write Your Own](https://developer.confluent.io/courses/kafka-connect/intro/) / [Confluent Kafka Go Client](https://github.com/confluentinc/confluent-kafka-go) / [goconnect](https://github.com/amient/goconnect)

---

## 6. 전략 C: Pause/Resume Atomic Switch 상세 설계 (권장안)

### 6.1 아키텍처 개요

```
                        ┌─────────────────────────────┐
                        │     Switch Controller       │
                        │    (K8s Custom Controller    │
                        │     또는 Operator)           │
                        └──────────┬──────────────────┘
                                   │
                        ┌──────────┴──────────┐
                        │ ConfigMap/CRD 감시   │
                        │ "active: blue|green" │
                        └──────────┬──────────┘
                                   │
                    ┌──────────────┼──────────────┐
                    ▼                              ▼
        ┌───────────────────┐          ┌───────────────────┐
        │  Blue Deployment  │          │  Green Deployment │
        │  ┌─────────────┐  │          │  ┌─────────────┐  │
        │  │ Consumer App │  │          │  │ Consumer App │  │
        │  │ (ACTIVE)     │◄─┤ Kafka    ├─►│ (PAUSED)    │  │
        │  │ resume 상태   │  │ Topic    │  │ pause 상태   │  │
        │  └──────┬──────┘  │          │  └──────┬──────┘  │
        │  ┌──────┴──────┐  │          │  ┌──────┴──────┐  │
        │  │  Sidecar     │  │          │  │  Sidecar     │  │
        │  │  (Shawarma형)│  │          │  │  (Shawarma형)│  │
        │  └─────────────┘  │          │  └─────────────┘  │
        └───────────────────┘          └───────────────────┘
                    │                              │
                    └──────────┬───────────────────┘
                               │
                    ┌──────────┴──────────┐
                    │  Same Consumer Group │
                    │  (group.id 공유)      │
                    │  + Static Membership │
                    └─────────────────────┘
```

### 6.2 핵심 설계 결정

#### 결정 1: 같은 Consumer Group + Static Membership

Blue와 Green이 **같은 `group.id`를 사용**하되, `group.instance.id`(Static Membership, KIP-345)를 활용하여 rebalance를 최소화한다.

```yaml
# Blue Deployment - StatefulSet 사용
env:
  - name: KAFKA_GROUP_ID
    value: "my-consumer-group"
  - name: KAFKA_GROUP_INSTANCE_ID
    valueFrom:
      fieldRef:
        fieldPath: metadata.name  # e.g., consumer-blue-0, consumer-blue-1
```

**왜 같은 Consumer Group인가?**
- 별도 Consumer Group을 사용하면 전환 시 offset 동기화 문제가 발생
- 같은 Group + pause/resume으로 파티션 할당을 유지하면서 처리만 중단/재개

> **참조**: [KIP-345 Static Membership](https://cwiki.apache.org/confluence/display/KAFKA/KIP-345:+Introduce+static+membership+protocol+to+reduce+consumer+rebalances) / [Confluent - Consumer Group IDs](https://www.confluent.io/blog/configuring-apache-kafka-consumer-group-ids/)

#### 결정 2: Sidecar 패턴으로 관심사 분리

Shawarma의 접근법을 차용하여, Consumer 앱은 **단순한 HTTP 엔드포인트만 노출**하고, 인프라 레벨 판단은 Sidecar가 담당한다.

```
Consumer App의 책임:
  - POST /lifecycle/pause  → AtomicBoolean 플래그 설정 → poll loop에서 pause 실행
  - POST /lifecycle/resume → AtomicBoolean 플래그 설정 → poll loop에서 resume 실행
  - GET  /lifecycle/status → 현재 상태 반환 (ACTIVE/PAUSED/DRAINING)

Sidecar의 책임:
  - K8s ConfigMap/CRD 변경 감시
  - Consumer App에 HTTP POST로 상태 변경 통지
  - Consumer 상태 헬스체크 및 보고
```

#### 결정 3: Cooperative Sticky Assignor + Rebalance 방어

```properties
# Consumer 설정
partition.assignment.strategy=org.apache.kafka.clients.consumer.CooperativeStickyAssignor
session.timeout.ms=45000
heartbeat.interval.ms=15000
max.poll.interval.ms=300000
```

Rebalance 발생 시 pause 상태를 복구하는 방어 로직:

```java
@Override
public void onPartitionsAssigned(Collection<TopicPartition> partitions) {
    log.info("Partitions assigned: {}", partitions);
    if (lifecycleState == LifecycleState.PAUSED) {
        // Rebalance 후에도 pause 상태 유지
        consumer.pause(partitions);
        log.info("Re-paused assigned partitions due to PAUSED lifecycle state");
    }
}
```

> **참조**: [Confluent - Cooperative Rebalancing](https://www.confluent.io/blog/cooperative-rebalancing-in-kafka-streams-consumer-ksqldb/) / [Kafka 4.0 NGCRP](https://www.instaclustr.com/blog/rebalance-your-apache-kafka-partitions-with-the-next-generation-consumer-rebalance-protocol/)

### 6.3 전환 시퀀스 (Switch Sequence)

```
시간 ──────────────────────────────────────────────────────────────►

[Blue: ACTIVE, Green: PAUSED]

  T0: 운영자가 ConfigMap 업데이트 (active: green)
      │
  T1: Sidecar가 변경 감지
      │
  T2: Blue Consumer에 POST /lifecycle/pause 전송
      │   Blue: 현재 poll 배치 처리 완료 (drain)
      │   Blue: offset commit (commitSync)
      │   Blue: consumer.pause(assignment)
      │   Blue: 상태 → PAUSED 응답
      │
  T3: Sidecar가 Blue PAUSED 확인 (GET /lifecycle/status)
      │
  T4: Green Consumer에 POST /lifecycle/resume 전송
      │   Green: consumer.resume(assignment)
      │   Green: 상태 → ACTIVE 응답
      │
  T5: 전환 완료. 총 소요시간: 1~3초

[Blue: PAUSED, Green: ACTIVE]
```

#### 롤백 시퀀스 (동일 메커니즘, 방향만 반대)

```
  T0: 운영자가 ConfigMap 업데이트 (active: blue)
  T1~T5: Green pause → Blue resume (동일 절차)
  총 롤백 시간: 1~3초
```

### 6.4 Kubernetes 매니페스트

#### Switch Controller CRD

```yaml
apiVersion: kafka.example.com/v1alpha1
kind: KafkaConsumerSwitch
metadata:
  name: order-consumer-switch
  namespace: production
spec:
  consumerGroupId: order-processing-group
  activeColor: blue  # blue 또는 green
  blueDeployment:
    name: order-consumer-blue
    replicas: 3
  greenDeployment:
    name: order-consumer-green
    replicas: 3
  switchPolicy:
    drainTimeoutSeconds: 10
    healthCheckIntervalMs: 500
    rollbackOnFailure: true
status:
  currentActive: blue
  lastSwitchTime: "2026-02-17T10:30:00Z"
  blueStatus: ACTIVE
  greenStatus: PAUSED
```

#### Blue Deployment (StatefulSet)

```yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: order-consumer-blue
  labels:
    app: order-consumer
    color: blue
spec:
  replicas: 3
  serviceName: order-consumer-blue
  selector:
    matchLabels:
      app: order-consumer
      color: blue
  template:
    metadata:
      labels:
        app: order-consumer
        color: blue
      annotations:
        kafka-switch.example.com/managed: "true"
    spec:
      containers:
        # Main Consumer Container
        - name: consumer
          image: myregistry/order-consumer:v2.1.0
          ports:
            - containerPort: 8080  # lifecycle 엔드포인트
          env:
            - name: KAFKA_BOOTSTRAP_SERVERS
              value: "kafka-cluster:9092"
            - name: KAFKA_GROUP_ID
              value: "order-processing-group"
            - name: KAFKA_GROUP_INSTANCE_ID
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
            - name: KAFKA_TOPICS
              value: "orders,order-updates"
            - name: INITIAL_STATE
              value: "ACTIVE"  # Blue 초기 상태
          readinessProbe:
            httpGet:
              path: /lifecycle/status
              port: 8080
            initialDelaySeconds: 10
            periodSeconds: 5
          livenessProbe:
            httpGet:
              path: /health
              port: 8080
            initialDelaySeconds: 15
            periodSeconds: 10
          resources:
            requests:
              cpu: 500m
              memory: 512Mi
            limits:
              cpu: 1000m
              memory: 1Gi

        # Switch Sidecar Container
        - name: switch-sidecar
          image: myregistry/kafka-switch-sidecar:v1.0.0
          env:
            - name: CONSUMER_LIFECYCLE_URL
              value: "http://localhost:8080/lifecycle"
            - name: SWITCH_CRD_NAME
              value: "order-consumer-switch"
            - name: MY_COLOR
              value: "blue"
            - name: MY_POD_NAME
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
            - name: MY_NAMESPACE
              valueFrom:
                fieldRef:
                  fieldPath: metadata.namespace
          resources:
            requests:
              cpu: 50m
              memory: 64Mi
            limits:
              cpu: 100m
              memory: 128Mi
```

#### Green Deployment (거의 동일, 차이점만 표시)

```yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: order-consumer-green
  labels:
    app: order-consumer
    color: green
spec:
  replicas: 3
  # ... (Blue와 동일 구조)
  template:
    spec:
      containers:
        - name: consumer
          image: myregistry/order-consumer:v2.2.0  # 새 버전
          env:
            # ... (동일)
            - name: INITIAL_STATE
              value: "PAUSED"  # Green 초기 상태 (대기)
        - name: switch-sidecar
          env:
            - name: MY_COLOR
              value: "green"  # 색상만 다름
```

### 6.5 Consumer App 구현 가이드 (Spring Kafka 예시)

```java
@RestController
@RequestMapping("/lifecycle")
public class ConsumerLifecycleController {

    private final KafkaListenerEndpointRegistry registry;
    private final AtomicReference<LifecycleState> state;

    @PostMapping("/pause")
    public ResponseEntity<Map<String, String>> pause() {
        state.set(LifecycleState.DRAINING);
        
        // 1. 현재 처리 중인 메시지 완료 대기
        awaitCurrentBatchCompletion();
        
        // 2. 모든 리스너 컨테이너 pause
        registry.getAllListenerContainers().forEach(container -> {
            if (container.isRunning()) {
                container.pause();
            }
        });
        
        state.set(LifecycleState.PAUSED);
        return ResponseEntity.ok(Map.of("status", "PAUSED"));
    }

    @PostMapping("/resume")
    public ResponseEntity<Map<String, String>> resume() {
        registry.getAllListenerContainers().forEach(container -> {
            if (container.isContainerPaused()) {
                container.resume();
            }
        });
        
        state.set(LifecycleState.ACTIVE);
        return ResponseEntity.ok(Map.of("status", "ACTIVE"));
    }

    @GetMapping("/status")
    public ResponseEntity<Map<String, Object>> status() {
        return ResponseEntity.ok(Map.of(
            "state", state.get().name(),
            "containers", getContainerStatuses()
        ));
    }
}
```

> **참조**: [Spring Kafka Pause/Resume 블로그](https://medium.com/@akhil.bojedla/start-stop-pause-and-resume-spring-kafka-consumer-at-runtime-45b44b9be44b) / [DZone - Stop & Resume Kafka](https://dzone.com/articles/ways-to-stop-amp-resume-your-kafka-producerconsume)

---

## 7. 전략 C의 잔존 리스크 및 완화 방안

### 7.1 리스크 매트릭스

```
  영향도
  높음 │  ①               ④
       │
  중간 │      ②      ③
       │
  낮음 │                     ⑤
       └──────────────────────
        낮음    중간    높음
                발생 확률

  ① Rebalance 시 Pause 유실 → RebalanceListener로 완화
  ② In-flight 메시지 중복 → Drain + Idempotent 처리
  ③ Sidecar 장애 → Liveness Probe + 기본값 유지
  ④ 양쪽 동시 Active → Distributed Lock으로 방지
  ⑤ Offset Gap → commitSync 강제 + 모니터링
```

### 7.2 양쪽 동시 Active 방지 (가장 중요한 안전장치)

Switch 과정에서 네트워크 지연이나 장애로 인해 Blue와 Green이 동시에 ACTIVE가 되는 상황을 방지해야 한다.

```yaml
# Distributed Lock을 활용한 안전장치
apiVersion: coordination.k8s.io/v1
kind: Lease
metadata:
  name: order-consumer-active-lease
  namespace: production
spec:
  holderIdentity: "blue"  # 현재 active인 색상
  leaseDurationSeconds: 30
  acquireTime: "2026-02-17T10:30:00Z"
  renewTime: "2026-02-17T10:30:25Z"
```

Switch Controller는 반드시 **"Pause First, Resume Second"** 원칙을 따른다:

```
1. Blue PAUSE 요청 → 응답 확인
2. Blue PAUSED 상태 검증 (GET /lifecycle/status)
3. Lease holder를 "green"으로 변경
4. Green RESUME 요청
5. Green ACTIVE 상태 검증
```

**만약 2단계에서 실패하면**: Blue는 ACTIVE를 유지하고, 전환을 중단한다.

### 7.3 모니터링 및 알림

```yaml
# Prometheus Alerting Rules
groups:
  - name: kafka-consumer-switch
    rules:
      # 양쪽 모두 ACTIVE 감지 (가장 Critical)
      - alert: DualActiveConsumers
        expr: |
          count(kafka_consumer_lifecycle_state{state="ACTIVE"} == 1) 
          BY (consumer_group) > 1
        for: 5s
        labels:
          severity: critical
        annotations:
          summary: "Blue와 Green 모두 ACTIVE 상태 - 즉시 조치 필요"

      # 전환 후 Consumer Lag 급증 감지
      - alert: PostSwitchLagSpike
        expr: |
          increase(kafka_consumer_lag_sum[1m]) > 10000
          and on(consumer_group) 
          changes(kafka_consumer_active_color[5m]) > 0
        for: 30s
        labels:
          severity: warning

      # 양쪽 모두 PAUSED (메시지 처리 중단)
      - alert: AllConsumersPaused
        expr: |
          count(kafka_consumer_lifecycle_state{state="ACTIVE"} == 1)
          BY (consumer_group) == 0
        for: 10s
        labels:
          severity: critical
```

---

## 8. 대안 설계: Argo Rollouts + PrePromotionAnalysis 연동

Pause/Resume Atomic Switch를 Argo Rollouts의 Blue/Green 전략과 결합할 수도 있다.

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata:
  name: order-consumer
spec:
  replicas: 3
  strategy:
    blueGreen:
      activeService: order-consumer-active
      previewService: order-consumer-preview
      autoPromotionEnabled: false
      prePromotionAnalysis:
        templates:
          - templateName: pause-blue-consumers
        args:
          - name: active-deployment
            value: "order-consumer-active"
      scaleDownDelaySeconds: 600  # 10분간 Blue 유지 (롤백 대비)
```

이 방식에서 `prePromotionAnalysis`가 Blue Consumer의 pause와 drain을 트리거하고, 완료 후 Green으로 promotion이 진행된다. 단, Argo Rollouts가 Kafka 파티션 할당을 직접 제어하지는 못하므로, **별도 Consumer Group 사용이 필요**하다.

> **참조**: [Argo Rollouts Blue/Green](https://argo-rollouts.readthedocs.io/en/stable/features/bluegreen/) / [Argo Rollouts Traffic Management](https://argo-rollouts.readthedocs.io/en/stable/features/traffic-management/)

---

## 9. 결론 및 권장사항

### 9.1 Pause/Resume Atomic Switch는 유효한 전략인가?

**Yes, 조건부로 매우 유효하다.** 

이 방식이 일반화된 도구로 존재하지 않는 이유는 기술적 결함 때문이 아니라:

1. **Kafka Consumer의 Thread-Safety 제약**으로 프레임워크별 구현이 필요
2. **Rebalance 시 pause 유실** 문제에 대한 방어 로직이 필수
3. HTTP 트래픽과 달리 **pull 기반 워크로드의 제어는 인프라+앱 양쪽 수정**이 필요
4. 이미 Shawarma와 같은 **소규모 프로젝트에서 검증**되었으나, 대형 에코시스템에 편입되지 못함

### 9.2 권장 적용 순서

```
=== 전략 E 경로 (Kafka Connect 워크로드) ===

Phase 1: Kafka Connect 환경 구축
         ├─ Strimzi Operator 또는 Confluent Platform 설치
         ├─ Blue/Green Connect Cluster 또는 Connector 쌍 생성
         └─ Green Connector를 STOPPED 상태로 배포

Phase 2: Switch Orchestrator 개발
         ├─ REST API 기반 전환 스크립트 (bash/Python/Go)
         ├─ Offset 동기화 로직 (KIP-875)
         └─ 상태 확인 폴링 + 타임아웃 처리

Phase 3: CI/CD 연동
         ├─ Argo Rollouts prePromotionAnalysis 또는 Tekton Task
         ├─ Strimzi CRD를 통한 GitOps 전환
         └─ Prometheus 모니터링 + 자동 롤백

=== 전략 C 경로 (커스텀 Consumer 워크로드) ===

Phase 1: Consumer App에 /lifecycle 엔드포인트 추가
         ├─ pause/resume/status HTTP API
         └─ RebalanceListener에서 pause 상태 복구

Phase 2: Switch Sidecar 개발 (Shawarma 참고)
         ├─ ConfigMap/CRD 변경 감시
         └─ Consumer App에 HTTP POST 전송

Phase 3: Switch Controller 또는 CRD Operator 개발
         ├─ "Pause First, Resume Second" 오케스트레이션
         ├─ K8s Lease 기반 양쪽 동시 Active 방지
         └─ Prometheus 메트릭 연동

Phase 4: 운영 자동화
         ├─ Argo Rollouts prePromotionAnalysis 연동
         ├─ Grafana 대시보드
         └─ 롤백 자동화 (Lag 급증 시)
```

### 9.3 최종 판단

| 상황 | 권장 전략 |
|---|---|
| 전환 속도가 크게 중요하지 않은 일반 서비스 | 전략 B (Consumer Group 분리) |
| **Kafka Connect로 구현 가능한 데이터 파이프라인 워크로드** | **전략 E (Kafka Connect REST API) ✅ 신규 권장** |
| **빠른 전환/롤백이 필요하고, 앱 수정이 가능한 경우** | **전략 C (Pause/Resume Atomic Switch) ✅ 권장** |
| 메시지 중복/누락이 절대 불가한 금융/결제 시스템 | 전략 D (Zero-Lag Offset Sync + 커스텀 컨트롤러) |

### 9.4 전략 E와 전략 C의 선택 기준

```
워크로드 유형 판단:

  "Kafka에서 읽어서 DB/ES/S3에 쓰는 파이프라인인가?"
    → Yes: 전략 E (Kafka Connect) 우선 검토
    → No: "Consumer 내부에 복잡한 비즈니스 로직이 있는가?"
            → Yes: 전략 C (Pause/Resume Atomic Switch)
            → No: "JVM 의존성 추가가 가능한가?"
                    → Yes: 전략 E 또는 C
                    → No: 전략 C + 해당 언어의 Sidecar 패턴
```

Pause/Resume Atomic Switch는 **1~3초 내 전환과 롤백**을 달성할 수 있는 현실적인 최선의 방법이며, Shawarma 프로젝트가 이미 프로덕션에서 이 패턴의 기본 원리를 검증하고 있다. Kafka Consumer에 특화된 구현체를 만들면 범용 도구로서의 가치가 충분하다.

한편, **Kafka Connect로 구현 가능한 워크로드라면 전략 E가 가장 실용적인 선택**이다. Thread-Safety, Rebalance Pause 유실, 앱 침투성이라는 3대 문제를 프레임워크가 이미 해결하고 있으며, REST API를 통해 **어떤 언어에서든 동일한 운영 인터페이스**를 제공한다. Strimzi Operator와 결합하면 `kubectl patch`만으로 Blue/Green 전환이 가능하여 운영 부담이 크게 줄어든다.

---

## 참조 링크 종합

| 분류 | 제목 | URL |
|---|---|---|
| **Kafka Connect** | Confluent - Kafka Connect 개요 | https://docs.confluent.io/platform/current/connect/index.html |
| **Kafka Connect** | Kafka Connect REST API 101 | https://developer.confluent.io/courses/kafka-connect/rest-api/ |
| **Kafka Connect** | Confluent - Monitoring Connectors (Pause/Resume) | https://docs.confluent.io/platform/current/connect/monitoring.html |
| **Kafka Connect** | KIP-875: First-class Offsets Support | https://cwiki.apache.org/confluence/display/KAFKA/KIP-875:+First-class+offsets+support+in+Kafka+Connect |
| **Kafka Connect** | KIP-980: Creating Connectors in Stopped State | https://cwiki.apache.org/confluence/display/KAFKA/KIP-980:+Allow+creating+connectors+in+a+stopped+state |
| **Kafka Connect** | Strimzi Proposal #054 - Stopping Connectors | https://github.com/strimzi/proposals/blob/main/054-stopping-kafka-connect-connectors.md |
| **Kafka Connect** | Strimzi Issue #3277 - REST API vs CRD 충돌 | https://github.com/strimzi/strimzi-kafka-operator/issues/3277 |
| **Kafka Connect** | Strimzi Issue #8713 - STOPPED 상태 지원 | https://github.com/strimzi/strimzi-kafka-operator/issues/8713 |
| **Kafka Connect** | Kafka Connect Improvements in 2.3 (Incremental Rebalancing) | https://www.confluent.io/blog/kafka-connect-improvements-in-apache-kafka-2-3/ |
| **Kafka Connect** | Sink Connector 개발 가이드 | https://docs.confluent.io/platform/current/connect/devguide.html |
| **Kafka Connect** | Why Not Write Your Own Integrations | https://developer.confluent.io/courses/kafka-connect/intro/ |
| **다국어** | Apache Kafka Clients Wiki | https://cwiki.apache.org/confluence/display/KAFKA/Clients |
| **다국어** | Kafka Client Library Comparison (Java vs KafkaJS) | https://www.lydtechconsulting.com/blog/kafka-client-apache-kafka-vs-kafkajs |
| **다국어** | confluent-kafka-go (librdkafka Go 바인딩) | https://github.com/confluentinc/confluent-kafka-go |
| **다국어** | confluent-kafka-python Issue #371 - Pause/Resume Deadlock | https://github.com/confluentinc/confluent-kafka-python/issues/371 |
| **다국어** | kafka-python Issue #2011 - Resume Offset Jump | https://github.com/dpkp/kafka-python/issues/2011 |
| **다국어** | KafkaJS Pause/Resume Issue #808 | https://github.com/tulios/kafkajs/issues/808 |
| **다국어** | go-kafka/connect - Go CLI for Connect REST API | https://github.com/go-kafka/connect |
| **다국어** | ricardo-ch/go-kafka-connect - Go 동기식 배포 | https://github.com/ricardo-ch/go-kafka-connect |
| **다국어** | amient/goconnect - Go Connect 프레임워크 시도 | https://github.com/amient/goconnect |
| 도구 | Shawarma - K8s Blue/Green Sidecar | https://github.com/CenterEdge/shawarma |
| 도구 | Shawarma Webhook (MutatingAdmission) | https://github.com/CenterEdge/shawarma-webhook |
| 도구 | Argo Rollouts Blue/Green | https://argo-rollouts.readthedocs.io/en/stable/features/bluegreen/ |
| 도구 | Argo Rollouts Concepts | https://argo-rollouts.readthedocs.io/en/stable/concepts/ |
| 사례 | Expedia - Kafka Blue/Green Deployment | https://medium.com/expedia-group-tech/kafka-blue-green-deployment-212065b7fee7 |
| 사례 | Airwallex - Kafka Streams Blue/Green | https://medium.com/airwallex-engineering/kafka-streams-iterative-development-and-blue-green-deployment-fae88b26e75e |
| 사례 | Feature Flag + Kafka Pause/Resume | https://www.improving.com/thoughts/unleashing-feature-flags-onto-kafka-consumers/ |
| 사례 | Lyft Blackhole Sink Pattern | https://www.streamingdata.tech/p/blackhole-sink-pattern-for-blue-green |
| 사례 | Cloudflare - Kafka Consumer Health | https://blog.cloudflare.com/intelligent-automatic-restarts-for-unhealthy-kafka-consumers/ |
| 기술 | KIP-345 Static Membership | https://cwiki.apache.org/confluence/display/KAFKA/KIP-345 |
| 기술 | Confluent - Cooperative Rebalancing | https://www.confluent.io/blog/cooperative-rebalancing-in-kafka-streams-consumer-ksqldb/ |
| 기술 | Kafka 4.0 Next Gen Rebalance Protocol | https://www.instaclustr.com/blog/rebalance-your-apache-kafka-partitions-with-the-next-generation-consumer-rebalance-protocol/ |
| 기술 | Red Hat - Kafka Pause/Resume | https://developers.redhat.com/articles/2023/12/01/how-avoid-rebalances-and-disconnections-kafka-consumers |
| 기술 | Spring Kafka Pause/Resume | https://medium.com/@akhil.bojedla/start-stop-pause-and-resume-spring-kafka-consumer-at-runtime-45b44b9be44b |
| 이슈 | Spring Kafka #2222 - Rebalance Pause 유실 | https://github.com/spring-projects/spring-kafka/issues/2222 |
| 이슈 | Argo Rollouts #3539 - Kafka Consumer Scale | https://github.com/argoproj/argo-rollouts/issues/3539 |
| 이슈 | Confluent Kafka Go #193 - Pause After Rebalance | https://github.com/confluentinc/confluent-kafka-go/issues/193 |
| 이슈 | KAFKA-13291 - Stateful Blue/Green | https://issues.apache.org/jira/browse/KAFKA-13291 |
| 특허 | Blue/Green Deployment Strategy for Kafka | https://www.tdcommons.org/dpubs_series/6318/ |
