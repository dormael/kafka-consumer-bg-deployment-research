@kafka-consumer-bluegreen-design.md 의 내용을 테스트하고 지표, 로그를 수집해서 테스트 결과를 확인하기 위해 아래 순서대로 작업을 계획하고 그 내용을 @plan/plan.md에 저장한다.

## 검증 대상 전략

디자인 문서에서 제시한 전략들 중 아래 3가지를 실제 구현하여 검증한다:

- **전략 B**: 별도 Consumer Group + Offset 동기화
- **전략 C**: Pause/Resume Atomic Switch (주 권장 전략, 우선 검증)
- **전략 E**: Kafka Connect REST API 기반 (데이터 파이프라인형 워크로드 대상)

전략 A, D는 설계 문서에서 비교 기준으로만 사용하며 별도 구현은 하지 않는다.

## 검증 목표 지표

| 목표 항목 | 기준값 |
|-----------|--------|
| 전환 소요 시간 | < 30초 전체 / 전략 C는 < 5초 목표 |
| 롤백 소요 시간 | < 60초 전체 / 전략 C는 < 5초 목표 |
| 전환 중 메시지 유실 | 0건 |
| 전환 중 메시지 중복 | 전략별 측정 후 비교 |
| 전환 중 양쪽 동시 Active | 0회 |

## 계획 방법

- 작업에 대한 세부 태스크는 @plan/task{01}-subtask{01}.md와 같은 형식으로 따로 저장한다.
- 혹시 중간에 계획이 변경되면 위의 파일의 수정도 포함해서 @plan/changes.md에 관련 내용을 왜/어떻게를 포함해 기록한다.

## 필요한 작업 목록

- 아래 작업들에 대해 사용자가 어떻게 작업할 지 이해할 수 있게 @plan/ 디렉토리에 위 계획 방법에 기술한 대로 기록한다.
- 셋업, 테스트 수행에 대해서는 사용자가 직접 수행해 볼 수 있도록 @tutorial/ 디렉토리에 각각의 작업에 대해 상세히 기술한다.
- 셋업, 코드 구현시에 사용할 컴포넌트나 라이브러리 버전의 선택시에 그 이유에 대해서도 상세하게 @plan/decisions.md에 기록한다.
- 각 작업에 필요한 skills나 mcp가 필요한 경우 이를 적극적으로 제안한다.
  - skill 검색은 `skills search --json {키워드}`와 find-skills 스킬을 이용한다.

### 검증 테스트를 위한 kubernetes cluster 셋업

helm chart나 operator를 이용해 kubernetes에 아래 컴포넌트들을 설치한다.
- 지표 수집을 위한 prometheus 혹은 victoria metrics
- 로그 수집을 위한 loki 혹은 victoria logs
- kafka cluster (지표, 로그 수집 설정 포함)
- argo rollouts controller (전략 B, C 전환/롤백 오케스트레이션용)
  - 다른 배포 도구(Flagger, OpenKruise Rollout, Keptn)에 대해서는 TODO로 기록만 한다.
- strimzi operator (전략 E: Kafka Connect 기반 검증용, KafkaConnect/KafkaConnector CRD 관리)
- KEDA (Consumer Lag 기반 자동 스케일링 검증용, 선택)

### 검증 테스트를 위한 Producer, Consumer 코드 구현

- 우선 Java Spring 애플리케이션 구현체로 구현한다.
- 다른 언어와 프레임워크는 TODO로 기록만 하고 실제 작업은 진행하지 않는다.
- **전략 E 예외**: 전략 E(Kafka Connect)는 프레임워크 자체의 REST API 및 CRD 제어 방식을 검증하는 것이므로, 커스텀 Java 앱 대신 Strimzi `KafkaConnector` CRD와 표준 Sink Connector(FileStreamSink)를 SUT(테스트 대상 시스템)로 사용한다. 커스텀 Consumer 앱의 `/lifecycle` 엔드포인트는 전략 E에서 사용하지 않는다.
- 두 컴포넌트의 지표, 로그 수집에 대한 어플리케이션 로직/설정을 포함한다.
- 특정 토픽, 파티션에 대해 수행되도록 Producer, Consumer 설정(파일+환경변수)을 제공한다.
- Producer, Consumer는 실제 데이터를 다루기 위함이 아닌 테스트 목적이므로 임의(랜덤)의 데이터를 Produce하고, 이 데이터를 Consume하여 실제 Sink는 스킵하도록 한다.

**Producer 추가 기능:**
- 메시지 크기(바이트), 생성률(초당 건수)을 런타임 설정(파일+환경변수)과 REST API로 변경 가능

**Consumer 추가 기능 (전략 C 구현을 위해 필수):**
- `/lifecycle/pause`, `/lifecycle/resume`, `/lifecycle/status` HTTP 엔드포인트 구현
  - `AtomicBoolean` 플래그 기반 Thread-safe 간접 제어 (poll loop 내에서 실행)
  - `ConsumerRebalanceListener.onPartitionsAssigned`에서 pause 상태 재적용 로직 포함 (Rebalance 시 pause 유실 방지)
  - 상태 반환: `ACTIVE` / `PAUSED` / `DRAINING` 구분
- 아래 문제 사례들을 런타임 설정(파일+환경변수)과 REST API로 주입 가능하도록 구현:
  - **처리 지연**: 메시지당 처리 시간을 설정값만큼 sleep 추가하여 Consumer Lag 유발
  - **처리 실패율**: 설정된 비율로 메시지 처리 중 예외를 발생시켜 에러율 시뮬레이션
  - **느린 Offset 커밋**: `commitSync` 전 sleep을 추가하여 커밋 타이밍 문제 유발
  - **max.poll.interval.ms 초과**: 처리 지연을 설정값 이상으로 키워 Rebalance 강제 유발

**Switch Controller/Sidecar 구현 (전략 C 필수):**
- **Switch Sidecar (Go 권장)**: ConfigMap/CRD 변경을 Watch하여 Consumer App에 `/lifecycle` HTTP POST 전송
- **Switch Controller**: "Pause First, Resume Second" 원칙의 전환 오케스트레이션, K8s Lease 기반 양쪽 동시 Active 방지

### 테스트 수행

각 전략에 대해 아래 시나리오를 순서대로 수행하며, 전략 C를 우선 검증한다.

#### 공통 전제 조건
- Producer가 일정 속도(TPS 100 기준)로 메시지를 지속 생성 중인 상태에서 전환을 수행한다.
- 각 테스트 전 Blue Consumer Lag = 0을 확인 후 시작한다.
- 메시지 유실/중복 계산을 위해 Producer는 메시지에 시퀀스 번호를 포함하고, Consumer는 수신한 시퀀스를 기록한다.
- 테스트 완료 후 Producer 발행 로그와 Consumer 수신 로그(Loki 또는 파일)를 비교하여 유실/중복 시퀀스를 자동 산출하는 **Validator 스크립트**를 `tools/validator/`에 구현한다. TPS 100 기준 수만 건의 메시지를 수동 검증하면 신뢰도를 보장하기 어렵기 때문이다.

#### 시나리오 1: 정상 Blue → Green 전환

1. Blue Consumer가 정상 소비 중인 상태 확인 (Lag = 0)
2. Green Consumer 배포 및 준비 완료 대기 (모든 Pod Ready)
3. Blue → Green 전환 수행 (전략별 방식으로)
4. 전환 소요 시간 측정 (전환 명령 시각 ~ Green ACTIVE 확인 시각)
5. Green이 정상적으로 모든 파티션을 소비 중인지 확인
6. 전환 중 메시지 유실/중복 건수 확인 (시퀀스 번호 검증)
7. Blue Scale Down

**검증 기준:**
- 전환 완료 시간 < 30초 (전략 C: < 5초)
- 메시지 유실 = 0건
- Green Consumer Lag < 100 (전환 후 2분 이내 회복)

#### 시나리오 2: 전환 직후 즉시 롤백

1. Blue → Green 전환 수행
2. Green 활성화 직후 롤백 트리거
3. 롤백 소요 시간 측정 (롤백 명령 시각 ~ Blue ACTIVE 재확인 시각)
4. Blue가 정상 소비를 재개하는지 확인

**검증 기준:**
- 롤백 완료 시간 < 60초 (전략 C: < 5초)
- Blue 재개 후 Consumer Lag 급증 없음 (< 100 유지)

#### 시나리오 3: Consumer Lag 발생 중 전환

1. Consumer의 "처리 지연" 설정으로 Consumer Lag를 임계값 이상으로 유발 (Lag > 500)
2. Lag가 높은 상태에서 전환 시도
3. 전환 정책(Lag = 0 대기 vs 즉시 전환)에 따른 동작 관찰 및 기록
4. Lag 소진 후 전환 완료 확인

**검증 기준:**
- Lag 소진 대기 중 메시지 유실 = 0건
- 전략별 Lag 처리 방식 차이 측정 및 기록

#### 시나리오 4: 전환 중 Rebalance 장애 주입 (전략 C 전용)

1. 전환 도중 Consumer Pod를 임의로 재시작하여 Rebalance 유발
2. Rebalance 이후 pause 상태가 유지되는지 확인 (RebalanceListener 동작 검증)
3. 양쪽 동시 Active 발생 여부 모니터링 (`DualActiveConsumers` 알람)

**Static Membership 동작 검증 (sub-scenario):**

전략 C의 핵심 설계 가정인 Static Membership(KIP-345)이 실제로 Rebalance를 억제하는지 직접 검증한다.

1. `group.instance.id` 설정이 적용된 상태에서 Consumer Pod를 재시작
2. Kafka Broker 로그의 JoinGroup/SyncGroup 요청 발생 여부 확인
3. `kafka_consumer_rebalance_count_total` 지표로 Rebalance 발생 횟수 비교 (Static Membership 적용 전/후)
4. `session.timeout.ms` 이내에 Pod가 복귀하면 Rebalance가 생략되는지, 초과하면 발생하는지 각각 확인

**검증 기준:**
- Rebalance 이후에도 의도한 색상(Blue 또는 Green)만 ACTIVE 상태 유지
- 양쪽 동시 Active 발생 = 0회
- Static Membership 적용 시: `session.timeout.ms` 이내 Pod 복귀 시 Rebalance 미발생 확인

#### 시나리오 5: 전환 실패 후 자동 롤백

1. Green Consumer를 의도적으로 비정상 상태(높은 에러율 주입)로 배포
2. 전환 수행 후 헬스체크 실패 유발
3. Switch Controller 또는 Argo Rollouts의 자동 롤백 동작 확인
4. Blue 복구 후 정상 소비 재개 확인

**검증 기준:**
- 자동 롤백 수행 여부 확인
- Blue 복구 후 메시지 유실 없이 정상 소비 재개

### 지표/로그 수집/확인

#### Prometheus 수집 지표

| 지표명 | 설명 | 수집 주체 |
|--------|------|-----------|
| `kafka_consumer_group_lag` | Consumer Group Lag (파티션별) | Kafka Exporter / JMX Exporter |
| `kafka_consumer_lifecycle_state` | 라이프사이클 상태 (ACTIVE/PAUSED/DRAINING) | Consumer App (Micrometer) |
| `bg_switch_duration_seconds` | 전환 소요 시간 | Switch Controller |
| `kafka_consumer_records_consumed_rate` | 초당 소비 메시지 수 (TPS) | JMX Exporter |
| `kafka_consumer_errors_total` | Consumer 처리 에러 누적 수 | Consumer App (Micrometer) |
| `kafka_producer_records_sent_rate` | Producer 초당 발행 메시지 수 | JMX Exporter |
| `kafka_consumer_rebalance_count_total` | Rebalance 발생 횟수 | Consumer App (Micrometer) |
| `kafka_consumer_commit_latency_avg` | Offset commitSync 평균 지연 (ms) | JMX Exporter |

#### Grafana 대시보드 구성

테스트 중 실시간 확인을 위해 아래 패널로 구성한다.

- **Row 1: Blue/Green 상태 개요**
  - Active Version Badge (blue/green)
  - Blue/Green Pod 수 (Running/Ready)
  - 전환 상태 (준비중 / 전환중 / 완료 / 롤백중)

- **Row 2: Consumer Lag 비교**
  - Blue Consumer Lag 시계열
  - Green Consumer Lag 시계열
  - 파티션별 Lag 히트맵

- **Row 3: 처리 성능**
  - Messages/sec (Blue vs Green 비교)
  - Consumer 에러율 (Blue vs Green)
  - 평균 처리 지연시간 (ms)

- **Row 4: 전환 이벤트 타임라인**
  - Rebalance 발생 횟수 및 타이밍
  - Lifecycle 상태 변경 이벤트 (Grafana Annotation 기반)
  - 전환/롤백 소요 시간

#### 로그 수집 및 확인 항목 (Loki)

아래 로그 항목을 테스트 전후에 수집하고 이상 여부를 확인한다.

- **Consumer App 로그**:
  - `/lifecycle/pause`, `/lifecycle/resume` API 호출 및 응답 시간
  - `onPartitionsAssigned`, `onPartitionsRevoked` 이벤트와 pause 상태 복구 로그
  - 메시지 처리 실패 로그 (에러 스택 포함)
  - Offset commitSync 완료 시점 로그

- **Switch Controller/Sidecar 로그**:
  - ConfigMap 변경 감지 이벤트
  - Pause/Resume 요청 및 응답 결과
  - 전환 단계별 소요 시간
  - K8s Lease 획득/해제 로그 (양쪽 동시 Active 방지)

- **Kafka Broker 로그**:
  - Rebalance 발생 및 완료 이벤트
  - Consumer Group Offset 커밋 이벤트

#### 전략별 핵심 검증 항목

| 전략 | 핵심 검증 항목 |
|------|----------------|
| **전략 B** | Offset 동기화 정확도, 전환 중 중복 메시지 수 |
| **전략 C** | Rebalance 시 pause 상태 유지 여부, 양쪽 동시 Active 방지 동작 |
| **전략 E** | config topic 기반 pause 영속성, Strimzi CRD reconcile 충돌 없음 확인 |

### 테스트 리포트 작성

테스트 완료 후 아래 구조로 `report/test-report.md`에 리포트를 작성한다. 지표 스크린샷은 `report/screenshots/`에 저장한다.

#### 리포트 구성

1. **요약 (Executive Summary)**
   - 테스트 목적 및 범위
   - 전략별 검증 결과 요약 (통과/실패/조건부 통과)
   - 최종 권장 전략

2. **환경 정보**
   - Kubernetes 클러스터 사양 (노드 수, CPU/Memory)
   - 사용한 컴포넌트 버전 (Kafka, Strimzi, Argo Rollouts, Prometheus, Spring Boot 등)
   - 테스트 토픽 파티션 수, Consumer 레플리카 수, Producer TPS

3. **시나리오별 결과**

   각 시나리오에 대해:
   - 시나리오 목적 및 수행 조건
   - 측정된 지표 (전환 시간, 롤백 시간, 메시지 유실/중복 수, Lag 변화)
   - Grafana 스크린샷 첨부
   - 검증 기준 통과 여부 및 이유

4. **전략별 비교표**

   | 항목 | 전략 B | 전략 C | 전략 E |
   |------|--------|--------|--------|
   | 실측 전환 시간 | | | |
   | 실측 롤백 시간 | | | |
   | 메시지 유실 건수 | | | |
   | 메시지 중복 건수 | | | |
   | Rebalance 발생 횟수 | | | |
   | 구현 복잡도 (상/중/하) | | | |
   | 운영 복잡도 (상/중/하) | | | |

5. **발견된 문제 및 해결 방안**
   - 테스트 중 발견된 예상치 못한 문제 목록
   - 각 문제에 대한 원인 분석 및 해결 방법 또는 미해결 사항

6. **결론 및 권장사항**
   - 설계 문서의 예상 결과와 실제 테스트 결과 비교
   - 각 전략의 적합한 도입 시나리오 (운영 상황별 가이드)
   - 프로덕션 도입 시 추가 고려사항
