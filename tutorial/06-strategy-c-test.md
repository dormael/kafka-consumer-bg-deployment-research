# Tutorial 06: 전략 C 테스트 수행 (Pause/Resume Atomic Switch)

> **관련 태스크:** plan/task05.md
> **우선순위:** 1순위

---

## 사전 조건 확인

모든 항목을 확인한 후 테스트를 시작한다.

```bash
# 1. Producer 동작 확인 (TPS 100)
kubectl exec -n kafka-bg-test deploy/bg-producer -- \
  curl -s http://localhost:8080/producer/stats
# messagesPerSecond: 100 확인

# 2. Blue Consumer 상태: ACTIVE, Lag = 0
kubectl exec -n kafka-bg-test consumer-blue-0 -c consumer -- \
  curl -s http://localhost:8080/lifecycle/status
# state: ACTIVE

kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group --describe
# LAG 열이 모두 0

# 3. Green Consumer 상태: PAUSED
kubectl exec -n kafka-bg-test consumer-green-0 -c consumer -- \
  curl -s http://localhost:8080/lifecycle/status
# state: PAUSED

# 4. Switch Controller 동작 확인
kubectl get pods -n kafka-bg-test -l app=switch-controller
# Running

# 5. Grafana 대시보드 열기
echo "Grafana: http://localhost:3000 (admin/admin)"
```

---

## 시나리오 1: 정상 Blue → Green 전환

### 수행

```bash
# 현재 시간 기록 (전환 시작 시각)
SWITCH_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Switch start: $SWITCH_START"

# ConfigMap 업데이트로 전환 트리거
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"green","switch-timestamp":"'$SWITCH_START'"}}'

# Switch Controller 로그에서 전환 과정 관찰
kubectl logs -n kafka-bg-test -l app=switch-controller -f --tail=1
```

### 모니터링

**Grafana 대시보드에서 관찰할 항목:**
1. Blue Consumer의 Lifecycle 상태: ACTIVE → DRAINING → PAUSED
2. Green Consumer의 Lifecycle 상태: PAUSED → ACTIVE
3. Consumer Lag 변화: 전환 중 일시적 Lag 증가 → 회복
4. Messages/sec: Blue 0으로 감소, Green 증가

### 전환 완료 확인

```bash
# Green 상태 확인
kubectl exec -n kafka-bg-test consumer-green-0 -c consumer -- \
  curl -s http://localhost:8080/lifecycle/status
# state: ACTIVE

# Blue 상태 확인
kubectl exec -n kafka-bg-test consumer-blue-0 -c consumer -- \
  curl -s http://localhost:8080/lifecycle/status
# state: PAUSED

# 전환 완료 시각 기록
SWITCH_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Switch end: $SWITCH_END"
echo "Duration: $(( $(date -d $SWITCH_END +%s) - $(date -d $SWITCH_START +%s) )) seconds"
```

### 안정화 대기 (2분)

```bash
# 2분 후 Green Consumer Lag 확인
sleep 120
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group --describe
# LAG < 100 확인
```

### 유실/중복 검증

```bash
# 테스트 종료 시각
TEST_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)

# Validator 실행
python tools/validator/validator.py \
  --source loki \
  --loki-url http://localhost:3100 \
  --start "$SWITCH_START" \
  --end "$TEST_END" \
  --switch-start "$SWITCH_START" \
  --switch-end "$SWITCH_END" \
  --strategy C \
  --output report/validation-c-scenario1.md
```

### Grafana 스크린샷 저장

- Consumer Lag 시계열 → `report/screenshots/c-scenario1-lag.png`
- Lifecycle 상태 → `report/screenshots/c-scenario1-lifecycle.png`
- Messages/sec → `report/screenshots/c-scenario1-tps.png`

---

## 시나리오 2: 전환 직후 즉시 롤백

### 사전 준비

먼저 Green이 ACTIVE 상태인지 확인 (시나리오 1 이후 상태).
Blue/Green을 초기 상태로 되돌린다:

```bash
# Blue → ACTIVE, Green → PAUSED로 복원
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"blue"}}'

# 안정화 대기
sleep 30
```

### 수행

```bash
# 1. Blue → Green 전환
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"green"}}'

# 2. Green ACTIVE 확인 즉시 롤백 (5초 이내)
sleep 3

# 3. 롤백 트리거
ROLLBACK_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"blue"}}'

# 4. Blue ACTIVE 재확인
sleep 5
kubectl exec -n kafka-bg-test consumer-blue-0 -c consumer -- \
  curl -s http://localhost:8080/lifecycle/status
```

### 검증

- 롤백 완료 시간 < 5초
- Blue 재개 후 Consumer Lag < 100

---

## 시나리오 3: Consumer Lag 발생 중 전환

### 사전 준비

```bash
# Blue ACTIVE 상태 확인
# 처리 지연 주입 (200ms per message → Lag > 500 유발)
for pod in consumer-blue-0 consumer-blue-1 consumer-blue-2; do
  kubectl exec -n kafka-bg-test $pod -c consumer -- \
    curl -s -X PUT http://localhost:8080/fault/processing-delay \
      -H "Content-Type: application/json" -d '{"delayMs": 200}'
done

# Lag 증가 대기 (약 2분)
echo "Waiting for lag to build..."
sleep 120

# Lag 확인 (> 500)
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group --describe
```

### 수행

```bash
# Lag > 500 상태에서 전환
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"green"}}'

# Switch Controller 로그에서 Lag 대기/즉시 전환 정책 관찰
kubectl logs -n kafka-bg-test -l app=switch-controller -f --tail=1
```

### 장애 주입 해제

```bash
for pod in consumer-blue-0 consumer-blue-1 consumer-blue-2; do
  kubectl exec -n kafka-bg-test $pod -c consumer -- \
    curl -s -X PUT http://localhost:8080/fault/processing-delay \
      -H "Content-Type: application/json" -d '{"delayMs": 0}'
done
```

---

## 시나리오 4: Rebalance 장애 주입

### 수행

```bash
# Blue ACTIVE 상태에서 시작
# 전환 시작
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"green"}}'

# 전환 도중 (1초 후) Blue Pod 강제 삭제 → Rebalance 유발
sleep 1
kubectl delete pod consumer-blue-0 -n kafka-bg-test

# Rebalance 후 pause 상태 유지 확인
sleep 30
kubectl exec -n kafka-bg-test consumer-blue-0 -c consumer -- \
  curl -s http://localhost:8080/lifecycle/status
# state: PAUSED (Rebalance 후에도 유지되어야 함)
```

### Static Membership 검증

```bash
# 1. group.instance.id 적용 확인
kubectl exec -n kafka-bg-test consumer-blue-0 -c consumer -- \
  curl -s http://localhost:8080/lifecycle/status | jq '.groupInstanceId'

# 2. Pod 삭제 후 session.timeout.ms(45초) 이내 복귀
kubectl delete pod consumer-blue-1 -n kafka-bg-test
# Pod가 자동 재생성되므로 45초 이내 복귀

# 3. Rebalance 발생 여부 확인
# Prometheus: bg_consumer_rebalance_count_total{pod="consumer-blue-1"}
# Kafka Broker 로그:
kubectl logs -n kafka kafka-cluster-dual-role-0 | grep -E "JoinGroup|SyncGroup" | tail -5

# 4. session.timeout.ms 초과 테스트 (45초)
kubectl scale statefulset consumer-blue -n kafka-bg-test --replicas=2
sleep 50  # 45초 초과 대기
# 이 시점에서 Rebalance 발생해야 함
kubectl scale statefulset consumer-blue -n kafka-bg-test --replicas=3
```

---

## 시나리오 5: 자동 롤백

### 사전 준비

```bash
# Green Consumer에 높은 에러율 주입
for pod in consumer-green-0 consumer-green-1 consumer-green-2; do
  kubectl exec -n kafka-bg-test $pod -c consumer -- \
    curl -s -X PUT http://localhost:8080/fault/error-rate \
      -H "Content-Type: application/json" -d '{"errorRatePercent": 80}'
done
```

### 수행

```bash
# Blue → Green 전환
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"green"}}'

# Switch Controller가 Green 헬스체크 실패 감지 → 자동 롤백 관찰
kubectl logs -n kafka-bg-test -l app=switch-controller -f --tail=1

# 약 30초 후 Blue ACTIVE 복원 확인
sleep 30
kubectl exec -n kafka-bg-test consumer-blue-0 -c consumer -- \
  curl -s http://localhost:8080/lifecycle/status
```

### 장애 주입 해제

```bash
for pod in consumer-green-0 consumer-green-1 consumer-green-2; do
  kubectl exec -n kafka-bg-test $pod -c consumer -- \
    curl -s -X PUT http://localhost:8080/fault/error-rate \
      -H "Content-Type: application/json" -d '{"errorRatePercent": 0}'
done
```

---

## 결과 요약 템플릿

각 시나리오 수행 후 아래 표를 채워 `report/` 디렉토리에 저장한다.

```markdown
| 시나리오 | 전환 시간 | 롤백 시간 | 유실 | 중복 | 동시 Active | 결과 |
|----------|-----------|-----------|------|------|-------------|------|
| 1. 정상 전환 | _초 | - | _건 | _건 | 0회 | PASS/FAIL |
| 2. 즉시 롤백 | _초 | _초 | _건 | _건 | 0회 | PASS/FAIL |
| 3. Lag 중 전환 | _초 | - | _건 | _건 | 0회 | PASS/FAIL |
| 4. Rebalance 장애 | _초 | - | _건 | _건 | _회 | PASS/FAIL |
| 5. 자동 롤백 | _초 | _초 | _건 | _건 | 0회 | PASS/FAIL |
```

## 다음 단계

- [07-strategy-b-test.md](07-strategy-b-test.md): 전략 B 테스트 수행
