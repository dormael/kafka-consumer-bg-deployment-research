# Tutorial 14: 시나리오 1 — 정상 Blue → Green 전환 테스트

> 이 튜토리얼은 각 전략(C, B, E)별로 정상적인 Blue → Green 전환을 수행하는 방법을 단계별로 안내합니다.

---

## 사전 요구사항

- Phase 1 (인프라) 완료: Prometheus, Loki, Kafka, Argo Rollouts, KafkaConnect
- Phase 2 (앱) 완료: Producer, Consumer, Switch Sidecar, Switch Controller, Validator 배포

---

## 공통 사전 준비

### 1. Producer 시작

```bash
# Producer가 TPS 100으로 메시지 생성 중인지 확인
kubectl port-forward svc/bg-test-producer -n kafka 8080:8080
curl http://localhost:8080/producer/status
# {"enabled":true,"ratePerSecond":100,"currentSequence":...}

# 시작 안 되어 있으면:
curl -X POST http://localhost:8080/producer/start
curl -X PUT "http://localhost:8080/producer/rate?ratePerSecond=100"
```

### 2. Blue Consumer Lag 확인

```bash
kubectl run kafka-check -n kafka --rm -it \
  --image=quay.io/strimzi/kafka:latest-kafka-3.8.0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server bg-test-cluster-kafka-bootstrap:9092 \
  --group bg-test-consumer-group --describe

# LAG 컬럼이 모두 0인지 확인
```

### 3. Grafana 대시보드 열기

```bash
kubectl port-forward svc/kube-prometheus-grafana -n monitoring 3000:80
# http://localhost:3000 → "Kafka BG Test" 대시보드 열기
```

### 4. Chaos 설정 초기화

```bash
# Blue Consumer의 모든 chaos 설정 리셋
for i in 0 1 2 3; do
  kubectl exec -n kafka consumer-blue-$i -c consumer -- \
    curl -s -X POST http://localhost:8080/chaos/reset
done
```

---

## 시나리오 1A: 전략 C (Pause/Resume Atomic Switch)

### Step 1: Blue 상태 확인

```bash
for i in 0 1 2 3; do
  echo "--- consumer-blue-$i ---"
  kubectl exec -n kafka consumer-blue-$i -c consumer -- \
    curl -s http://localhost:8080/lifecycle/status | jq .
done
# 모든 Pod: {"state":"ACTIVE","assignedPartitions":[...],"pausedPartitions":[]}
```

### Step 2: Green Consumer 배포

```bash
# Green StatefulSet 스케일 업 (초기 상태: PAUSED)
kubectl scale statefulset consumer-green -n kafka --replicas=4

# 모든 Pod Ready 대기
kubectl rollout status statefulset/consumer-green -n kafka --timeout=120s
```

Green Consumer 상태 확인:

```bash
for i in 0 1 2 3; do
  echo "--- consumer-green-$i ---"
  kubectl exec -n kafka consumer-green-$i -c consumer -- \
    curl -s http://localhost:8080/lifecycle/status | jq .
done
# 모든 Pod: {"state":"PAUSED",...}
```

### Step 3: 전환 시작 (시각 기록)

```bash
echo "전환 시작: $(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)"

# Switch Controller에 전환 요청
kubectl port-forward svc/bg-switch-controller -n kafka 8090:8090
curl -X POST http://localhost:8090/switch

echo "전환 완료: $(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)"
```

### Step 4: 전환 결과 확인

```bash
# Green Consumer 상태 확인
for i in 0 1 2 3; do
  kubectl exec -n kafka consumer-green-$i -c consumer -- \
    curl -s http://localhost:8080/lifecycle/status | jq .state
done
# 모든 Pod: "ACTIVE"

# Blue Consumer 상태 확인
for i in 0 1 2 3; do
  kubectl exec -n kafka consumer-blue-$i -c consumer -- \
    curl -s http://localhost:8080/lifecycle/status | jq .state
done
# 모든 Pod: "PAUSED"
```

### Step 5: Green Consumer Lag 모니터링

```bash
# 2분간 5초 간격으로 Lag 확인
for i in $(seq 1 24); do
  echo "--- $(date) ---"
  kubectl run kafka-check-$i -n kafka --rm -it --restart=Never \
    --image=quay.io/strimzi/kafka:latest-kafka-3.8.0 -- \
    bin/kafka-consumer-groups.sh --bootstrap-server bg-test-cluster-kafka-bootstrap:9092 \
    --group bg-test-consumer-group --describe 2>/dev/null | grep bg-test-topic
  sleep 5
done
```

### Step 6: Validator 실행

```bash
# 전환 시작 ~ 전환 후 5분 구간의 유실/중복 검증
python tools/validator/validator.py \
  --loki-url http://localhost:3100 \
  --start "2026-02-18T10:00:00Z" \
  --end "2026-02-18T10:10:00Z" \
  --consumer-color green \
  --scenario "S1A-strategy-C" \
  --output report/scenario-1A-strategy-C.md
```

### Step 7: Grafana 스크린샷 수집

아래 시점의 스크린샷을 저장합니다:
1. 전환 직전 (Blue ACTIVE, Lag=0)
2. 전환 중 (Blue DRAINING → PAUSED)
3. 전환 직후 (Green ACTIVE)
4. 5분 후 안정 상태

### Step 8: Blue Scale Down

```bash
kubectl scale statefulset consumer-blue -n kafka --replicas=0
```

### 결과 기록

| 항목 | 측정값 | 기준값 | 통과 여부 |
|------|--------|--------|-----------|
| 전환 소요 시간 | ___초 | < 5초 | |
| 메시지 유실 | ___건 | 0건 | |
| 메시지 중복 | ___건 | 측정 | |
| Green Lag 회복 시간 | ___초 | < 120초 | |
| Rebalance 횟수 | ___회 | 0회 | |

---

## 시나리오 1B: 전략 B (별도 Consumer Group + Offset 동기화)

### 사전 준비: Consumer Group ID 변경

전략 B는 Blue/Green이 별도 Consumer Group을 사용합니다. 환경변수를 변경하여 재배포합니다.

```bash
# Blue: bg-test-consumer-blue, Green: bg-test-consumer-green
# StatefulSet 환경변수 업데이트 필요
```

### Step 1: Blue Consumer (bg-test-consumer-blue) Lag = 0 확인

### Step 2: Green Deployment 배포 (replicas=4, group: bg-test-consumer-green)

### Step 3: Blue Consumer Scale Down 또는 Pause

### Step 4: Offset 동기화

```bash
# Blue의 현재 offset으로 Green의 offset 재설정
kubectl run offset-sync -n kafka --rm -it \
  --image=quay.io/strimzi/kafka:latest-kafka-3.8.0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server bg-test-cluster-kafka-bootstrap:9092 \
  --group bg-test-consumer-green \
  --topic bg-test-topic \
  --reset-offsets --to-current --execute
```

### Step 5: Green 활성화 (ConfigMap 업데이트)

### Step 6: Validator 실행 및 결과 기록

---

## 시나리오 1C: 전략 E (Kafka Connect CRD)

### Step 1: Blue Connector (file-sink-blue) RUNNING 확인

```bash
kubectl get kafkaconnector file-sink-blue -n kafka -o jsonpath='{.status.connectorStatus.connector.state}'
# RUNNING
```

### Step 2: 전환 (CRD patch)

```bash
echo "전환 시작: $(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)"

kubectl patch kafkaconnector file-sink-blue -n kafka --type merge -p '{"spec":{"state":"stopped"}}'
kubectl patch kafkaconnector file-sink-green -n kafka --type merge -p '{"spec":{"state":"running"}}'

# Green RUNNING 확인
kubectl get kafkaconnector file-sink-green -n kafka -o jsonpath='{.status.connectorStatus.connector.state}' -w

echo "전환 완료: $(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)"
```

### Step 3: 결과 확인 및 기록

---

## 환경 초기화 (다음 시나리오 전)

```bash
# Green Consumer 상태를 PAUSED로 복원
# Blue Consumer 스케일 업 및 ACTIVE로 복원
# Consumer Lag = 0 대기
# Chaos 설정 리셋
```

---

## 다음 단계

- [Tutorial 15: 시나리오 2 — 즉시 롤백](15-scenario2-immediate-rollback.md)
