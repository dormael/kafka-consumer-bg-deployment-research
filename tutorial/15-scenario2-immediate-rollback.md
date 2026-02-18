# Tutorial 15: 시나리오 2 — 전환 직후 즉시 롤백

> Blue → Green 전환 직후 즉시 롤백을 수행하여 롤백 속도와 메시지 안전성을 검증합니다.

---

## 전략 C 수행 절차

### Step 1: 정상 전환 수행

시나리오 1A와 동일한 절차로 Blue → Green 전환을 수행합니다.

### Step 2: 즉시 롤백 (Green ACTIVE 확인 후 3초 이내)

```bash
echo "롤백 시작: $(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)"

curl -X POST http://localhost:8090/rollback

echo "롤백 완료: $(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)"
```

### Step 3: Blue ACTIVE 재확인

```bash
for i in 0 1 2 3; do
  kubectl exec -n kafka consumer-blue-$i -c consumer -- \
    curl -s http://localhost:8080/lifecycle/status | jq .state
done
# 모든 Pod: "ACTIVE"
```

### Step 4: Consumer Lag 확인 (< 100 유지)

```bash
kubectl run kafka-check -n kafka --rm -it \
  --image=quay.io/strimzi/kafka:latest-kafka-3.8.0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server bg-test-cluster-kafka-bootstrap:9092 \
  --group bg-test-consumer-group --describe
```

### Step 5: Validator 실행

전환~롤백 구간의 메시지 유실/중복을 검증합니다.

```bash
python tools/validator/validator.py \
  --loki-url http://localhost:3100 \
  --start "<전환시작시각>" \
  --end "<롤백완료+5분>" \
  --consumer-color blue \
  --scenario "S2A-strategy-C-rollback" \
  --output report/scenario-2A-strategy-C.md
```

### 결과 기록

| 항목 | 측정값 | 기준값 | 통과 여부 |
|------|--------|--------|-----------|
| 롤백 소요 시간 | ___초 | < 5초 | |
| Blue 재개 후 Lag | ___ | < 100 | |
| 전환~롤백 구간 메시지 유실 | ___건 | 0건 | |

---

## 전략 B, E도 동일한 흐름으로 수행

- 전략 B: Offset 재동기화 포함
- 전략 E: `kubectl patch kafkaconnector` 방향 반대로

---

## 다음 단계

- [Tutorial 16: 시나리오 3 — Lag 발생 중 전환](16-scenario3-lag-during-switch.md)
