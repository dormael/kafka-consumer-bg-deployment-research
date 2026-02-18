# Tutorial 17: 시나리오 4 — 전환 중 Rebalance 장애 주입 (전략 C 전용)

> 전환 도중 Rebalance를 유발하여 pause 상태 유지, 양쪽 동시 Active 방지, Static Membership 동작을 검증합니다.

---

## 시나리오 4A: 전환 중 Rebalance 유발

### Step 1: 전환 시작

```bash
curl -X POST http://localhost:8090/switch &
# 백그라운드에서 전환 실행
```

### Step 2: 전환 진행 중 Pod 강제 삭제

Blue가 DRAINING 상태일 때 Pod 1개를 삭제합니다.

```bash
# Blue의 현재 상태 확인 (DRAINING인지 확인)
kubectl exec -n kafka consumer-blue-0 -c consumer -- \
  curl -s http://localhost:8080/lifecycle/status | jq .state

# Pod 강제 삭제
kubectl delete pod consumer-blue-2 -n kafka --force --grace-period=0
```

### Step 3: Rebalance 확인

```bash
# Kafka Broker 로그에서 JoinGroup 확인
kubectl logs -n kafka bg-test-cluster-kafka-0 | grep -i "joingroup\|syncgroup\|rebalance" | tail -20

# Consumer Rebalance 지표 확인
curl -s "http://localhost:9090/api/v1/query?query=bg_consumer_rebalance_count_total" | jq .
```

### Step 4: Pause 상태 유지 확인

재시작된 Pod(consumer-blue-2)가 PAUSED 상태로 올라오는지 확인합니다.

```bash
# 재시작 대기
kubectl wait --for=condition=ready pod/consumer-blue-2 -n kafka --timeout=60s

# 상태 확인
kubectl exec -n kafka consumer-blue-2 -c consumer -- \
  curl -s http://localhost:8080/lifecycle/status | jq .state
# "PAUSED"
```

### Step 5: 양쪽 동시 Active 확인

```bash
# DualActiveConsumers 알람 확인
curl -s "http://localhost:9090/api/v1/query?query=bg_dual_active_events_total" | jq '.data.result[0].value[1]'
# "0"
```

---

## 시나리오 4B: Static Membership 검증

### session.timeout.ms 이내 복귀 (Rebalance 미발생 확인)

```bash
# 1. 현재 Rebalance 카운트 기록
BEFORE=$(curl -s "http://localhost:9090/api/v1/query?query=bg_consumer_rebalance_count_total" | jq -r '.data.result[0].value[1]')
echo "Rebalance count before: $BEFORE"

# 2. Pod 삭제 (graceful, StatefulSet이 즉시 재생성)
kubectl delete pod consumer-blue-1 -n kafka

# 3. Pod 재생성 대기 (session.timeout.ms=45초 이내)
kubectl wait --for=condition=ready pod/consumer-blue-1 -n kafka --timeout=40s

# 4. Rebalance 카운트 비교
AFTER=$(curl -s "http://localhost:9090/api/v1/query?query=bg_consumer_rebalance_count_total" | jq -r '.data.result[0].value[1]')
echo "Rebalance count after: $AFTER"
echo "Rebalance occurred: $((AFTER > BEFORE))"  # 0이면 미발생 확인
```

### session.timeout.ms 초과 (Rebalance 발생 확인)

```bash
# session.timeout.ms를 10초로 낮춘 환경에서 테스트
# Pod 삭제 후 의도적으로 10초 이상 대기

# 1. Rebalance 카운트 기록
# 2. Pod 삭제
kubectl delete pod consumer-blue-1 -n kafka

# 3. 15초 대기 (session.timeout.ms=10초 초과)
sleep 15

# 4. Pod Ready 대기
kubectl wait --for=condition=ready pod/consumer-blue-1 -n kafka --timeout=60s

# 5. Rebalance 발생 확인
# 6. 재할당 후 pause 상태 유지 확인
kubectl exec -n kafka consumer-blue-1 -c consumer -- \
  curl -s http://localhost:8080/lifecycle/status | jq .state
# "PAUSED" (BgRebalanceListener가 re-pause 수행)
```

---

## 결과 기록

| 항목 | 결과 |
|------|------|
| 4A: Rebalance 후 pause 유지 | |
| 4A: 양쪽 동시 Active | 0회 / 발생 |
| 4B: session.timeout 이내 Rebalance 미발생 | Yes / No |
| 4B: session.timeout 초과 Rebalance 발생 + pause 유지 | Yes / No |

---

## 다음 단계

- [Tutorial 18: 시나리오 5 — 자동 롤백](18-scenario5-auto-rollback.md)
