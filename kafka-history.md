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
| **1.1** | 2018.03 | **Delegation Token** 인증 도입 |
| **2.0** | 2018.07 | **Prefix ACL (KIP-290)** — 패턴 기반 접근 제어 |
| **2.4** | 2019.12 | **KIP-429 (Cooperative Rebalancing)**, **KIP-345 (Static Membership)**, KIP-392 (Fetch From Follower), **MirrorMaker 2 (KIP-382)** |
| **2.8** | 2021.04 | **KRaft Early Access (KIP-500)** — ZooKeeper 없이 동작하는 첫 버전 |
| **3.0** | 2021.09 | KRaft Preview, `acks=all` 및 `enable.idempotence=true` 기본값 |
| **3.3** | 2022.09 | **Source Connector Exactly-Once (KIP-618)** |
| **3.3.1** | 2022.10 | **KRaft Production Ready** (신규 클러스터 대상) |
| **3.5** | 2023.05 | **ZooKeeper 모드 Deprecated**, Bridge Release 시작 |
| **3.6** | 2023.10 | **Tiered Storage Early Access (KIP-405)**, ZK→KRaft 마이그레이션 Production Ready |
| **3.7** | 2024.03 | **KIP-848 (차세대 Consumer Rebalance) Early Access** |
| **3.9** | 2024.11 | 최종 Bridge Release, **Tiered Storage Production Ready** |
| **4.0** | 2025.03 | **ZooKeeper 완전 제거**, KIP-848 GA, **Share Groups EA (KIP-932)**, Java 17 필수(브로커), MirrorMaker 1 삭제, log4j2 강제 전환 |
| **4.1** | 2025.09 | Share Groups Preview, **Streams 서버 측 리밸런싱 EA (KIP-1071)**, **네이티브 JWT 인증 (KIP-1139)** |
| **4.2** | ~2025말 | **Share Groups GA**, Streams 리밸런싱 GA, Streams 기본 DLQ, **Transaction V2 (KIP-1228)** |

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

### KRaft 내부 아키텍처

KRaft는 Kafka 내부에 통합된 Raft 기반 합의 프로토콜로, 3가지 핵심 요소로 구성된다:

**Quorum Controller (쿼럼 컨트롤러)**
- 클러스터 내 특정 브로커들이 컨트롤러로 지정되어 Raft 쿼럼을 형성
- 투표를 통해 한 대의 **활성 컨트롤러(Active Controller)**가 선출되며, 이 리더만 메타데이터 쓰기 권한 보유
- 나머지 컨트롤러는 핫 스탠바이(팔로워)로 동작, 리더의 변경사항을 실시간 추적
- 리더 장애 시 팔로워가 이미 최신 메타데이터를 메모리에 보유하므로 **수 밀리초 내 즉각 페일오버**

**MetadataLog (`__cluster_metadata`)**
- 클러스터 상태(토픽 생성, 파티션 리더 선출, 브로커 하트비트 등)를 **단일 파티션 내부 토픽**에 이벤트 스트림으로 저장
- 활성 컨트롤러가 로그에 이벤트를 추가하면, 쿼럼 과반수가 로컬 저장 완료 시 커밋 인정
- 모든 브로커가 이 토픽을 구독하여 이벤트 로그를 순서대로 재생(Replay)함으로써 동일한 인메모리 상태를 결정론적으로 구축

**Snapshot (스냅샷) 메커니즘**
- 이벤트 로그 무한 증가 방지를 위해 쿼럼 컨트롤러가 주기적으로 전체 상태 요약 스냅샷을 생성하고 이전 로그를 절삭(Abridge)
- 신규 브로커나 일시 중지 후 복귀한 노드는 최신 스냅샷 로드 후 이후 변경분만 읽으면 되므로 클러스터 복구/동기화 시간 극적 단축

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

## 4. Exactly-Once Semantics 진화

### KIP-98: 멱등 프로듀서 + 트랜잭션 (Kafka 0.11, 2017)

스트림 처리(Read-Process-Write) 과정에서 브로커/네트워크 장애 시 메시지 중복 처리나 유실을 방지하기 위해 도입되었다.

**멱등성 프로듀서 (Idempotent Producer)**
- 각 프로듀서에 고유 ID(PID)와 시퀀스 번호를 부여
- 재시도 시 브로커가 중복 메시지를 식별하고 무시

**트랜잭션 API**
- 트랜잭션 코디네이터가 **2단계 커밋(2PC)** 프로토콜을 관리
- 여러 파티션에 대한 쓰기와 컨슈머 오프셋 커밋을 **원자적(Atomic)**으로 묶어 처리
- 내부적으로 `__transaction_state` 토픽에 커밋/어보트 마커를 기록하여 트랜잭션 완료 여부 판단

### Hanging Transaction 문제와 KIP-890

네트워크 단절 등으로 트랜잭션 컨트롤 레코드(커밋/어보트 마커)가 누락되면, 트랜잭션이 영원히 완료되지 않은 상태(Hanging)로 남게 된다. 이 경우:
- **LSO(Last Stable Offset)**가 증가하지 않아 `read_committed` 컨슈머의 읽기가 멈춤
- 로그 컴팩션도 중단되어 **디스크 고갈**로 이어질 수 있음

**Transaction Server-Side Defense (KIP-890)** — 3단계에 걸쳐 해결:

| 단계 | 메커니즘 | 효과 |
|------|----------|------|
| **Epoch Bumping** | 트랜잭션 커밋/어보트 시마다 프로듀서 에포크 증가 | 지연 도착한 이전 세션 메시지가 새 트랜잭션에 섞이는 것 차단 |
| **Implicit Partition Addition** | 클라이언트의 명시적 `AddPartitionsToTxn` 제거, 첫 Produce 요청 시 서버가 암시적으로 파티션 추가 | 버그/지연으로 인한 오류 방지 |
| **Strict Epoch Validation** (4.2, Transaction V2) | `producer_epoch == current_epoch + 1` 엄격 검증 | 좀비 마커가 새 트랜잭션을 잘못 커밋하는 레이스 컨디션 방지 |

---

## 5. Tiered Storage (KIP-405)

### 도입 배경

기존 Kafka는 컴퓨팅과 스토리지가 결합되어 있어, 데이터 보존 기간을 늘리려면 컴퓨팅 리소스가 충분하더라도 브로커/디스크를 추가 증설해야 했다. 또한 과거 데이터를 읽는 Cold Read 작업 시 디스크 I/O가 포화되어 실시간 처리에 영향을 주는 문제("Death by a Thousand Reads")가 있었다.

### 아키텍처

스토리지를 **로컬(Local)**과 **원격(Remote)** 두 계층으로 분리:

- **Local Tier (Hot Data)**: 최근 로그 세그먼트는 브로커 로컬 디스크에 유지, 실시간 컨슈머에게 낮은 지연 시간 제공
- **Remote Tier (Cold Data)**: 닫힌(closed) 로그 세그먼트를 비동기적으로 외부 객체 스토리지로 업로드
- **Remote Log Manager(RLM)**: 브로커 내부에서 `RLMCopyTask`가 주기적으로(예: 30초) 실행, 업로드 조건을 만족하는 세그먼트와 인덱스 파일을 원격 스토리지로 복사
- 원격 로그 메타데이터는 `__remote_log_metadata` 내부 토픽에 저장하여 내결함성 유지

### 지원 스토리지 및 버전 이력

- 지원: **AWS S3, Google Cloud Storage(GCS), Azure Blob Storage, HDFS** 등
- 3.6.0 (2023.10): Early Access
- **3.9.0 (2024.11): Production Ready (GA)**

### 제약사항

- 압축된(Compacted) 토픽은 지원하지 않음
- 2.8.0 이전에 생성되어 프로듀서 스냅샷 파일이 없는 로그 세그먼트는 미지원
- 클러스터 수준 비활성화 시 Tiered Storage가 활성화된 모든 토픽을 먼저 삭제해야 함

### 실제 효과

- 객체 스토리지 활용으로 스토리지 비용 **약 10배 절감**
- 사실상 무제한의 데이터 보존 가능
- 과거 데이터 읽기 부하가 외부 스토리지로 분산되어 실시간 프로듀스 지연 시간(p99) **최대 30% 개선**

---

## 6. MirrorMaker 2 (KIP-382)

### 도입 배경

클러스터 간 **재해 복구(DR)**, 글로벌 데이터 지리적 복제, 데이터 격리 및 중앙 집계를 위해 Kafka 2.4.0 (2019)에서 도입되었다.

### MM1 vs MM2

| 항목 | MirrorMaker 1 | MirrorMaker 2 |
|------|---------------|---------------|
| 아키텍처 | 단순 프로듀서-컨슈머 쌍 | **Kafka Connect 프레임워크** 기반 |
| 설정 | 정적 | 동적, 확장 가능 |
| 오프셋 동기화 | 불가 | **자동 변환 및 동기화** |
| Active-Active | 미지원 | **기본 지원** |
| 현재 상태 | 4.0에서 삭제 | 현행 표준 |

### 3개의 핵심 커넥터

1. **MirrorSourceConnector**: 데이터, 토픽 설정, ACL을 대상 클러스터로 복제
2. **MirrorCheckpointConnector**: 소스-대상 클러스터 간 컨슈머 그룹 오프셋을 변환 및 동기화 → 끊김 없는 마이그레이션
3. **MirrorHeartbeatConnector**: 클러스터 간 연결 상태와 복제 지연을 모니터링

### Active-Active 구성

- 양방향 복제 토폴로지(ClusterA ⇄ ClusterB)를 기본 지원
- 무한 루프 복제 방지를 위해 대상 토픽 이름 앞에 소스 클러스터 이름을 접두사로 부착 (예: `source.topic-name`)

---

## 7. Security 진화

Kafka는 초기 보안 기능이 없는 형태에서, 엔터프라이즈 멀티 테넌시를 지원하는 강력한 인증/인가 프레임워크로 진화했다.

### SASL 인증 방식

| 방식 | 도입 | 특징 | 적합 환경 |
|------|------|------|-----------|
| **Kerberos (GSSAPI)** | 0.9 (2015) | 외부 Kerberos 서버 연동, Principal/Keytab 사용 | 대규모 조직의 레거시 AD 환경, 장기 실행 프로세스 |
| **PLAIN** | 0.9 (2015) | 단순 ID/비밀번호 기반, **반드시 TLS와 함께 사용** 필요 | 개발/테스트 환경 |
| **SCRAM (SHA-256/512)** | 0.10.2 (2017) | Salted Challenge Response, ZK/KRaft에 안전 저장, **브로커 재시작 없이 동적 갱신** | 프로덕션 (Kerberos 없는 환경) |
| **OAUTHBEARER** | 2.0 (2018) | OAuth 2 프레임워크 기반 | 클라우드/MSA 환경 |
| **네이티브 JWT** | 4.1 (2025) | KIP-1139, 암호학적 서명 JWT 기본 지원, 평문 시크릿 불필요 | 최신 프로덕션 |

### Delegation Token (위임 토큰, 1.1~)

- 클라이언트-브로커 간 공유 비밀 키(Secret)로 생성되는 가벼운 단기 인증 방식
- Kerberos Keytab이나 TLS 인증서를 수많은 워커 노드에 배포하는 관리 오버헤드 감소
- 유효 기간을 짧게 설정하고 빠르게 폐기/갱신 가능 → 보안 침해 시 영향(Blast radius) 최소화

### ACL (Access Control Lists)

- **2.0.0 (KIP-290)**: **Prefix 기반 ACL** 도입 — 'foo'로 시작하는 모든 토픽에 한 번에 권한 부여 가능
- 대규모 보안 클러스터에서 접근 제어 관리 대폭 간소화

---

## 8. Kafka Connect, Streams & Schema Registry

### Kafka Connect
- **0.9 (2015)**: 프레임워크 도입 — Source/Sink Connector 패턴
- **3.3 (2022)**: KIP-618로 **Source Connector Exactly-Once** 지원
- 수백 개 커넥터 생태계 (Debezium CDC, JDBC, S3, Elasticsearch 등)

### Kafka Streams
- **0.10 (2016)**: Java 스트림 처리 라이브러리 도입
- **0.11 (2017)**: KIP-129로 Exactly-Once Semantics 지원
- Kafka 내장 라이브러리로 별도 클러스터 불필요 (vs Flink, Spark)
- 로컬 상태 저장소로 **RocksDB**를 기본 사용

#### RocksDB 튜닝 (컨테이너 환경)

Kafka Streams의 RocksDB는 JVM 힙 외부에 메모리를 할당하므로, K8s 환경에서 특별한 튜닝이 필요하다:

| 항목 | 방법 | 효과 |
|------|------|------|
| **오프힙 메모리 제한** | `RocksDBConfigSetter` 구현으로 명시적 제한 | 컨테이너 OOMKilled 방지 (가장 중요) |
| **전역 캐시 공유** | `LRUCache`로 모든 RocksDB 인스턴스가 블록 캐시/쓰기 버퍼 공유 | 메모리 경합/과다 할당 방지 |
| **메모리 할당자** | Linux에서 glibc 대신 **jemalloc** 사용 | 메모리 단편화 방지 |
| **쓰기 지연 해결** | 백그라운드 압축 스레드 수 증가, MemTable 크기/최대 개수 증가 | 쓰기 버스트 흡수 |
| **압축 스타일** | 기본 Universal → Level Compaction으로 변경 가능 | 디스크 공간 절약 (쓰기 속도 약간 희생) |

### Confluent Schema Registry
- Confluent Platform 일부로 별도 제공 (Apache Kafka 외부)
- 초기: Avro만 지원
- **CP 5.5 (2020)**: **Protobuf, JSON Schema** 추가 지원
- 호환성 모드: BACKWARD, FORWARD, FULL, NONE

---

## 9. Kafka on Kubernetes 적응 과정

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

## 10. Share Groups (KIP-932)

Kafka에 **큐(Queue) 시맨틱스**를 도입하는 근본적 변화:

- 기존: 파티션 수 = Consumer 병렬도 상한 (파티션 1개 → Consumer 1개 고정 할당)
- Share Groups: **파티션보다 많은 Consumer**가 레코드를 협력적으로 공유 소비
- 파티션-Consumer 매핑 제약 해소
- 용도: 피크 부하 대응 스케일링, 느린 Consumer 보상

| 버전 | 단계 |
|------|------|
| 4.0 (2025.03) | Early Access (`unstable.api.versions.enable=true` 필요) |
| 4.1 (2025.09) | Preview |
| **4.2 (~2025말)** | **GA — 진정한 수평적 컨슈머 무한 확장** |

> **주의:** 4.0 EA 코드로 Share Group을 활성화한 클러스터는 호환성 제약으로 4.1로 업그레이드 불가.

---

## 11. 차세대 혁신: Virtual Clusters & ParKa

### Virtual Clusters — KIP-1134

단일 물리적 Kafka 클러스터 위에 여러 **논리적 클러스터**를 시뮬레이션하는 네이티브 멀티 테넌시 기능:

- 각 가상 클러스터는 자체적인 토픽/컨슈머 그룹 세트를 독립적으로 관리
- 물리 클러스터에서는 가상 클러스터별 고유 접두사(Prefix)를 붙여 리소스 충돌을 원천 차단
- 물리 클러스터 수를 줄이고 하드웨어 활용도를 극대화
- 현재 커뮤니티 논의 중, **Kafka 4.x 또는 5.0**에서 도입 목표

### ParKa — KIP-1008

"Parquet" + "Kafka"의 결합. **Apache Parquet를 Kafka 로그 세그먼트의 스토리지 포맷으로 사용**:

- 프로듀서 클라이언트에 Parquet를 새 인코더/압축기로 추가
- 배치 레코드(세그먼트) 단위로 Parquet 포맷으로 인코딩/압축 후 브로커 전송
- `columnar.encoding = parquet` 설정으로 활성화
- **데이터 레이크 연동**: `fetch.raw.bytes`로 Parquet 원시 바이트를 브로커에서 직접 가져와 데이터 레이크에 그대로 덤프 → 수집 리소스/시간 극적 절약
- 컬럼형 포맷 특성으로 우수한 압축률 + 추가 오버헤드 없는 컬럼 수준 데이터 암호화

---

## 12. 전체 타임라인 요약

```
2010  LinkedIn에서 Kafka 첫 커밋
2012  Apache Top-Level Project 졸업
2013  0.8 — Replication
2015  0.9 — Kafka Connect, SSL/SASL 보안
2016  0.10 — Kafka Streams
2017  0.11 — Exactly-Once │ 1.0 안정성 마일스톤 │ Strimzi 시작
2018  1.1 — Delegation Token │ 2.0 — Prefix ACL
2019  2.4 — Cooperative Rebalancing + Static Membership + MM2 │ KIP-500 제안 │ Strimzi CNCF 진입
2021  2.8 — KRaft EA │ 3.0 — KRaft Preview, acks=all 기본값
2022  3.3 — Source Connector EOS │ 3.3.1 — KRaft Production Ready
2023  3.5 — ZooKeeper Deprecated │ 3.6 — Tiered Storage EA
2024  3.7~3.9 — KIP-848 EA→Preview, 최종 Bridge Release, Tiered Storage GA
2025  4.0 — ZK 삭제, KIP-848 GA, Share Groups EA, MM1 삭제
      4.1 — Share Groups Preview, JWT 인증, Streams 서버측 리밸런싱 EA
      4.2 — Share Groups GA, Transaction V2, Streams DLQ
```

---

## 본 프로젝트와의 연관성

이 프로젝트(Kafka Consumer Blue/Green 배포 연구)에서 활용하는 Kafka 기능들의 도입 시점:

| 기능 | 도입 버전 | 프로젝트 활용 |
|------|-----------|---------------|
| Static Membership (KIP-345) | 2.4 (2019) | `group.instance.id = ${HOSTNAME}` — Pod 재시작 시 리밸런스 방지 |
| CooperativeStickyAssignor (KIP-429) | 2.4 (2019) | 전환 중 리밸런스 영향 최소화 |
| Consumer Pause/Resume API | 0.10.1 (2016) | Strategy C의 핵심 — Atomic Switch |
| Exactly-Once Semantics (KIP-98) | 0.11 (2017) | 멱등 프로듀서로 메시지 중복 방지 기반 |
| StatefulSet 패턴 | K8s 1.9+ (2017) | 안정적 Pod 이름 → Static Membership ID 유지 |
