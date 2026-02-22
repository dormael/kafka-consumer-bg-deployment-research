# Tutorial 08: 전략 E 테스트 수행 (Kafka Connect REST API / Strimzi CRD)

> **관련 태스크:** plan/task07.md
> **우선순위:** 3순위
> **최종 수정:** 2026-02-22

---

## 공통 참고사항

### 제어 방식

전략 E는 **Strimzi KafkaConnector CRD**와 **Kafka Connect REST API** 두 가지 방식으로 Connector를 제어한다.

| 방식 | 특징 | 권장 |
|------|------|------|
| **CRD** (`kubectl patch kafkaconnector`) | Strimzi Operator가 reconcile, 영속성 보장 | **운영 권장** |
| **REST API** (`curl PUT /connectors/.../stop`) | 즉시 반영, 그러나 Operator reconcile에 의해 덮어써질 수 있음 | 비교 측정용 |

> **핵심:** Strimzi 환경에서는 CRD가 권위적(authoritative) 소스이다.
> REST API로 직접 변경한 상태는 Operator reconcile 주기(~120초)에 CRD의 `spec.state`로 복원된다.

### 전략 E의 SUT (테스트 대상 시스템)

전략 E는 커스텀 Java Consumer 앱이 아닌 **Kafka Connect FileStreamSinkConnector**를 SUT로 사용한다:
- 메시지를 Kafka Topic에서 읽어 파일(`/tmp/*-sink-output.txt`)에 기록
- 전략 C/B의 Consumer 앱과 달리 `/lifecycle/pause`, `/fault/*` 같은 커스텀 API가 없음
- 전환은 순수하게 Kafka Connect 프레임워크의 pause/stop/resume 메커니즘에 의존

### 헬퍼 함수

전환/롤백 절차를 자동화하는 헬퍼 스크립트를 `source`로 로드하여 사용한다:

```bash
source tools/strategy-e-switch.sh
```

사용 가능한 함수:

| 함수 | 용도 |
|------|------|
| `check_e_status` | Connector/Worker 상태 조회 (CRD + REST API) |
| `check_e_offsets` | Blue/Green Connect 그룹 offset 조회 |
| `switch_to_green_crd` | Blue→Green 전환 (CRD, Strimzi reconcile 대기) |
| `switch_to_blue_crd` | Green→Blue 롤백 (CRD) |
| `switch_to_green_rest` | Blue→Green 전환 (REST API, CRD 동시 변경) |
| `switch_to_blue_rest` | Green→Blue 롤백 (REST API, CRD 동시 변경) |
| `test_config_persist` | config topic 영속성 테스트 |
| `test_crd_rest_conflict` | CRD vs REST API 충돌 테스트 |

### 환경 정보

| 항목 | 값 |
|------|-----|
| Grafana | `http://192.168.58.2:30080` (admin / admin123) |
| Loki | ClusterIP `svc/loki:3100` (monitoring 네임스페이스) |
| Kafka Broker Pod | `kafka-cluster-dual-role-0` (kafka 네임스페이스) |
| Blue KafkaConnect | `connect-blue` (consumer group: `connect-cluster-blue`) |
| Green KafkaConnect | `connect-green` (consumer group: `connect-cluster-green`) |
| Blue Connector | `bg-sink-blue` (strimzi.io/cluster: connect-blue) |
| Green Connector | `bg-sink-green` (strimzi.io/cluster: connect-green) |
| Topic | `bg-test-topic` (8 partitions) |
| Blue REST API | `connect-blue-connect-api:8083` |
| Green REST API | `connect-green-connect-api:8083` |

---

## 아키텍처 개요: 전략 E

### Strimzi KafkaConnect 구조

```
Producer (100 TPS)
    │
    ▼
Kafka Topic: bg-test-topic (8 partitions)
    │
    ├── KafkaConnect: connect-blue
    │     ├── KafkaConnector: bg-sink-blue (RUNNING)
    │     │     ├── Task-0 (partition 0,1,2,3)
    │     │     └── Task-1 (partition 4,5,6,7)
    │     ├── Consumer Group: connect-cluster-blue
    │     ├── Config Topic: connect-configs-blue
    │     ├── Offset Topic: connect-offsets-blue
    │     └── Status Topic: connect-status-blue
    │
    └── KafkaConnect: connect-green
          ├── KafkaConnector: bg-sink-green (STOPPED)
          │     └── (Task 없음 — STOPPED 상태)
          ├── Consumer Group: connect-cluster-green
          ├── Config Topic: connect-configs-green
          ├── Offset Topic: connect-offsets-green
          └── Status Topic: connect-status-green

제어 경로:
  CRD 방식:  kubectl patch kafkaconnector → Strimzi Operator → Connect Worker
  REST 방식: curl PUT /connectors/.../stop → Connect Worker 직접 (Operator 미관여)
```

### 전략 C/B 대비 구조적 장단점

| 항목 | 전략 C (Pause/Resume) | 전략 B (Offset 동기화) | 전략 E (KafkaConnect) |
|------|----------------------|----------------------|----------------------|
| SUT | 커스텀 Java Consumer | 커스텀 Java Consumer | **FileStreamSinkConnector** |
| Consumer Group | 동일 | 별도 | **별도** (Connect Worker별) |
| 전환 방식 | HTTP pause/resume | Scale down/up + offset reset | **CRD state patch** |
| 전환 시간 (예상) | ~1초 | 30~60초 | **5~30초** (reconcile 포함) |
| Sidecar | 필요 (4-레이어) | 불필요 | **불필요** |
| Pod 재시작 필요 | 아니오 | 예 (scale) | **아니오** (Worker 유지) |
| 상태 영속성 | X (in-memory) | N/A | **O (config topic)** |
| 자동화 수준 | Switch Controller | 수동/스크립트 | **CRD 선언적** |

### KafkaConnector state 전이

```
           ┌─────────┐
     ┌─────│ RUNNING │─────┐
     │     └─────────┘     │
  pause()               stop()
     │                     │
     ▼                     ▼
┌─────────┐          ┌─────────┐
│ PAUSED  │          │ STOPPED │
└─────────┘          └─────────┘
     │                     │
  resume()             resume()
     │                     │
     └──────►┌─────────┐◄──┘
             │ RUNNING │
             └─────────┘
```

- **RUNNING → STOPPED**: Task가 완전히 제거됨 (리소스 해방)
- **RUNNING → PAUSED**: Task 존재하지만 소비 중단 (빠른 재개 가능)
- 본 테스트에서는 **STOPPED ↔ RUNNING** 전환을 주로 사용

---

## 사전 조건

### 1. 전략 C/B 리소스 Scale Down

전략 E 테스트 전에 이전 전략의 리소스를 중단하여 리소스 절약:

```bash
# 전략 C Consumer Scale Down
kubectl scale statefulset consumer-blue consumer-green -n kafka-bg-test --replicas=0
kubectl wait --for=jsonpath='{.status.readyReplicas}'=0 \
  statefulset/consumer-blue -n kafka-bg-test --timeout=60s 2>/dev/null || true
kubectl wait --for=jsonpath='{.status.readyReplicas}'=0 \
  statefulset/consumer-green -n kafka-bg-test --timeout=60s 2>/dev/null || true

# Switch Controller Scale Down
kubectl scale deployment bg-switch-controller -n kafka-bg-test --replicas=0

# 전략 B Consumer Scale Down (배포되어 있다면)
kubectl scale deployment consumer-b-blue consumer-b-green -n kafka-bg-test --replicas=0 2>/dev/null || true

# 확인
kubectl get statefulset,deployment -n kafka-bg-test
# 기대: 모든 replicas 0
```

### 2. KafkaConnect 클러스터 상태 확인

```bash
# KafkaConnect 클러스터 Ready 확인
kubectl get kafkaconnect -n kafka
# 기대:
#   connect-blue   Ready
#   connect-green  Ready

# Connect Worker Pod 확인
kubectl get pods -n kafka -l strimzi.io/kind=KafkaConnect
# 기대: connect-blue-connect-xxx Running, connect-green-connect-xxx Running

# REST API 접근 테스트
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  curl -s http://connect-blue-connect-api:8083/ | python3 -m json.tool
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  curl -s http://connect-green-connect-api:8083/ | python3 -m json.tool
# 기대: Kafka Connect 버전 정보 출력
```

### 3. Blue/Green Connector 배포

```bash
# Connector CRD 배포
kubectl apply -f k8s/strategy-e/connector-blue.yaml
kubectl apply -f k8s/strategy-e/connector-green.yaml

# 배포 확인
kubectl get kafkaconnector -n kafka
# 기대:
#   bg-sink-blue   ... RUNNING
#   bg-sink-green  ... STOPPED
```

### 4. Producer 동작 확인

```bash
kubectl exec -n kafka-bg-test deploy/bg-test-producer -- \
  curl -s http://localhost:8080/producer/stats
# 기대: messagesPerSecond: 100, running: true
```

### 5. Blue Connector 정상 소비 확인

```bash
# 헬퍼 함수 로드
source tools/strategy-e-switch.sh

# 전체 상태 확인
check_e_status
# 기대: bg-sink-blue RUNNING, bg-sink-green STOPPED

# Connect Group offset 확인
check_e_offsets
# 기대: connect-cluster-blue 그룹에 8개 파티션 할당, LAG ≈ 0

# Blue Connector 출력 파일 확인 (소비 중인지)
kubectl exec -n kafka deploy/connect-blue-connect -- \
  tail -5 /tmp/blue-sink-output.txt 2>/dev/null
# 기대: JSON 메시지가 출력됨
```

---

## 전략 E 구조적 특성 (테스트 전 필독)

전략 E는 Blue/Green이 **별도 KafkaConnect 클러스터**에서 별도 Consumer Group으로 동작한다.

```
KafkaConnect: connect-blue
  Consumer Group: connect-cluster-blue
  KafkaConnector: bg-sink-blue (RUNNING)
    → Task-0: partition 0,1,2,3
    → Task-1: partition 4,5,6,7
    → 출력: /tmp/blue-sink-output.txt

KafkaConnect: connect-green
  Consumer Group: connect-cluster-green
  KafkaConnector: bg-sink-green (STOPPED)
    → Task 없음 (STOPPED 상태)
```

**핵심 특성:**
- **Worker Pod 재시작 없이 전환**: CRD `spec.state` 변경만으로 Connector 상태 전환 (전략 B의 Pod scale과 대비)
- **config topic 영속성**: Connector의 pause/stop 상태가 `connect-configs-*` 토픽에 저장 → Worker 재시작 후에도 상태 복원
- **별도 offset topic**: `connect-offsets-blue/green`에 각각의 offset 저장 → 전략 B처럼 수동 offset 동기화 불필요
- **Strimzi Operator 간접 제어**: CRD 변경 → Strimzi Operator reconcile → REST API 호출 (간접 경로로 인한 지연)

**전략 E의 약점:**
- Strimzi reconcile 지연 (~5~30초, 최대 120초)
- FileStreamSinkConnector는 장애 주입 API 미지원 (시나리오 5에서 설정 오류로 대체)
- 별도 Connect 클러스터 2개로 인한 리소스 사용량 증가 (Worker Pod 2개)

---

## 시나리오 1: 정상 Blue → Green 전환 (CRD 방식)

### 목표

Blue RUNNING → Green RUNNING 전환이 30초 이내(CRD)에 완료되고, Connector 출력이 정상적으로 이관되는지 검증.

### 수행

```bash
# 헬퍼 스크립트 로드 (아직 안 했다면)
source tools/strategy-e-switch.sh

# 현재 상태 확인
check_e_status
# 기대: bg-sink-blue RUNNING, bg-sink-green STOPPED
```

**방법 A: 자동화 스크립트 사용**

```bash
# 한 번에 전환 (CRD 방식)
switch_to_green_crd
```

**방법 B: 수동 단계별 실행**

```bash
# 1. 시작 시각 기록
SWITCH_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Switch start: $SWITCH_START"

# 2. Blue Connector → STOPPED
kubectl patch kafkaconnector bg-sink-blue -n kafka \
  --type merge -p '{"spec":{"state":"stopped"}}'

# 3. Blue STOPPED 확인 (Strimzi reconcile 대기)
echo "Waiting for Blue to stop..."
while true; do
  STATE=$(kubectl get kafkaconnector bg-sink-blue -n kafka \
    -o jsonpath='{.status.connectorStatus.connector.state}' 2>/dev/null)
  echo "Blue state: $STATE"
  [ "$STATE" = "STOPPED" ] && break
  sleep 2
done

# 4. Green Connector → RUNNING
kubectl patch kafkaconnector bg-sink-green -n kafka \
  --type merge -p '{"spec":{"state":"running"}}'

# 5. Green RUNNING 확인
echo "Waiting for Green to run..."
while true; do
  STATE=$(kubectl get kafkaconnector bg-sink-green -n kafka \
    -o jsonpath='{.status.connectorStatus.connector.state}' 2>/dev/null)
  echo "Green state: $STATE"
  [ "$STATE" = "RUNNING" ] && break
  sleep 2
done

# 6. 완료 시각 기록
SWITCH_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "CRD Switch duration: $(( $(date -d "$SWITCH_END" +%s) - $(date -d "$SWITCH_START" +%s) )) seconds"
```

### 전환 완료 확인

```bash
# Connector 상태 확인
check_e_status
# 기대: bg-sink-blue STOPPED, bg-sink-green RUNNING

# Green Connector 출력 파일 확인
kubectl exec -n kafka deploy/connect-green-connect -- \
  tail -5 /tmp/green-sink-output.txt 2>/dev/null
# 기대: JSON 메시지가 출력됨

# Green Connect Group offset 확인
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group connect-cluster-green --describe
# 기대: 8개 파티션 할당, LAG 감소 중
```

### 안정화 대기 (2분)

```bash
sleep 120

# Green Connect Group Lag 확인 (안정화 후)
check_e_offsets
# 기대: connect-cluster-green LAG ≈ 0
```

### 유실/중복 검증 (Validator)

```bash
# 1. Loki port-forward 시작
kubectl port-forward -n monitoring svc/loki 3100:3100 &
LOKI_PF_PID=$!
sleep 2

# 2. 테스트 종료 시각
TEST_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)

# 3. Validator 실행
python3 tools/validator/validator.py \
  --source loki \
  --loki-url http://localhost:3100 \
  --start "$SWITCH_START" \
  --end "$TEST_END" \
  --switch-start "$SWITCH_START" \
  --switch-end "$SWITCH_END" \
  --strategy E \
  --output report/validation-e-scenario1.md

# 4. port-forward 종료
kill $LOKI_PF_PID 2>/dev/null
```

> **Validator 결과 해석 (전략 E 주의사항):**
> - FileStreamSinkConnector는 커스텀 Consumer 앱과 다른 로그 포맷을 사용
> - Validator가 Connect Worker 로그에서 sequence number를 파싱하지 못할 수 있음
> - 이 경우 **수동 검증** 필요: Blue 출력 파일의 마지막 메시지 ~ Green 출력 파일의 첫 메시지 연속성 확인

### 수동 유실 검증 (Validator 미지원 시)

```bash
# Blue 마지막 출력 확인
kubectl exec -n kafka deploy/connect-blue-connect -- \
  tail -1 /tmp/blue-sink-output.txt 2>/dev/null
# → 마지막 sequence number 확인

# Green 첫 출력 확인
kubectl exec -n kafka deploy/connect-green-connect -- \
  head -1 /tmp/green-sink-output.txt 2>/dev/null
# → 첫 sequence number 확인

# 두 sequence number가 연속적이면 유실 없음
```

### Grafana 스크린샷 저장

브라우저에서 `http://192.168.58.2:30080` → 대시보드를 열어 스크린샷 저장:

- Consumer Lag 시계열 → `report/screenshots/e-scenario1-lag.png`
- Messages/sec → `report/screenshots/e-scenario1-tps.png`

### 검증 기준

| 항목 | 기준 | 통과 조건 |
|------|------|-----------|
| CRD 전환 완료 시간 | < 30초 (Strimzi reconcile 포함) | 필수 |
| Green Connector RUNNING 확인 | spec + actual 모두 running | 필수 |
| Green Connect Group Lag (전환 후 2분) | < 100 | 필수 |
| 메시지 유실 | 0건 | 필수 |

---

## 시나리오 1-B: 정상 전환 (REST API 방식 - 비교용)

### 목표

REST API 직접 호출 방식의 전환 시간을 CRD 방식과 비교 측정한다.

> **주의:** Strimzi 환경에서는 CRD와 REST API 직접 호출이 충돌할 수 있다.
> 헬퍼 스크립트(`switch_to_green_rest`)는 CRD `spec.state`도 함께 변경하여 충돌을 방지한다.

### 사전 준비

시나리오 1 이후 상태(Green=RUNNING, Blue=STOPPED)에서 먼저 Blue 복원:

```bash
# CRD 방식으로 Blue 복원
switch_to_blue_crd

# 안정화 대기
sleep 30
check_e_status
# 기대: bg-sink-blue RUNNING, bg-sink-green STOPPED
```

### 수행

**방법 A: 자동화 스크립트 사용**

```bash
switch_to_green_rest
```

**방법 B: 수동 단계별 실행**

```bash
SWITCH_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)

# CRD spec.state 변경 (reconcile 충돌 방지)
kubectl patch kafkaconnector bg-sink-blue -n kafka \
  --type merge -p '{"spec":{"state":"stopped"}}'
kubectl patch kafkaconnector bg-sink-green -n kafka \
  --type merge -p '{"spec":{"state":"running"}}'

# REST API로 Blue stop
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  curl -s -X PUT http://connect-blue-connect-api:8083/connectors/bg-sink-blue/stop

# Blue STOPPED 폴링
while true; do
  STATE=$(kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
    curl -s http://connect-blue-connect-api:8083/connectors/bg-sink-blue/status \
    | python3 -c "import sys,json; print(json.load(sys.stdin)['connector']['state'])" 2>/dev/null)
  echo "Blue state: $STATE"
  [ "$STATE" = "STOPPED" ] && break
  sleep 0.5
done

# REST API로 Green resume
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  curl -s -X PUT http://connect-green-connect-api:8083/connectors/bg-sink-green/resume

# Green RUNNING 폴링
while true; do
  STATE=$(kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
    curl -s http://connect-green-connect-api:8083/connectors/bg-sink-green/status \
    | python3 -c "import sys,json; print(json.load(sys.stdin)['connector']['state'])" 2>/dev/null)
  echo "Green state: $STATE"
  [ "$STATE" = "RUNNING" ] && break
  sleep 0.5
done

SWITCH_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "REST API Switch duration: $(( $(date -d "$SWITCH_END" +%s) - $(date -d "$SWITCH_START" +%s) )) seconds"
```

### 비교 결과 기록

| 방식 | 전환 시간 | 비고 |
|------|-----------|------|
| CRD | _초 (시나리오 1) | Strimzi reconcile 포함 |
| REST API | _초 (시나리오 1-B) | 직접 호출, reconcile 미대기 |

### 검증 기준

| 항목 | 기준 |
|------|------|
| REST API 전환 시간 | < 5초 |
| CRD 전환 시간과의 차이 | 기록 (REST가 더 빠를 것으로 예상) |

---

## 시나리오 2: 전환 직후 즉시 롤백

### 목표

Blue→Green 전환 후 즉시 Green→Blue 롤백이 30초 이내에 완료되는지 검증.

### 사전 준비

시나리오 1-B 이후 상태(Green=RUNNING, Blue=STOPPED)에서 시작.

```bash
check_e_status
# 기대: bg-sink-blue STOPPED, bg-sink-green RUNNING
```

### 수행

**방법 A: 자동화 스크립트 사용**

```bash
switch_to_blue_crd
```

**방법 B: 수동 단계별 실행**

```bash
ROLLBACK_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Rollback start: $ROLLBACK_START"

# Green → STOPPED
kubectl patch kafkaconnector bg-sink-green -n kafka \
  --type merge -p '{"spec":{"state":"stopped"}}'

# Green STOPPED 대기
echo "Waiting for Green to stop..."
while true; do
  STATE=$(kubectl get kafkaconnector bg-sink-green -n kafka \
    -o jsonpath='{.status.connectorStatus.connector.state}' 2>/dev/null)
  echo "Green state: $STATE"
  [ "$STATE" = "STOPPED" ] && break
  sleep 2
done

# Blue → RUNNING
kubectl patch kafkaconnector bg-sink-blue -n kafka \
  --type merge -p '{"spec":{"state":"running"}}'

# Blue RUNNING 대기
echo "Waiting for Blue to run..."
while true; do
  STATE=$(kubectl get kafkaconnector bg-sink-blue -n kafka \
    -o jsonpath='{.status.connectorStatus.connector.state}' 2>/dev/null)
  echo "Blue state: $STATE"
  [ "$STATE" = "RUNNING" ] && break
  sleep 2
done

ROLLBACK_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Rollback duration: $(( $(date -d "$ROLLBACK_END" +%s) - $(date -d "$ROLLBACK_START" +%s) )) seconds"
```

### 검증

```bash
# Blue Connector 상태 확인
check_e_status
# 기대: bg-sink-blue RUNNING, bg-sink-green STOPPED

# Blue 소비 재개 확인
kubectl exec -n kafka deploy/connect-blue-connect -- \
  tail -3 /tmp/blue-sink-output.txt 2>/dev/null
# 기대: 새 메시지가 출력됨

# Connect Group offset 확인
sleep 60
check_e_offsets
# 기대: connect-cluster-blue LAG ≈ 0
```

### 검증 기준

| 항목 | 기준 |
|------|------|
| 롤백 완료 시간 | < 30초 (CRD) |
| Blue 소비 재개 | 확인 |
| 메시지 유실 | 0건 |

> **전략 C vs 전략 B vs 전략 E 비교 (롤백):**
> - 전략 C: ConfigMap 변경 → ~1초 (Controller가 즉시 반영)
> - 전략 B: Scale down/up + Offset reset → 30~60초
> - 전략 E: CRD patch → 5~30초 (Pod 재시작 불필요)

---

## 시나리오 3: Lag 발생 중 전환

### 목표

Blue Connector에 처리 지연을 유발한 상태에서 전환을 수행하고, Lag이 있는 상태에서의 동작을 관찰한다.

### 사전 준비 — Blue RUNNING 확인

```bash
check_e_status
# 기대: bg-sink-blue RUNNING, bg-sink-green STOPPED
```

### Lag 유발

FileStreamSinkConnector는 장애 주입 API가 없으므로, **tasksMax를 줄여서** 처리 속도를 저하시킨다:

```bash
# Blue Connector의 tasksMax를 1로 줄임 (기존 2 → 1)
# Task 수가 줄면 파티션 분배가 변경되어 처리 속도 저하
kubectl patch kafkaconnector bg-sink-blue -n kafka \
  --type merge -p '{"spec":{"tasksMax":1}}'

# 변경 확인
echo "tasksMax 변경 대기..."
sleep 30
kubectl get kafkaconnector bg-sink-blue -n kafka \
  -o jsonpath='{.spec.tasksMax}'
# 기대: 1
```

> **참고:** FileStreamSinkConnector의 tasksMax를 1로 줄이면 단일 Task가 8개 파티션을 모두 처리해야 하므로
> TPS 100 환경에서 점진적으로 Lag이 증가할 수 있다. 그러나 FileStreamSink는 매우 가벼워서
> 눈에 띄는 Lag이 발생하지 않을 수 있다. 이 경우 Lag 발생 여부를 기록한다.

### Lag 증가 대기

```bash
echo "Lag 증가 대기 (2분)..."
sleep 120

# Lag 확인
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group connect-cluster-blue --describe
# 관찰: LAG 값 기록
```

### 수행

```bash
SCENARIO3_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Switch start (with lag): $SCENARIO3_START"

# Lag 상태에서 전환
switch_to_green_crd

SCENARIO3_END=$SWITCH_END
```

### Green 시작 후 확인

```bash
# Green Connect Group offset 확인
sleep 60
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group connect-cluster-green --describe
# 관찰: Green이 어느 offset부터 소비를 시작했는지 기록
```

> **관찰 포인트 (전략 E 고유):**
> - Green Connector는 별도 Consumer Group(`connect-cluster-green`)을 사용
> - Green이 처음 RUNNING 될 때, offset이 없으면 `auto.offset.reset` 정책에 따름
>   (Kafka Connect 기본: `earliest` 또는 Connect 워커 설정에 따름)
> - 별도 offset topic이므로 Blue의 Lag과 무관하게 독립적으로 소비 시작
> - **전략 B와의 차이:** 전략 B는 `--to-current`로 수동 동기화하지만,
>   전략 E는 Connect 프레임워크의 auto.offset.reset에 의존

### tasksMax 복원

```bash
# 전환 완료 후 Green의 tasksMax는 2로 유지 (CRD에 정의된 값)
# Blue의 tasksMax 복원 (다음 시나리오를 위해)
kubectl patch kafkaconnector bg-sink-blue -n kafka \
  --type merge -p '{"spec":{"tasksMax":2}}'
```

### 검증 기준

| 항목 | 기준 |
|------|------|
| 전환 완료 시간 | < 30초 |
| Green 시작 후 Lag 회복 | < 2분 |
| Lag 상태에서의 동작 | 관찰 및 기록 |

---

## 시나리오 5: 전환 실패 후 롤백

### 목표

Green Connector에 잘못된 설정을 적용하여 FAILED 상태를 유발하고, 이를 관찰한 후 롤백하는 절차를 검증한다.

### 사전 준비

시나리오 3 이후 상태(Green=RUNNING, Blue=STOPPED)에서 먼저 Blue 복원:

```bash
switch_to_blue_crd

sleep 30
check_e_status
# 기대: bg-sink-blue RUNNING, bg-sink-green STOPPED
```

### Green Connector에 잘못된 설정 적용

```bash
SCENARIO5_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Scenario 5 start: $SCENARIO5_START"

# Green Connector 삭제 후 잘못된 설정으로 재생성
kubectl delete kafkaconnector bg-sink-green -n kafka 2>/dev/null

# 존재하지 않는 Connector class로 재생성
cat <<'EOF' | kubectl apply -f -
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaConnector
metadata:
  name: bg-sink-green
  namespace: kafka
  labels:
    strimzi.io/cluster: connect-green
    app: bg-connector
    color: green
    component: connector
    strategy: e
spec:
  class: com.example.NonExistentSinkConnector
  tasksMax: 2
  state: running
  config:
    topics: bg-test-topic
    file: /tmp/green-sink-output.txt
EOF

echo "잘못된 설정의 Green Connector 생성 완료"
```

### FAILED 상태 관찰

```bash
# Strimzi reconcile 대기
sleep 30

# Green Connector 상태 확인
kubectl get kafkaconnector bg-sink-green -n kafka \
  -o jsonpath='{.status.connectorStatus.connector.state}' 2>/dev/null
# 기대: FAILED 또는 UNASSIGNED

# 상세 에러 확인
kubectl get kafkaconnector bg-sink-green -n kafka -o yaml | grep -A 5 "trace"
# 기대: ClassNotFoundException 또는 유사 에러

# REST API로도 확인
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  curl -s http://connect-green-connect-api:8083/connectors/bg-sink-green/status \
  | python3 -m json.tool
```

> **관찰 포인트:**
> - 존재하지 않는 Connector class → Kafka Connect Worker가 FAILED 보고
> - Strimzi Operator는 CRD status에 FAILED 상태 반영
> - Blue Connector는 별도 KafkaConnect 클러스터이므로 **영향 없음** (격리 확인)

### Blue 정상 동작 확인

```bash
# Blue Connector가 Green 장애에 영향받지 않는지 확인
kubectl get kafkaconnector bg-sink-blue -n kafka \
  -o jsonpath='{.status.connectorStatus.connector.state}'
# 기대: RUNNING (영향 없음)

kubectl exec -n kafka deploy/connect-blue-connect -- \
  tail -3 /tmp/blue-sink-output.txt 2>/dev/null
# 기대: 정상 출력 계속
```

### 롤백 — 정상 설정으로 Green 복구

```bash
ROLLBACK_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Rollback start: $ROLLBACK_START"

# 잘못된 Green Connector 삭제
kubectl delete kafkaconnector bg-sink-green -n kafka

# 정상 설정으로 재생성 (STOPPED 상태)
kubectl apply -f k8s/strategy-e/connector-green.yaml

# Green이 STOPPED으로 정상 등록되었는지 확인
sleep 15
kubectl get kafkaconnector bg-sink-green -n kafka \
  -o jsonpath='{.status.connectorStatus.connector.state}'
# 기대: STOPPED (정상 등록)

ROLLBACK_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Rollback duration: $(( $(date -d "$ROLLBACK_END" +%s) - $(date -d "$ROLLBACK_START" +%s) )) seconds"
```

### 검증 기준

| 항목 | 기준 |
|------|------|
| Green FAILED 상태 관찰 | FAILED 또는 에러 메시지 확인 |
| Blue 격리 확인 | Green 장애 시 Blue 영향 없음 |
| 롤백 후 Green 정상 복구 | STOPPED 상태로 정상 등록 |
| 메시지 유실 | Blue가 계속 소비하므로 0건 |

> **전략 C vs 전략 B vs 전략 E 비교 (장애 시나리오):**
> - 전략 C: Green Consumer 에러 → Switch Controller 자동 롤백 가능
> - 전략 B: Green Consumer 에러 → 수동 롤백 (scale down/up)
> - 전략 E: Green Connector FAILED → CRD 삭제/재생성 또는 설정 수정

---

## 추가 검증 1: config topic 영속성

### 목적

Kafka Connect의 pause 상태가 config topic에 영구 저장되어, Worker Pod 재시작 후에도 유지되는지 확인한다.
이는 전략 C의 in-memory pause 유실 문제(P0)와 비교하기 위해 중요한 검증이다.

### 수행

**방법 A: 자동화 스크립트 사용**

```bash
test_config_persist
```

**방법 B: 수동 단계별 실행**

```bash
# 1. Blue Connector를 PAUSED로 변경
kubectl patch kafkaconnector bg-sink-blue -n kafka \
  --type merge -p '{"spec":{"state":"paused"}}'

# 2. Blue의 PAUSED 확인
echo "Blue PAUSED 대기..."
while true; do
  STATE=$(kubectl get kafkaconnector bg-sink-blue -n kafka \
    -o jsonpath='{.status.connectorStatus.connector.state}' 2>/dev/null)
  echo "Blue state: $STATE"
  [ "$STATE" = "PAUSED" ] && break
  sleep 2
done

# 3. Connect Worker Pod 강제 삭제 (재시작 유발)
echo "Connect Worker Pod 강제 삭제..."
kubectl delete pod -n kafka -l strimzi.io/name=connect-blue-connect --force

# 4. Worker 복귀 대기
echo "Worker 복귀 대기..."
sleep 5
kubectl wait --for=condition=Ready pod -n kafka \
  -l strimzi.io/name=connect-blue-connect --timeout=120s

# Strimzi reconcile 대기
echo "Strimzi reconcile 대기 (30초)..."
sleep 30

# 5. Blue가 여전히 PAUSED인지 확인
STATE=$(kubectl get kafkaconnector bg-sink-blue -n kafka \
  -o jsonpath='{.status.connectorStatus.connector.state}' 2>/dev/null)
echo "재시작 후 Blue 상태: $STATE"

if [ "$STATE" = "PAUSED" ]; then
  echo "PASS: config topic에서 PAUSED 상태가 정상 복원됨"
else
  echo "FAIL: 기대 PAUSED, 실제 $STATE"
fi
```

### 정리

```bash
# Blue를 RUNNING으로 복원
kubectl patch kafkaconnector bg-sink-blue -n kafka \
  --type merge -p '{"spec":{"state":"running"}}'

echo "Blue RUNNING 대기..."
while true; do
  STATE=$(kubectl get kafkaconnector bg-sink-blue -n kafka \
    -o jsonpath='{.status.connectorStatus.connector.state}' 2>/dev/null)
  [ "$STATE" = "RUNNING" ] && break
  sleep 2
done
echo "Blue 복원 완료"
```

### 검증 기준

| 항목 | 기준 |
|------|------|
| Worker 재시작 후 PAUSED 유지 | 필수 (PASS) |

> **전략 C와의 비교:**
> - 전략 C: Consumer Pod 재시작 시 pause 유실 → PAUSED → ACTIVE 전이 위험 (P0, Sidecar 안전망으로 해결)
> - 전략 E: **config topic에 영구 저장** → Worker 재시작 후에도 pause 유지 (아키텍처적 장점)

---

## 추가 검증 2: REST API vs CRD 충돌

### 목적

CRD에 `state: running`으로 설정된 상태에서 REST API로 직접 pause를 호출하면,
Strimzi Operator의 reconcile에 의해 CRD의 `state: running`으로 복원되는지 확인한다.

### 수행

**방법 A: 자동화 스크립트 사용**

```bash
test_crd_rest_conflict
```

**방법 B: 수동 단계별 실행**

```bash
# 1. CRD에 state: running 확인
kubectl get kafkaconnector bg-sink-blue -n kafka -o jsonpath='{.spec.state}'
# running

# 2. REST API로 직접 pause
echo "REST API로 Blue pause..."
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  curl -s -X PUT http://connect-blue-connect-api:8083/connectors/bg-sink-blue/pause

# 3. 즉시 상태 확인 (REST API에 의해 PAUSED)
sleep 2
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  curl -s http://connect-blue-connect-api:8083/connectors/bg-sink-blue/status \
  | python3 -c "import sys,json; print('REST status:', json.load(sys.stdin)['connector']['state'])" 2>/dev/null
# 기대: PAUSED

# 4. CRD spec.state 확인 (변경 없음)
kubectl get kafkaconnector bg-sink-blue -n kafka -o jsonpath='{.spec.state}'
echo ""
# 기대: running (CRD는 변경되지 않음)

# 5. Strimzi reconcile 대기 (최대 180초)
echo "Strimzi reconcile 대기 (최대 180초)..."
echo "Strimzi 기본 reconcile 주기: 약 120초"
for i in $(seq 1 18); do
  sleep 10
  STATE=$(kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
    curl -s http://connect-blue-connect-api:8083/connectors/bg-sink-blue/status 2>/dev/null \
    | python3 -c "import sys,json; print(json.load(sys.stdin)['connector']['state'])" 2>/dev/null)
  echo "  $((i * 10))초: $STATE"
  [ "$STATE" = "RUNNING" ] && break
done

# 6. 최종 상태 확인
FINAL_STATE=$(kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  curl -s http://connect-blue-connect-api:8083/connectors/bg-sink-blue/status 2>/dev/null \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['connector']['state'])" 2>/dev/null)
echo ""
echo "최종 상태: $FINAL_STATE"

if [ "$FINAL_STATE" = "RUNNING" ]; then
  echo "PASS: Strimzi가 CRD의 running으로 복원함"
  echo "→ Strimzi 환경에서는 반드시 CRD를 통해 제어해야 함"
else
  echo "OBSERVE: REST API pause가 유지됨"
  echo "→ reconcile 주기 확인 필요, 또는 Strimzi Operator 설정 확인"
fi
```

### 검증 기준

| 항목 | 기준 |
|------|------|
| REST API pause 즉시 반영 | PAUSED 확인 |
| Strimzi reconcile 후 복원 | RUNNING으로 복원 확인 |
| reconcile 소요 시간 | 기록 (~120초 예상) |

> **핵심 교훈:**
> - Strimzi 환경에서 REST API 직접 호출은 **일시적**이며, CRD가 **권위적 소스(source of truth)**
> - 운영 환경에서는 반드시 `kubectl patch kafkaconnector` (CRD 방식)을 사용해야 함
> - REST API는 즉시 반영이 필요한 긴급 상황에서만 사용하되, CRD도 함께 변경해야 함

---

## 결과 요약 템플릿

각 시나리오 수행 후 아래 표를 채워 `report/` 디렉토리에 저장한다.

```markdown
| 시나리오 | 전환 시간 (CRD) | 전환 시간 (REST) | 유실 | 중복 | 결과 |
|----------|-----------------|------------------|------|------|------|
| 1. 정상 전환 | _초 | - | _건 | _건 | PASS/FAIL |
| 1-B. REST 비교 | - | _초 | _건 | _건 | PASS/FAIL |
| 2. 즉시 롤백 | _초 | - | _건 | _건 | PASS/FAIL |
| 3. Lag 중 전환 | _초 | - | _건 | _건 | PASS/FAIL |
| 5. 실패 후 롤백 | _초 | - | _건 | _건 | PASS/FAIL |
| config topic 영속성 | - | - | - | - | PASS/FAIL |
| REST vs CRD 충돌 | - | - | - | - | PASS/FAIL |
```

### 3개 전략 비교 요약

```markdown
| 항목 | 전략 C | 전략 B | 전략 E |
|------|--------|--------|--------|
| 전환 시간 | ~_초 | ~_초 | ~_초 |
| 롤백 시간 | ~_초 | ~_초 | ~_초 |
| 메시지 유실 | _건 | _건 | _건 |
| 메시지 중복 | _건 | _건 | _건 |
| 상태 영속성 | X (in-memory) | N/A | O (config topic) |
| 자동화 수준 | Controller | 스크립트 | CRD 선언적 |
| Pod 재시작 | 불필요 | 필요 | 불필요 |
| 인프라 복잡도 | 높음 (Sidecar) | 낮음 | 중간 (Strimzi) |
```

---

## 정리 및 원복

### 전략 E 리소스 정리

```bash
# 1. Connector 삭제
kubectl delete kafkaconnector bg-sink-blue bg-sink-green -n kafka

# 2. Connector 삭제 확인
kubectl get kafkaconnector -n kafka
# 기대: No resources found

# 참고: KafkaConnect 클러스터(connect-blue, connect-green)는 유지
# 전략 E를 다시 테스트하려면 Connector만 재배포하면 됨
```

### 전략 C 리소스 복원 (필요 시)

```bash
# 1. 전략 C Consumer 복원
kubectl scale statefulset consumer-blue -n kafka-bg-test --replicas=3
kubectl scale statefulset consumer-green -n kafka-bg-test --replicas=3
kubectl scale deployment bg-switch-controller -n kafka-bg-test --replicas=1

# 2. 전략 C 복원 확인
kubectl rollout status statefulset consumer-blue -n kafka-bg-test --timeout=120s
kubectl rollout status statefulset consumer-green -n kafka-bg-test --timeout=120s

# 3. ConfigMap active=blue 설정
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"blue"}}'

# 4. 전략 C 정상 동작 확인
sleep 30
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  curl -s http://localhost:8080/lifecycle/status
# 기대: {"state":"ACTIVE",...}
```

---

## 트러블슈팅

### KafkaConnect 클러스터가 NotReady

```bash
# Strimzi Operator 로그 확인
kubectl logs -n kafka deploy/strimzi-cluster-operator --tail=30 | grep -i "connect"

# KafkaConnect 상태 확인
kubectl describe kafkaconnect connect-blue -n kafka | tail -30

# Connect Worker Pod 확인
kubectl get pods -n kafka -l strimzi.io/kind=KafkaConnect
kubectl describe pod -n kafka -l strimzi.io/name=connect-blue-connect | tail -20
```

### Connector가 RUNNING으로 전환되지 않음

```bash
# Strimzi Operator reconcile 대기 (최대 120초)
# CRD 변경 후 즉시 반영되지 않을 수 있음

# Connector 상세 상태 확인
kubectl get kafkaconnector bg-sink-green -n kafka -o yaml

# REST API로 직접 확인
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  curl -s http://connect-green-connect-api:8083/connectors/bg-sink-green/status \
  | python3 -m json.tool

# Strimzi Operator를 재시작하면 즉시 reconcile 발생
kubectl rollout restart deployment strimzi-cluster-operator -n kafka
```

### FileStreamSinkConnector 출력 파일이 비어있음

```bash
# Connector Task 상태 확인
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  curl -s http://connect-blue-connect-api:8083/connectors/bg-sink-blue/status \
  | python3 -m json.tool
# Task 상태가 RUNNING인지 확인

# Connect Worker 로그에서 에러 확인
kubectl logs -n kafka deploy/connect-blue-connect --tail=30

# Topic에 메시지가 있는지 확인
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-console-consumer.sh --bootstrap-server localhost:9092 \
    --topic bg-test-topic --from-beginning --max-messages 3
```

### REST API 접근 불가

```bash
# Connect Worker Pod가 Running인지 확인
kubectl get pods -n kafka -l strimzi.io/kind=KafkaConnect

# Service 확인
kubectl get svc -n kafka | grep connect

# Kafka Pod에서 접근 테스트 (DNS 확인)
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  curl -sv http://connect-blue-connect-api:8083/ 2>&1 | head -10
```

### config topic 영속성 테스트에서 상태가 복원되지 않음

```bash
# config topic 내용 확인
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-console-consumer.sh --bootstrap-server localhost:9092 \
    --topic connect-configs-blue --from-beginning --max-messages 5 2>/dev/null

# Strimzi Operator가 CRD spec.state를 기반으로 reconcile하므로,
# CRD에 state: paused가 설정되어 있다면 config topic과 무관하게 PAUSED 복원
kubectl get kafkaconnector bg-sink-blue -n kafka -o jsonpath='{.spec.state}'
```

---

## 다음 단계

- [09-cleanup.md](09-cleanup.md): 테스트 환경 정리
- `plan/task08.md`: 테스트 리포트 작성 (3개 전략 비교 분석)
