# Tutorial 08: 전략 E 테스트 수행 (Kafka Connect REST API / Strimzi CRD)

> **관련 태스크:** plan/task07.md
> **우선순위:** 3순위

---

## 사전 조건

- Blue/Green KafkaConnect 클러스터 배포 완료 (Tutorial 03, Step 6)
- Producer가 TPS 100으로 메시지 생성 중

```bash
# KafkaConnect 상태 확인
kubectl get kafkaconnect -n kafka
# connect-blue   Ready
# connect-green  Ready

# REST API 접근 테스트
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  curl -s http://connect-blue-connect-api:8083/ | jq .
```

---

## Step 1: Blue/Green Connector 생성

### Blue Connector (RUNNING)

```yaml
# k8s/connector-blue.yaml
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

### Green Connector (STOPPED)

```yaml
# k8s/connector-green.yaml
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

```bash
kubectl apply -f k8s/connector-blue.yaml
kubectl apply -f k8s/connector-green.yaml

# Connector 상태 확인
kubectl get kafkaconnector -n kafka
```

---

## 시나리오 1: 정상 전환 (CRD 방식)

### 수행

```bash
SWITCH_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)

# Step 1: Blue Connector → STOPPED
kubectl patch kafkaconnector bg-sink-blue -n kafka \
  --type merge -p '{"spec":{"state":"stopped"}}'

# Step 2: Blue STOPPED 확인 (Strimzi reconcile 대기)
echo "Waiting for Blue to stop..."
while true; do
  STATE=$(kubectl get kafkaconnector bg-sink-blue -n kafka \
    -o jsonpath='{.status.connectorStatus.connector.state}' 2>/dev/null)
  echo "Blue state: $STATE"
  [ "$STATE" = "STOPPED" ] && break
  sleep 2
done

# Step 3: Green Connector → RUNNING
kubectl patch kafkaconnector bg-sink-green -n kafka \
  --type merge -p '{"spec":{"state":"running"}}'

# Step 4: Green RUNNING 확인
echo "Waiting for Green to run..."
while true; do
  STATE=$(kubectl get kafkaconnector bg-sink-green -n kafka \
    -o jsonpath='{.status.connectorStatus.connector.state}' 2>/dev/null)
  echo "Green state: $STATE"
  [ "$STATE" = "RUNNING" ] && break
  sleep 2
done

SWITCH_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "CRD Switch duration: $(( $(date -d "$SWITCH_END" +%s) - $(date -d "$SWITCH_START" +%s) )) seconds"
```

---

## 시나리오 1-B: 정상 전환 (REST API 직접 호출 - 비교용)

**주의:** Strimzi 환경에서는 CRD와 REST API 직접 호출이 충돌할 수 있다. 이 시나리오는 비교 측정용으로만 수행한다.

```bash
SWITCH_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)

# REST API로 직접 전환
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  curl -s -X PUT http://connect-blue-connect-api:8083/connectors/bg-sink-blue/stop

# 상태 확인 (폴링)
while true; do
  STATE=$(kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
    curl -s http://connect-blue-connect-api:8083/connectors/bg-sink-blue/status \
    | jq -r '.connector.state')
  echo "Blue state: $STATE"
  [ "$STATE" = "STOPPED" ] && break
  sleep 0.5
done

kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  curl -s -X PUT http://connect-green-connect-api:8083/connectors/bg-sink-green/resume

SWITCH_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "REST API Switch duration: $(( $(date -d "$SWITCH_END" +%s) - $(date -d "$SWITCH_START" +%s) )) seconds"
```

---

## 시나리오 2: 즉시 롤백

```bash
# Green → Blue 롤백 (CRD 방식)
kubectl patch kafkaconnector bg-sink-green -n kafka \
  --type merge -p '{"spec":{"state":"stopped"}}'
kubectl patch kafkaconnector bg-sink-blue -n kafka \
  --type merge -p '{"spec":{"state":"running"}}'
```

---

## 추가 검증: config topic 영속성

### 목적
Kafka Connect의 pause 상태가 config topic에 영구 저장되어, Worker 재시작 후에도 유지되는지 확인한다.

```bash
# 1. Blue Connector를 PAUSED로 변경
kubectl patch kafkaconnector bg-sink-blue -n kafka \
  --type merge -p '{"spec":{"state":"paused"}}'

# 2. Blue의 PAUSED 확인
kubectl get kafkaconnector bg-sink-blue -n kafka \
  -o jsonpath='{.status.connectorStatus.connector.state}'
# PAUSED

# 3. Connect Worker Pod 강제 삭제 (재시작)
kubectl delete pod -n kafka -l strimzi.io/name=connect-blue-connect

# 4. Worker 복귀 대기
kubectl rollout status deployment connect-blue-connect -n kafka --timeout=120s

# 5. Blue가 여전히 PAUSED인지 확인
kubectl get kafkaconnector bg-sink-blue -n kafka \
  -o jsonpath='{.status.connectorStatus.connector.state}'
# PAUSED (config topic에서 복원됨)
```

---

## 추가 검증: REST API vs CRD 충돌

### 목적
CRD에 `state: running`으로 설정된 상태에서 REST API로 pause를 호출하면, Strimzi Operator가 이를 덮어쓰는지 확인한다.

```bash
# 1. CRD에 state: running 확인
kubectl get kafkaconnector bg-sink-blue -n kafka -o jsonpath='{.spec.state}'
# running

# 2. REST API로 직접 pause
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  curl -s -X PUT http://connect-blue-connect-api:8083/connectors/bg-sink-blue/pause

# 3. 즉시 상태 확인
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  curl -s http://connect-blue-connect-api:8083/connectors/bg-sink-blue/status \
  | jq '.connector.state'
# "PAUSED" (REST API에 의해)

# 4. Strimzi reconcile 대기 (최대 120초)
echo "Waiting for Strimzi reconcile..."
sleep 150

# 5. 다시 상태 확인
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  curl -s http://connect-blue-connect-api:8083/connectors/bg-sink-blue/status \
  | jq '.connector.state'
# "RUNNING" (Strimzi가 CRD의 running 상태로 복원할 것으로 예상)
```

**예상 결과:** Strimzi Operator가 CRD의 `state: running`을 기준으로 REST API의 PAUSED를 RUNNING으로 복원한다. → **Strimzi 환경에서는 반드시 CRD를 통해 제어해야 한다.**

---

## 결과 요약 템플릿

```markdown
| 시나리오 | 전환 시간 (CRD) | 전환 시간 (REST) | 유실 | 중복 | 결과 |
|----------|-----------------|------------------|------|------|------|
| 1. 정상 전환 | _초 | _초 | _건 | _건 | PASS/FAIL |
| 2. 즉시 롤백 | _초 | - | _건 | _건 | PASS/FAIL |
| config topic 영속성 | - | - | - | - | PASS/FAIL |
| REST vs CRD 충돌 | - | - | - | - | 관찰 기록 |
```

## 다음 단계

- [09-cleanup.md](09-cleanup.md): 테스트 환경 정리
