# Kafka 4.x 리밸런싱 개선 리서치

> **작성일:** 2026-02-28
> **배경:** Phase 2에서 Argo Rollouts(Deployment 기반) 전환 시 Static Membership 사용 불가 문제 (decisions.md D1, D2 참조)

---

## 핵심 문제

- Deployment 기반 Pod → 이름 랜덤 → `group.instance.id` 불가 → Static Membership 사용 불가
- Static Membership 없이 Classic Protocol → **모든 Pod 재시작마다 Stop-the-World 리밸런싱**

---

## KIP-848: New Consumer Group Protocol

### 개요

Kafka 4.0에서 GA된 새 Consumer Group Protocol. 리밸런싱의 성격을 근본적으로 변경한다.

**Classic Protocol (JoinGroup/SyncGroup):**
```
Consumer A ──┐
Consumer B ──┤── JoinGroup ──► Leader가 할당 계산 ──► SyncGroup ──► 전원 재개
Consumer C ──┘   (⛔ STOP-THE-WORLD: 전체 Consumer가 멈춤)
```

**New Protocol (ConsumerGroupHeartbeat):**
```
Consumer A ── Heartbeat ──► Coordinator: "변경 없음"  ──► 계속 소비 ✅
Consumer B ── Heartbeat ──► Coordinator: "P5 반납"    ──► P5만 반납, P3·P4 계속 소비 ✅
Consumer C ── Heartbeat ──► Coordinator: "P5 획득"    ──► P5 획득
                            (🔄 글로벌 배리어 없음, 영향받는 파티션만 이동)
```

### 핵심 변경 사항

1. **Server-Side Assignment**: 할당 로직이 Client(Consumer Leader)에서 Broker(Group Coordinator)로 이동
2. **Heartbeat 기반 점진적 조정**: JoinGroup/SyncGroup 대신 `ConsumerGroupHeartbeat` API로 점진적 할당 변경 수신
3. **글로벌 동기화 배리어 제거**: 할당 변경이 없는 Consumer는 **전혀 영향받지 않음**
4. **3-Phase Per-Member Reconciliation**:
   - Phase 1: Coordinator가 특정 파티션 revoke 지시 (Heartbeat 응답)
   - Phase 2: Consumer가 오프셋 커밋 후 revoke, 새 epoch로 전환
   - Phase 3: Coordinator가 가용 파티션을 대상 Consumer에 할당

### Classic Protocol vs KIP-848 비교

| 측면 | Classic (Static Membership 없이) | KIP-848 (Static Membership 없이) |
|------|----------------------------------|----------------------------------|
| Pod 재시작 시 리밸런싱 | **발생** (전체) | **발생** (하지만 점진적) |
| Stop-the-World? | **Yes** — 전체 Consumer 멈춤 | **No** — 할당 변경 없는 Consumer는 영향 없음 |
| 리밸런싱 소요시간 | ~103초 (10 consumers, 900 partitions) | **~5초** (동일 조건, **20배 빠름**) |
| 파티션 셔플링 | 전체 재계산 가능 | **델타만** (이동 필요한 파티션만) |
| 할당 로직 위치 | Client (Leader Consumer) | **Server (Group Coordinator)** |

### Argo Rollouts Blue-Green 시나리오에서의 동작

**Green Pod 시작 시 (새 Consumer 가입):**
1. Coordinator가 새 target assignment 계산 → 일부 파티션을 Green으로 이동 결정
2. **해당 파티션을 가진 Blue Pod만** revoke 요청 받음
3. **나머지 Blue Pod는 중단 없이 계속 소비**
4. Green Pod가 해당 파티션 획득

**Blue Pod 종료 시:**
1. Coordinator가 종료된 Pod의 파티션만 재분배
2. **다른 모든 Consumer는 영향 없이 계속 소비**

### 버전별 상태

| Kafka 버전 | KIP-848 상태 | 날짜 |
|------------|-------------|------|
| 3.7 | Early Access (테스트 전용) | 2024-02 |
| 3.8 | Preview | 2024-07 |
| 3.9 | Preview | 2024-10 |
| **4.0** | **GA (프로덕션 사용 가능)** | 2025-03 |
| 4.1 | GA + rack-aware 개선 (KIP-1078) | 2025-07 |
| 4.2 | GA + adaptive batching | 2026-02 |
| 5.0 (예정) | **기본 프로토콜**로 전환 예정 | TBD |

### 활성화 방법

Consumer 설정에 한 줄 추가:

```properties
group.protocol=consumer
```

활성화 시 **제거해야 하는 Classic Protocol 설정:**
- `partition.assignment.strategy` → 서버 사이드 `group.consumer.assignors`로 대체
- `session.timeout.ms` → 서버 사이드 `group.consumer.session.timeout.ms`로 대체
- `heartbeat.interval.ms` → 서버 사이드 `group.consumer.heartbeat.interval.ms`로 대체

Spring Kafka 설정:
```yaml
spring:
  kafka:
    consumer:
      properties:
        group.protocol: consumer
```

> **참고:** Spring for Apache Kafka 4.0.0-M2 (Spring Boot 4.0 대상)에서 KIP-848 네이티브 지원. 이전 버전에서는 raw property로 전달.

### Static Membership과의 관계

KIP-848에서도 Static Membership(`group.instance.id`)은 여전히 지원됨:
- `MemberEpoch == -2`: 임시 탈퇴 → session timeout 내 재가입 시 기존 할당 복원
- `MemberEpoch == -1`: 영구 탈퇴

**그러나 중요도가 크게 낮아짐:**

| | Static Membership 필요성 |
|---|---|
| Classic Protocol | **필수** — 없으면 Stop-the-World |
| **KIP-848** | **Nice-to-have** — 없어도 점진적 리밸런싱으로 영향 최소 |

---

## KIP-932: Share Groups (참고)

### 개요

파티션 할당 개념 자체를 제거하는 새로운 소비 모델.

- 여러 Consumer가 **동일 파티션의 레코드를 동시 처리** (큐 시맨틱스)
- **레코드 단위 acknowledgement** (오프셋 기반이 아님)
- 파티션-Consumer 할당 없음 → **리밸런싱 문제 원천 제거**
- Consumer 수 > 파티션 수 가능

### 버전별 상태

| Kafka 버전 | 상태 |
|------------|------|
| 4.0 | Early Access (프로덕션 사용 불가) |
| 4.1 | Preview |
| 4.2 | GA 목표 |

### 이 프로젝트에의 적용 가능성

**부적합** — 다음 이유로 Blue/Green 배포 패턴에 직접 적용하기 어려움:
- 파티션 내 순서 보장을 포기 (큐 시맨틱스)
- 독립적인 작업 처리(알림 전송, 태스크 처리)에 적합한 모델
- 순서 보장이 필요한 스트림 처리에는 부적합

---

## 기타 관련 KIP

| KIP | 버전 | 설명 | 관련도 |
|-----|------|------|--------|
| KIP-1071 | 4.1 EA | Streams Rebalance Protocol (Kafka Streams 전용) | 낮음 (plain consumer 사용) |
| KIP-1078 | 4.1 | KIP-848 rack-aware 할당 개선 | 중간 (multi-AZ 클러스터) |
| KIP-1082 | 4.0 | Client-Generated Member IDs | 낮음 (안정성 개선, 리밸런싱 무관) |

---

## 결론 및 Phase 2 영향

### KIP-848을 사용할 경우 decisions.md에 미치는 영향

| 결정 | 현재 (Classic Protocol) | KIP-848 적용 시 |
|------|------------------------|-----------------|
| D2: Static Membership 제거 | CooperativeStickyAssignor + PauseAwareRebalanceListener로 완화 | `group.protocol=consumer` 한 줄로 더 나은 결과 |
| D3: STOPPED 상태 | 그룹 탈퇴 시 Stop-the-World 우려 | 점진적 리밸런싱으로 우려 감소 |
| D4: prePromotionAnalysis | 전환 중 리밸런싱 지연 고려 필요 | ~5초 리밸런싱으로 전환 시퀀스 단순화 가능 |

### 전제 조건

- Kafka Broker: 4.0+ 필요 (4.1 권장, KIP-1078 rack-aware 개선 포함)
- Java Client (kafka-clients): 4.0+ (Java 11+ 필수)
- Spring Kafka: 3.3.x에서 `group.protocol=consumer` raw property 전달 가능, 4.0.0-M2+ 네이티브 지원
- Spring Boot: 3.4.x + kafka-clients 4.1.x override 권장
- librdkafka (Go/Python): 2.12+ (KIP-848 GA, Static Membership 지원)
- Strimzi Operator: 0.46+ (Kafka 4.0 지원), 0.50.1 (Kafka 4.1.1 지원)
- Kubernetes: 1.27+ (Strimzi 0.48+ 요구), 1.30+ (KEDA 2.17 호환) 권장

### 요약

**"리밸런싱을 완전히 피할 수는 없지만, KIP-848로 리밸런싱의 성격이 근본적으로 바뀐다."**

- Stop-the-World → 점진적 (영향받지 않는 Consumer는 계속 소비)
- ~100초 → ~5초
- Static Membership: 필수 → Nice-to-have

---

## 참고 자료

- [KIP-848 Confluent Blog](https://www.confluent.io/blog/kip-848-consumer-rebalance-protocol/)
- [KIP-848 Apache Wiki](https://cwiki.apache.org/confluence/display/KAFKA/KIP-848:+The+Next+Generation+of+the+Consumer+Rebalance+Protocol)
- [Consumer Rebalance Protocol Operations Guide (Kafka 4.1)](https://kafka.apache.org/41/operations/consumer-rebalance-protocol/)
- [Kafka Consumer Group Rebalance - Next-Gen Protocol (Lydtech)](https://www.lydtechconsulting.com/blog/blog-kafka-rebalance-next-gen)
- [Rebalance Partitions 20x Faster (Instaclustr)](https://www.instaclustr.com/blog/rebalance-your-apache-kafka-partitions-with-the-next-generation-consumer-rebalance-protocol/)
- [KIP-932 Apache Wiki](https://cwiki.apache.org/confluence/display/KAFKA/KIP-932:+Queues+for+Kafka)
- [Apache Kafka 4.0 Release](https://www.confluent.io/blog/latest-apache-kafka-release/)
- [Apache Kafka 4.2 Release](https://kafka.apache.org/blog/2026/02/17/apache-kafka-4.2.0-release-announcement/)
