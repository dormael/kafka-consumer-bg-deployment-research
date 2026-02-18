# Tutorial 16: 시나리오 3 — Consumer Lag 발생 중 전환

> Consumer Lag가 높은 상태에서 전환을 시도하여 전략별 Lag 처리 방식의 차이를 관찰합니다.

---

## Step 1: Lag 유발

Blue Consumer에 처리 지연을 주입합니다.

```bash
# Blue Consumer의 모든 Pod에 처리 지연 500ms 설정
for i in 0 1 2 3; do
  kubectl exec -n kafka consumer-blue-$i -c consumer -- \
    curl -s -X PUT "http://localhost:8080/chaos/processing-delay?delayMs=500"
done
```

## Step 2: Lag > 500 도달 대기

```bash
# 5초 간격으로 Lag 모니터링
watch -n 5 "kubectl run lag-check -n kafka --rm -it --restart=Never \
  --image=quay.io/strimzi/kafka:latest-kafka-3.8.0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server bg-test-cluster-kafka-bootstrap:9092 \
  --group bg-test-consumer-group --describe 2>/dev/null | tail -10"
```

Producer TPS 100, Consumer TPS ~2(500ms 지연)이므로 약 5~10초 후 Lag > 500 도달.

## Step 3: Lag가 높은 상태에서 전환 시도

```bash
echo "전환 시작 (Lag 높은 상태): $(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)"

# 전략 C
curl -X POST http://localhost:8090/switch

echo "전환 완료: $(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)"
```

## Step 4: 동작 관찰

Switch Controller의 `drainTimeoutSeconds` 설정에 따라 동작이 달라집니다:

- **대기 모드**: Blue Lag=0이 될 때까지 대기 후 pause → 전환 시간 증가
- **즉시 모드**: Lag 무관 즉시 pause → 빠르지만 in-flight 메시지 중복 가능

## Step 5: Green의 Chaos 리셋 및 Lag 소진 확인

```bash
# Green Consumer의 chaos 설정이 없는지 확인
for i in 0 1 2 3; do
  kubectl exec -n kafka consumer-green-$i -c consumer -- \
    curl -s http://localhost:8080/chaos/status
done

# Lag 소진 모니터링
watch -n 5 "kubectl run lag-check -n kafka --rm -it --restart=Never \
  --image=quay.io/strimzi/kafka:latest-kafka-3.8.0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server bg-test-cluster-kafka-bootstrap:9092 \
  --group bg-test-consumer-group --describe 2>/dev/null | tail -10"
```

## Step 6: Validator 실행 및 결과 기록

```bash
python tools/validator/validator.py \
  --loki-url http://localhost:3100 \
  --start "<lag유발시각>" \
  --end "<lag소진완료시각>" \
  --consumer-color green \
  --scenario "S3-strategy-C-lag" \
  --output report/scenario-3-strategy-C.md
```

---

## 다음 단계

- [Tutorial 17: 시나리오 4 — Rebalance 장애 주입](17-scenario4-rebalance-injection.md)
