# Task 07: 전략 E 테스트 수행 (Kafka Connect REST API / Strimzi CRD)

> **의존:** task01 (Strimzi KafkaConnect 클러스터), task04 (Validator)
> **우선순위:** 3순위
> **튜토리얼:** `tutorial/08-strategy-e-test.md`

---

## 목표

전략 E(Kafka Connect REST API 기반)를 Strimzi KafkaConnector CRD와 표준 Sink Connector(FileStreamSink)를 사용하여 검증한다.

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
    file: /tmp/blue-output.txt
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
  state: stopped  # 대기 상태
  config:
    topics: bg-test-topic
    file: /tmp/green-output.txt
```

## 전환 절차 (CRD 제어 방식)

```bash
# 1. Blue Connector를 STOPPED으로 전환
kubectl patch kafkaconnector bg-sink-blue -n kafka \
  --type merge -p '{"spec":{"state":"stopped"}}'

# 2. Blue STOPPED 확인 (Strimzi Operator가 reconcile)
kubectl get kafkaconnector bg-sink-blue -n kafka -o jsonpath='{.status.connectorStatus.connector.state}'

# 3. (선택) Offset 동기화 - KIP-875 REST API
# Green이 Blue의 마지막 offset부터 시작하도록

# 4. Green Connector를 RUNNING으로 전환
kubectl patch kafkaconnector bg-sink-green -n kafka \
  --type merge -p '{"spec":{"state":"running"}}'

# 5. Green RUNNING 확인
kubectl get kafkaconnector bg-sink-green -n kafka -o jsonpath='{.status.connectorStatus.connector.state}'
```

## 핵심 검증 항목 (전략 E 고유)

| 항목 | 설명 |
|------|------|
| **config topic 기반 pause 영속성** | Worker 재시작 후에도 pause 상태 유지 확인 |
| **Strimzi CRD reconcile 충돌 없음** | CRD로 상태를 제어하므로 REST API 직접 호출과의 충돌이 없는지 확인 |
| **REST API vs CRD 제어 비교** | 두 방식의 전환 시간 차이 측정 |
| **KIP-980 STOPPED 상태** | 초기 STOPPED 상태에서 생성한 Connector가 올바르게 동작하는지 |

## 시나리오 수행

### 시나리오 1: 정상 전환

1. Producer가 TPS 100으로 메시지 생성 중
2. Blue Connector RUNNING, Green Connector STOPPED
3. CRD patch로 전환 수행
4. 전환 시간 측정 (CRD patch 시점 ~ Green RUNNING 확인 시점)

### 시나리오 2: 즉시 롤백

1. 전환 직후 롤백 (CRD patch로 Blue RUNNING, Green STOPPED)

### 시나리오 3: Lag 발생 중 전환

1. Consumer측 처리 지연은 Connector 설정으로 시뮬레이션
   - FileStreamSink에 쓰기 지연을 줄 수 없으므로, tasksMax를 1로 줄여 처리 속도 저하 유발

### 시나리오 5: 전환 실패 후 롤백

1. Green Connector에 잘못된 설정 적용 (예: 존재하지 않는 파일 경로)
2. RUNNING으로 전환 시도 → FAILED 상태 관찰
3. 롤백 수행

### 추가: config topic 영속성 검증

1. Blue Connector PAUSED 상태에서 Connect Worker Pod 강제 재시작
   ```bash
   kubectl delete pod connect-blue-connect-0 -n kafka
   ```
2. Worker 복귀 후 Blue가 여전히 PAUSED 상태인지 확인
3. 이 검증은 전략 C의 인메모리 pause 유실 문제와의 비교를 위해 중요

### 추가: Strimzi REST API 직접 호출 vs CRD 충돌 검증

1. CRD에 `state: running`으로 설정된 상태에서
2. REST API로 직접 pause 호출
   ```bash
   curl -X PUT http://connect-blue-connect-api:8083/connectors/bg-sink-blue/pause
   ```
3. Strimzi Operator의 reconcile 주기(기본 120초) 이후 상태 확인
4. REST API pause가 CRD의 running으로 덮어써지는지 확인

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
| 2. 즉시 롤백 | | | | | |
| 3. Lag 중 전환 | | | | | |
| 5. 실패 후 롤백 | | | | | |
| config topic 영속성 | | | | | |
| REST vs CRD 충돌 | | | | | |
