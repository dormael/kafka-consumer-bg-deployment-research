# Task 07: 전략 E 테스트 수행 (Kafka Connect REST API / Strimzi CRD)

> **의존:** task01 (Strimzi KafkaConnect 클러스터), task04 (Validator)
> **우선순위:** 3순위
> **튜토리얼:** `tutorial/08-strategy-e-test.md`
> **상태:** 코드 생성 완료 (2026-02-22)

---

## 목표

전략 E(Kafka Connect REST API 기반)를 Strimzi KafkaConnector CRD와 표준 Sink Connector(FileStreamSink)를 사용하여 검증한다.

## 전략 E의 핵심 차이점 (vs 전략 C, B)

| 항목 | 전략 C | 전략 B | 전략 E |
|------|--------|--------|--------|
| SUT | 커스텀 Java Consumer | 커스텀 Java Consumer | **FileStreamSinkConnector** |
| Consumer Group | 같은 group.id 공유 | 별도 group.id (blue/green) | **별도** (Connect Worker별) |
| 전환 방식 | Pause/Resume HTTP | Scale down/up + Offset reset | **CRD state patch** |
| 전환 시간 예상 | ~1초 | 30~60초 | **5~30초** (reconcile 포함) |
| Sidecar 필요 | 필요 (4-레이어 안전망) | 불필요 | **불필요** |
| Pod 재시작 | 불필요 | 필요 (Scale) | **불필요** |
| 상태 영속성 | X (in-memory) | N/A | **O (config topic)** |
| Offset 동기화 | 불필요 (같은 Group) | 필수 (kafka-consumer-groups.sh) | **불필요** (별도 offset topic) |
| 자동화 수준 | Switch Controller | 수동/스크립트 | **CRD 선언적** |

## 설계 결정

| # | 결정 | 근거 |
|---|------|------|
| D1 | FileStreamSinkConnector를 SUT로 사용 | Kafka 기본 포함, 추가 빌드 불필요 |
| D2 | CRD를 주 제어 방식으로 사용 | Strimzi Operator가 권위적 소스, REST API는 비교용 |
| D3 | Blue/Green 별도 KafkaConnect 클러스터 | 물리적 분리, 별도 config/offset/status 토픽 |
| D4 | Green 초기 STOPPED (PAUSED 아님) | KIP-980 STOPPED: Task 미생성으로 리소스 절약 |
| D5 | REST API 사용 시 CRD 동시 변경 | reconcile 충돌 방지 패턴 |
| D6 | tasksMax 조정으로 Lag 유발 | FileStreamSink에 장애 주입 API 미존재 |
| D7 | 존재하지 않는 class로 FAILED 유발 | 시나리오 5: 설정 오류 기반 장애 시뮬레이션 |

## SUT(테스트 대상 시스템)

전략 E는 커스텀 Java Consumer 앱 대신 **Strimzi KafkaConnector CRD + FileStreamSinkConnector**를 SUT로 사용한다.

### Blue Connector (KafkaConnector CRD)

```yaml
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaConnector
metadata:
  name: bg-sink-blue
  namespace: kafka
  labels:
    strimzi.io/cluster: connect-blue
spec:
  class: org.apache.kafka.connect.file.FileStreamSinkConnector
  tasksMax: 2
  state: running
  config:
    topics: bg-test-topic
    file: /tmp/blue-sink-output.txt
```

### Green Connector (초기 STOPPED)

```yaml
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaConnector
metadata:
  name: bg-sink-green
  namespace: kafka
  labels:
    strimzi.io/cluster: connect-green
spec:
  class: org.apache.kafka.connect.file.FileStreamSinkConnector
  tasksMax: 2
  state: stopped
  config:
    topics: bg-test-topic
    file: /tmp/green-sink-output.txt
```

## 생성/수정 파일

| 구분 | 파일 | 설명 |
|------|------|------|
| 생성 | `k8s/strategy-e/connector-blue.yaml` | Blue KafkaConnector CRD (state: running) |
| 생성 | `k8s/strategy-e/connector-green.yaml` | Green KafkaConnector CRD (state: stopped) |
| 생성 | `tools/strategy-e-switch.sh` | 전환/롤백 헬퍼 스크립트 (8개 함수) |
| 수정 | `tutorial/08-strategy-e-test.md` | 스켈레톤 → 상세 튜토리얼 (6개 시나리오 + 추가 검증 2개) |
| 수정 | `plan/task07.md` | 구현 상태 및 설계 결정 추가 |
| 수정 | `plan/plan.md` | task07 상태 갱신 |

## 전환 절차 (헬퍼 스크립트 자동화)

```bash
source tools/strategy-e-switch.sh

# CRD 방식 (권장)
switch_to_green_crd   # Blue→Green 전환
switch_to_blue_crd    # Green→Blue 롤백

# REST API 방식 (비교용, CRD 동시 변경)
switch_to_green_rest  # Blue→Green 전환
switch_to_blue_rest   # Green→Blue 롤백

# 추가 검증
test_config_persist     # config topic 영속성
test_crd_rest_conflict  # CRD vs REST API 충돌
```

수동 CRD 절차:
1. Blue Connector → STOPPED (`kubectl patch kafkaconnector`)
2. Strimzi Operator reconcile 대기 (Blue STOPPED 확인)
3. Green Connector → RUNNING (`kubectl patch kafkaconnector`)
4. Strimzi Operator reconcile 대기 (Green RUNNING 확인)

## 핵심 검증 항목 (전략 E 고유)

| 항목 | 설명 |
|------|------|
| **config topic 기반 pause 영속성** | Worker 재시작 후에도 pause 상태 유지 확인 |
| **Strimzi CRD reconcile 충돌 없음** | CRD로 상태를 제어하므로 REST API 직접 호출과의 충돌이 없는지 확인 |
| **REST API vs CRD 제어 비교** | 두 방식의 전환 시간 차이 측정 |
| **KIP-980 STOPPED 상태** | 초기 STOPPED 상태에서 생성한 Connector가 올바르게 동작하는지 |

## 시나리오 수행

### 시나리오 목록

| # | 시나리오 | 핵심 검증 항목 |
|---|----------|----------------|
| 1 | 정상 Blue→Green 전환 (CRD) | 전환 시간 <30초, Connector 출력 이관 |
| 1-B | 정상 전환 (REST API, 비교) | REST 전환 시간 <5초, CRD와 비교 |
| 2 | 전환 직후 즉시 롤백 | 롤백 시간 <30초, Blue 소비 재개 |
| 3 | Lag 발생 중 전환 | tasksMax 줄여 Lag 유발, 전환 동작 관찰 |
| 5 | 전환 실패 후 롤백 | 잘못된 class → FAILED, Blue 격리, 롤백 |
| 추가-1 | config topic 영속성 | Worker 재시작 후 PAUSED 유지 확인 |
| 추가-2 | REST API vs CRD 충돌 | REST pause → Strimzi reconcile → RUNNING 복원 |

### 시나리오 3: Lag 유발 방법

FileStreamSinkConnector에는 장애 주입 API가 없으므로, `tasksMax`를 줄여 처리 속도를 저하:

```bash
kubectl patch kafkaconnector bg-sink-blue -n kafka \
  --type merge -p '{"spec":{"tasksMax":1}}'
```

### 시나리오 5: FAILED 유발 방법

존재하지 않는 Connector class로 재생성:

```bash
kubectl delete kafkaconnector bg-sink-green -n kafka
# NonExistentSinkConnector → ClassNotFoundException → FAILED
```

## 검증 기준

| 항목 | 기준 |
|------|------|
| CRD 기반 전환 시간 | < 30초 (Strimzi reconcile 포함) |
| REST API 기반 전환 시간 | < 5초 |
| config topic 영속성 | Worker 재시작 후 pause 유지 |
| CRD-REST 충돌 | CRD 제어 시 충돌 없음 확인 |
| 메시지 유실 | 0건 |

## 결과 정리

| 시나리오 | 전환 시간 (CRD) | 전환 시간 (REST) | 유실 | 중복 | 결과 |
|----------|-----------------|------------------|------|------|------|
| 1. 정상 전환 | | | | | |
| 1-B. REST 비교 | | | | | |
| 2. 즉시 롤백 | | | | | |
| 3. Lag 중 전환 | | | | | |
| 5. 실패 후 롤백 | | | | | |
| config topic 영속성 | | | | | |
| REST vs CRD 충돌 | | | | | |
