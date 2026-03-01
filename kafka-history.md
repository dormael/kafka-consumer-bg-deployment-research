# Apache Kafka 생태계 역사와 Kubernetes 적응 과정

## 1. Kafka 탄생과 핵심 버전 변천사

### 기원 (2010-2012)

LinkedIn에서 **Jay Kreps, Neha Narkhede, Jun Rao**가 2010년 10월 첫 커밋. 대규모 로그 수집 파이프라인의 한계를 해결하기 위해 설계되었다. 2011년 7월 Apache Incubator에 진입, 2012년 10월 Top-Level Project로 졸업.

### 주요 버전별 변경 요약

| 버전 | 시기 | 핵심 변경 |
|------|------|-----------|
| **0.8** | 2013 | **Replication** 도입 — 장애 허용의 기반 마련 |
| **0.9** | 2015.11 | **Kafka Connect** 프레임워크, 새 Consumer API, SSL/SASL 보안 |
| **0.10** | 2016.05 | **Kafka Streams** 라이브러리, 메시지 타임스탬프 추가 |
| **0.11** | 2017.06 | **Exactly-Once Semantics (KIP-98)** — 멱등 프로듀서 + 트랜잭션 API |
| **1.0** | 2017.11 | 안정성 마일스톤, JBOD 지원 |
| **2.4** | 2019.12 | **KIP-429 (Cooperative Rebalancing)**, **KIP-345 (Static Membership)**, KIP-392 (Fetch From Follower) |
| **2.8** | 2021.04 | **KRaft Early Access (KIP-500)** — ZooKeeper 없이 동작하는 첫 버전 |
| **3.0** | 2021.09 | KRaft Preview, `acks=all` 및 `enable.idempotence=true` 기본값 |
| **3.3.1** | 2022.10 | **KRaft Production Ready** (신규 클러스터 대상) |
| **3.5** | 2023.05 | **ZooKeeper 모드 Deprecated**, Bridge Release 시작 |
| **3.6** | 2023.10 | **Tiered Storage Early Access (KIP-405)**, ZK→KRaft 마이그레이션 Production Ready |
| **3.7** | 2024.03 | **KIP-848 (차세대 Consumer Rebalance) Early Access** |
| **3.9** | 2024.11 | 최종 Bridge Release, Tiered Storage Production Ready |
| **4.0** | 2025.03 | **ZooKeeper 완전 제거**, KIP-848 GA, **Share Groups EA (KIP-932)**, Java 17 필수(브로커) |
| **4.1** | 2025.09 | Share Groups Preview |

---

## 2. ZooKeeper에서 KRaft로 (KIP-500)

Kafka 역사상 가장 큰 아키텍처 전환. ZooKeeper 의존성 제거까지 **6년**이 걸렸다.

| 시기 | 마일스톤 |
|------|----------|
| 2019.08 | KIP-500 제안 (Colin McCabe) |
| 2020.09 | Core Raft 구현 머지 (15,744 LOC 추가) |
| 2021.04 (2.8) | KRaft Early Access |
| 2022.10 (3.3.1) | KRaft **Production Ready** (신규 클러스터) |
| 2023.05 (3.5) | ZooKeeper Deprecated |
| 2023.10 (3.6) | ZK→KRaft 마이그레이션 Production Ready |
| 2024.11 (3.9) | 최종 Bridge Release |
| **2025.03 (4.0)** | **ZooKeeper 코드 완전 삭제** |

**KRaft의 핵심 이점:**
- ZooKeeper 클러스터 운영 부담 제거
- 메타데이터 관리 단일화 (Kafka 자체 Raft 합의)
- 컨트롤러 페일오버 시간 단축 (초 단위 → 밀리초 단위)
- K8s 환경에서 운영 복잡도 대폭 감소

---

## 3. Consumer Group Protocol 진화

Consumer 리밸런싱은 Kafka 운영 안정성의 핵심 이슈였다. 총 4세대에 걸쳐 진화했다.

### 1세대: Eager Rebalancing (원조)
- **Stop-the-World** 방식: 그룹 변경 시 모든 Consumer가 파티션 반납 후 재할당
- Consumer Leader(클라이언트)가 파티션 할당 계산
- 리밸런스 중 전체 소비 중단 → 운영 환경에서 심각한 문제

### 2세대: Static Membership — KIP-345 (Kafka 2.4, 2019)
- `group.instance.id` 설정으로 Consumer에 **영구 ID** 부여
- Pod 재시작 시 LeaveGroup을 보내지 않음 → `session.timeout.ms`로만 탈퇴 감지
- 재참여 시 이전 파티션 할당 그대로 복원 → **Rolling Update 시 불필요한 리밸런스 방지**
- 본 프로젝트에서 `group.instance.id = ${HOSTNAME}`으로 활용 중

### 3세대: Cooperative Rebalancing — KIP-429 (Kafka 2.4, 2019)
- **CooperativeStickyAssignor** 전략 도입
- 영향 없는 파티션은 유지, 이동이 필요한 파티션만 재할당
- 2단계 리밸런스: 1차에서 이동 대상 식별, 2차에서 재할당
- 리밸런스 중 소비 중단 시간 극적 감소
- 본 프로젝트에서 CooperativeStickyAssignor 사용 중

### 4세대: Server-Side Rebalancing — KIP-848 (Kafka 3.7~4.0)
- 파티션 할당 로직이 **클라이언트 → 서버(Group Coordinator)**로 이동
- 리밸런스 속도 최대 **20배 향상**
- Consumer가 리밸런스 중에도 처리 계속 가능
- Kafka 4.0에서 GA, **Kafka 5.0에서 기본 프로토콜**이 될 예정

---

## 4. Kafka on Kubernetes 적응 과정

### 초기 도전 과제 (2016-2018)

Kafka의 Stateful 특성이 K8s의 Cattle(가축) 모델과 충돌했다:

- **스토리지/캐시**: Pod 재스케줄링 시 OS Page Cache 무효화 → 성능 급락
- **네트워크**: Broker가 상호 교환 불가 — 클라이언트가 특정 Broker에 직접 연결 필요, 단순 LB 불가
- **Graceful Shutdown**: K8s의 SIGTERM/SIGKILL이 Kafka의 파티션 리더 이관을 기다리지 않음
- **ZooKeeper**: Kafka + ZK 두 개의 StatefulSet 운영으로 복잡도 2배

### Strimzi Operator 진화

| 시기 | 마일스톤 |
|------|----------|
| 2017 | 프로젝트 시작 |
| 2019.08 | CNCF Sandbox 진입 |
| 0.29.0 | KRaft 초기 지원 |
| 0.39.0 | K8s 1.21-1.22 지원 마지막 버전 |
| 0.43.0 | K8s 1.23-1.24 지원 마지막 버전 |
| 현재 | K8s 1.27+ 필요, KRaft GA, ZK 지원 제거 |

### Confluent for Kubernetes (CFK)

| 세대 | 상세 |
|------|------|
| Operator 1.x (Legacy) | Confluent Platform 6.0-6.1, 2022.04 지원 종료 |
| CFK 2.x (차세대) | 완전 재작성, K8s-native CRD |
| CFK 3.1.0 (현재) | CP 7.3-8.1, K8s 1.26-1.34 지원 |

### KRaft가 K8s에 미친 영향

KRaft 도입으로 K8s 배포가 극적으로 단순화되었다:
1. ZooKeeper StatefulSet 제거 (3~5개 Pod + PVC + 모니터링 불필요)
2. 네트워크 구성 단순화 (ZK용 Service/DNS 불필요)
3. Pod 시작 속도 향상 (ZK 의존성 대기 없음)
4. Operator 로직 단순화

---

## 5. Kafka Connect & Schema Registry

### Kafka Connect
- **0.9 (2015)**: 프레임워크 도입 — Source/Sink Connector 패턴
- **3.3 (2022)**: KIP-618로 **Source Connector Exactly-Once** 지원
- 수백 개 커넥터 생태계 (Debezium CDC, JDBC, S3, Elasticsearch 등)

### Kafka Streams
- **0.10 (2016)**: Java 스트림 처리 라이브러리 도입
- **0.11 (2017)**: KIP-129로 Exactly-Once Semantics 지원
- Kafka 내장 라이브러리로 별도 클러스터 불필요 (vs Flink, Spark)

### Confluent Schema Registry
- Confluent Platform 일부로 별도 제공 (Apache Kafka 외부)
- 초기: Avro만 지원
- **CP 5.5 (2020)**: **Protobuf, JSON Schema** 추가 지원
- 호환성 모드: BACKWARD, FORWARD, FULL, NONE

---

## 6. 최신 흐름: Share Groups (KIP-932)

Kafka에 **큐(Queue) 시맨틱스**를 도입하는 근본적 변화:

- 기존: 파티션 수 = Consumer 병렬도 상한 (파티션 1개 → Consumer 1개 고정 할당)
- Share Groups: **파티션보다 많은 Consumer**가 레코드를 협력적으로 공유 소비
- 파티션-Consumer 매핑 제약 해소
- 용도: 피크 부하 대응 스케일링, 느린 Consumer 보상

| 버전 | 단계 |
|------|------|
| 4.0 (2025.03) | Early Access (`unstable.api.versions.enable=true` 필요) |
| 4.1 (2025.09) | Preview |
| 4.2 (~2025말) | GA 목표 |

---

## 7. 전체 타임라인 요약

```
2010  LinkedIn에서 Kafka 첫 커밋
2012  Apache Top-Level Project 졸업
2013  0.8 — Replication
2015  0.9 — Kafka Connect, 보안
2016  0.10 — Kafka Streams
2017  0.11 — Exactly-Once │ 1.0 안정성 마일스톤 │ Strimzi 시작
2019  2.4 — Cooperative Rebalancing + Static Membership │ KIP-500 제안 │ Strimzi CNCF 진입
2021  2.8 — KRaft EA │ 3.0 — KRaft Preview, acks=all 기본값
2022  3.3.1 — KRaft Production Ready
2023  3.5 — ZooKeeper Deprecated │ 3.6 — Tiered Storage EA
2024  3.7~3.9 — KIP-848 EA→Preview, 최종 Bridge Release
2025  4.0 — ZooKeeper 삭제, KIP-848 GA, Share Groups EA
```

---

## 본 프로젝트와의 연관성

이 프로젝트(Kafka Consumer Blue/Green 배포 연구)에서 활용하는 Kafka 기능들의 도입 시점:

| 기능 | 도입 버전 | 프로젝트 활용 |
|------|-----------|---------------|
| Static Membership (KIP-345) | 2.4 (2019) | `group.instance.id = ${HOSTNAME}` — Pod 재시작 시 리밸런스 방지 |
| CooperativeStickyAssignor (KIP-429) | 2.4 (2019) | 전환 중 리밸런스 영향 최소화 |
| Consumer Pause/Resume API | 0.10.1 (2016) | Strategy C의 핵심 — Atomic Switch |
| StatefulSet 패턴 | K8s 1.9+ (2017) | 안정적 Pod 이름 → Static Membership ID 유지 |
