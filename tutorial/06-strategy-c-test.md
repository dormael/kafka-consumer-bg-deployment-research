# Tutorial 06: 전략 C 테스트 수행 (Pause/Resume Atomic Switch)

> **관련 태스크:** plan/task05.md
> **우선순위:** 1순위
> **최종 수정:** 2026-02-22 (4-레이어 안전망 아키텍처 개선 반영)

---

## 공통 참고사항

### HTTP 클라이언트

Sidecar Container에 `curl`이 포함되어 GET/PUT/POST 모두 사용 가능하다.

| 용도 | 방법 |
|------|------|
| **GET 요청** (상태 확인) | Sidecar Container의 `curl -s` 사용 |
| **PUT 요청** (장애 주입) | Sidecar Container의 `curl -s -X PUT` 사용 |
| **POST 요청** (lifecycle 제어) | Sidecar Container의 `curl -s -X POST` 사용 |
| **Kafka Consumer Group 조회** | Kafka broker Pod에서 `kafka-consumer-groups.sh` 실행 |

> **주의:** Consumer Pod는 2개의 Container(`consumer`, `switch-sidecar`)를 포함한다.
> `curl`은 `switch-sidecar` Container에서 실행한다 (같은 Pod이므로 `localhost:8080`으로 consumer에 접근 가능).
>
> **[개선 사항]** 이전에는 Sidecar Container에 `curl`이 없어 PUT 요청 시 `port-forward`가
> 필요했으나, Sidecar Dockerfile에 `curl`이 추가되어 `kubectl exec -c switch-sidecar -- curl`로
> 모든 HTTP 메서드를 사용할 수 있다.

### 헬퍼 함수 (선택사항)

테스트 중 반복되는 명령을 단축하려면 아래 함수를 쉘에 등록한다:

```bash
# Consumer lifecycle 상태 확인 (GET) — curl 사용
check_status() {
  local pod=$1
  kubectl exec -n kafka-bg-test "$pod" -c switch-sidecar -- \
    curl -s http://localhost:8080/lifecycle/status 2>/dev/null
}

# 모든 Consumer 상태 일괄 확인
check_all() {
  echo "=== Blue ==="
  for pod in consumer-blue-0 consumer-blue-1 consumer-blue-2; do
    echo -n "$pod: "; check_status "$pod"
  done
  echo "=== Green ==="
  for pod in consumer-green-0 consumer-green-1 consumer-green-2; do
    echo -n "$pod: "; check_status "$pod"
  done
}

# Consumer Lag 확인
check_lag() {
  kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
    bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
      --group bg-test-group --describe
}

# Switch Controller 로그 확인 (최근 N줄)
sc_logs() {
  local lines=${1:-20}
  kubectl logs -n kafka-bg-test -l app=bg-switch-controller --tail="$lines"
}

# ConfigMap 현재 상태 확인 (전환 트리거용)
check_active() {
  kubectl get configmap kafka-consumer-active-version -n kafka-bg-test \
    -o jsonpath='{.data.active}' && echo ""
}

# [신규] Desired State ConfigMap 확인 (Pod별 desired state)
check_desired_state() {
  echo "=== Desired State ConfigMap ==="
  kubectl get configmap kafka-consumer-state -n kafka-bg-test -o jsonpath='{.data}' | python3 -m json.tool
}

# [신규] 긴급 수동 복구: Controller 없이 Sidecar에 직접 push
emergency_set_state() {
  local color=$1 lifecycle=$2  # e.g., "blue" "PAUSED"
  for i in 0 1 2; do
    kubectl exec -n kafka-bg-test consumer-${color}-$i -c switch-sidecar -- \
      curl -s -X POST http://localhost:8082/desired-state \
      -H "Content-Type: application/json" \
      -d "{\"lifecycle\":\"${lifecycle}\"}"
    echo "  consumer-${color}-$i -> ${lifecycle}"
  done
}
```

### 환경 정보

| 항목 | 값 |
|------|-----|
| Grafana | `http://192.168.58.2:30080` (admin / admin123) |
| Loki | ClusterIP `svc/loki:3100` (monitoring 네임스페이스) |
| Kafka Broker Pod | `kafka-cluster-dual-role-0` (kafka 네임스페이스) |
| Consumer Group | `bg-test-group` |
| Topic | `bg-test-topic` (8 partitions) |
| Switch Controller 라벨 | `app=bg-switch-controller` |
| Desired State ConfigMap | `kafka-consumer-state` (Pod별 desired lifecycle state) |
| Active Version ConfigMap | `kafka-consumer-active-version` (전환 트리거) |

---

## 아키텍처 개요: 4-레이어 안전망

> **[개선 사항]** Task 05 테스트에서 발견된 P0(Dual-Active), P1(Sidecar 재시도 실패) 버그를 해결하기 위해
> 아키텍처가 다음과 같이 개선되었다.

### 핵심 변경 사항

1. **Consumer 기본 PAUSED 시작**: `INITIAL_STATE` env var 제거, application.yaml에서 기본값을 PAUSED로 변경
2. **`kafka-consumer-state` ConfigMap 도입**: Pod hostname별 desired state를 JSON으로 관리
3. **Sidecar Reconciler**: K8s API Watch를 제거하고 File Polling + HTTP Push 수신 패턴으로 재작성
4. **Controller ConfigMap 선기록**: 전환 시 `kafka-consumer-state` ConfigMap에 먼저 desired state 기록

### 4-레이어 안전망 구조

```
                          Controller
                        /     |     \
               (1) ConfigMap  (2) Consumer   (3) Sidecar HTTP
                   Write       HTTP 직접      push desired state
                     |         pause/resume       |
              kafka-consumer-     |          Sidecar 메모리 캐시
              state (영속)    Consumer가        |
                     |        즉시 전환     (4) Reconcile loop
              kubelet sync                  desired vs actual 비교
              (60-90초)                     불일치 시 Consumer 전환
                     |
              Volume Mount 파일 갱신
              Sidecar File Polling
              (최후의 fallback)
```

| 레이어 | 역할 | 지연 | 실패 조건 |
|--------|------|------|-----------|
| L1: Controller → Consumer HTTP | 즉시 전환 | ~1초 | Controller 다운 |
| L2: Controller → Sidecar HTTP push | Sidecar 캐시 갱신 | ~1초 | Controller 다운 |
| L3: Sidecar Reconcile Loop | 캐시 vs actual 비교 | 5초 주기 | Sidecar 재시작 (캐시 소실) |
| L4: Volume Mount File Polling | 파일에서 desired state 읽기 | 60-90초 | kubelet 장애 |

### P0 버그 해결 원리

이전 문제: PAUSED 측 Pod가 재시작하면 `INITIAL_STATE=ACTIVE` env var로 인해 Dual-Active 발생.

해결:
1. Consumer가 기본 **PAUSED**로 시작 (Dual-Active 원천 차단)
2. Sidecar가 메모리 캐시(L2) 또는 Volume Mount 파일(L4)에서 desired state를 읽음
3. Reconcile loop(L3)이 5초 주기로 desired vs actual을 비교하여 올바른 상태로 전환
4. Consumer 기동 전 Sidecar 명령이 실패해도 다음 주기에 자동 재시도 (P1 해결)

---

## 사전 조건 확인

모든 항목을 확인한 후 테스트를 시작한다.

```bash
# 1. Producer 동작 확인 (TPS 100)
kubectl exec -n kafka-bg-test deploy/bg-test-producer -- \
  curl -s http://localhost:8080/producer/stats
# 기대 결과: messagesPerSecond: 100
# running: true 확인

# 2. Blue Consumer 상태: ACTIVE
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  curl -s http://localhost:8080/lifecycle/status
# 기대 결과: {"state":"ACTIVE","stateCode":0,...}

# 3. Blue Consumer Lag = 0 확인
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group --describe
# 기대 결과: LAG 열이 모두 0 (또는 매우 작은 값)
# ※ PAUSED 측 파티션은 Lag이 누적될 수 있음 — "전략 C 구조적 특성" 참조

# 4. Green Consumer 상태: PAUSED
kubectl exec -n kafka-bg-test consumer-green-0 -c switch-sidecar -- \
  curl -s http://localhost:8080/lifecycle/status
# 기대 결과: {"state":"PAUSED","stateCode":2,...}

# 5. Switch Controller 동작 확인
kubectl get pods -n kafka-bg-test -l app=bg-switch-controller
# 기대 결과: 1/1 Running

# 6. ConfigMap 현재 상태 (전환 트리거용)
kubectl get configmap kafka-consumer-active-version -n kafka-bg-test \
  -o jsonpath='{.data.active}' && echo ""
# 기대 결과: blue

# 7. [신규] Desired State ConfigMap 확인
kubectl get configmap kafka-consumer-state -n kafka-bg-test \
  -o jsonpath='{.data}' | python3 -m json.tool
# 기대 결과: Blue Pod들은 {"lifecycle":"ACTIVE"}, Green Pod들은 {"lifecycle":"PAUSED"}

# 8. [신규] Volume Mount 파일 확인
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  cat /etc/consumer-state/consumer-blue-0
# 기대 결과: {"lifecycle":"ACTIVE"}

# 9. Grafana 대시보드 접근
echo "Grafana: http://192.168.58.2:30080 (admin/admin123)"
echo "대시보드: 'Kafka Consumer Blue/Green Deployment'"
```

---

## 전략 C 구조적 특성 (테스트 전 필독)

전략 C는 Blue/Green 양쪽이 **같은 Consumer Group**(`bg-test-group`)을 공유한다.

```
bg-test-group 내 6개 Consumer (Blue 3 + Green 3) → 8개 파티션 분배
  Blue-0: partition 0, 1       ← ACTIVE (소비 중)
  Blue-1: partition 2          ← ACTIVE (소비 중)
  Blue-2: partition 3          ← ACTIVE (소비 중)
  Green-0: partition 4, 5      ← PAUSED (미소비 → Lag 누적)
  Green-1: partition 6         ← PAUSED (미소비 → Lag 누적)
  Green-2: partition 7         ← PAUSED (미소비 → Lag 누적)
```

**결과:**
- ACTIVE 측은 자신에게 할당된 파티션(~4개)만 소비
- PAUSED 측에 할당된 파티션(~4개)은 **항상 Lag 누적** (정상 동작)
- Validator로 유실/중복 측정 시 **~50%가 "missing"으로 표시**되지만 이는 실제 유실이 아님
- 전환 시 역할만 교체되므로, 이전에 소비하던 파티션은 이제 PAUSED 측이 되어 다시 Lag 누적
- **이 한계는 전략 B(별도 Consumer Group)에서는 발생하지 않음**

---

## 시나리오 1: 정상 Blue → Green 전환

### 목표

Blue ACTIVE → Green ACTIVE 전환이 5초 이내에 완료되고, 양쪽 동시 Active가 발생하지 않는지 검증.

### 수행

```bash
# 1. 현재 시간 기록 (전환 시작 시각)
SWITCH_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Switch start: $SWITCH_START"

# 2. ConfigMap 업데이트로 전환 트리거
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"green"}}'
# 출력: configmap/kafka-consumer-active-version patched

# 3. Switch Controller 로그에서 전환 과정 실시간 관찰
# (별도 터미널에서 실행 권장, Ctrl+C로 종료)
kubectl logs -n kafka-bg-test -l app=bg-switch-controller -f --tail=1
```

**Switch Controller 로그에서 확인할 핵심 메시지:**

```
"message":"switch initiated","from":"blue","to":"green"
"message":"sending lifecycle command","command":"pause"        ← Blue pause
"message":"all pods reached target state","target":"PAUSED"    ← Blue PAUSED 확인
"message":"sending lifecycle command","command":"resume"       ← Green resume
"message":"all pods reached target state","target":"ACTIVE"    ← Green ACTIVE 확인
"message":"switch completed","duration_ms":NNNN               ← 전환 완료 (목표: <5000ms)
```

### 전환 완료 확인

```bash
# Green 상태 확인 — ACTIVE여야 함
kubectl exec -n kafka-bg-test consumer-green-0 -c switch-sidecar -- \
  curl -s http://localhost:8080/lifecycle/status
# 기대: {"state":"ACTIVE","stateCode":0,...}

# Blue 상태 확인 — PAUSED여야 함
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  curl -s http://localhost:8080/lifecycle/status
# 기대: {"state":"PAUSED","stateCode":2,...}

# [신규] Desired State ConfigMap 확인 — Controller가 선기록했는지 검증
kubectl get configmap kafka-consumer-state -n kafka-bg-test \
  -o jsonpath='{.data}' | python3 -m json.tool
# 기대: Blue Pod들은 {"lifecycle":"PAUSED"}, Green Pod들은 {"lifecycle":"ACTIVE"}

# 전환 완료 시각 기록
SWITCH_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Switch end: $SWITCH_END"
echo "Duration: $(( $(date -d "$SWITCH_END" +%s) - $(date -d "$SWITCH_START" +%s) )) seconds"
```

### 안정화 대기 (2분)

```bash
sleep 120

# Green Consumer Lag 확인
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group --describe
# 기대: Green에 할당된 파티션의 LAG ≈ 0
# ※ Blue에 할당된 파티션은 이제 PAUSED이므로 Lag 누적 (전략 C 구조적 특성)
```

### 유실/중복 검증 (Validator)

```bash
# 1. Loki port-forward 시작 (별도 터미널 또는 백그라운드)
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
  --strategy C \
  --output report/validation-c-scenario1.md

# 4. port-forward 종료
kill $LOKI_PF_PID 2>/dev/null
```

> **Validator 결과 해석 주의:**
> - `Missing (Loss): ~50%`는 PAUSED 측 파티션의 미소비 메시지이며 **실제 유실이 아님**
> - ACTIVE 측 파티션만 대상으로 유실이 0건인지 확인해야 함
> - `Duplicates`: at-least-once 보장에 의한 중복은 허용 범위

### Grafana 스크린샷 저장

브라우저에서 `http://192.168.58.2:30080` → "Kafka Consumer Blue/Green Deployment" 대시보드를 열어 스크린샷 저장:

- Consumer Lag 시계열 → `report/screenshots/c-scenario1-lag.png`
- Lifecycle 상태 → `report/screenshots/c-scenario1-lifecycle.png`
- Messages/sec → `report/screenshots/c-scenario1-tps.png`

### 검증 기준

| 항목 | 기준 | 통과 조건 |
|------|------|-----------|
| 전환 완료 시간 | < 5초 | 필수 |
| 메시지 유실 (ACTIVE 파티션) | 0건 | 필수 |
| Green Consumer Lag (전환 후 2분) | < 100 | 필수 |
| 양쪽 동시 Active | 0회 | 필수 |

---

## 시나리오 2: 전환 직후 즉시 롤백

### 목표

Blue→Green 전환 후 즉시 Green→Blue 롤백이 5초 이내에 완료되고, Blue가 정상 소비를 재개하는지 검증.

### 사전 준비

시나리오 1 이후 상태(Green=ACTIVE, Blue=PAUSED)에서 시작.
먼저 Blue→ACTIVE로 복원한다:

```bash
# Blue → ACTIVE, Green → PAUSED로 복원
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"blue"}}'

# Switch Controller가 전환을 완료할 때까지 대기
sleep 10

# 상태 확인
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  curl -s http://localhost:8080/lifecycle/status
# 기대: {"state":"ACTIVE",...}

# 추가 안정화 대기
sleep 20
```

### 수행

```bash
# 시작 시각 기록
SCENARIO2_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)

# 1. Blue → Green 전환
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"green"}}'

# 2. Switch Controller가 전환을 완료할 때까지 대기 (약 1~2초)
#    Green ACTIVE 확인 즉시 롤백 (5초 이내)
sleep 3

# 3. Green ACTIVE 상태 확인
kubectl exec -n kafka-bg-test consumer-green-0 -c switch-sidecar -- \
  curl -s http://localhost:8080/lifecycle/status
# 기대: {"state":"ACTIVE",...}

# 4. 롤백 트리거
ROLLBACK_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Rollback start: $ROLLBACK_START"

kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"blue"}}'

# 5. Switch Controller가 롤백을 완료할 때까지 대기
sleep 5

# 6. Blue ACTIVE 재확인
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  curl -s http://localhost:8080/lifecycle/status
# 기대: {"state":"ACTIVE",...}

ROLLBACK_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Rollback end: $ROLLBACK_END"
echo "Rollback duration: $(( $(date -d "$ROLLBACK_END" +%s) - $(date -d "$ROLLBACK_START" +%s) )) seconds"
```

### 검증

```bash
# Consumer Lag 확인
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group --describe
# 기대: Blue 할당 파티션 LAG < 100
```

| 항목 | 기준 |
|------|------|
| 롤백 완료 시간 | < 5초 |
| Blue 재개 후 Consumer Lag | < 100 유지 |
| 메시지 유실 | 0건 |

---

## 시나리오 3: Consumer Lag 발생 중 전환

### 목표

Blue Consumer에 처리 지연을 주입하여 Lag > 500을 유발한 상태에서 전환을 수행하고, Lag이 전환 시간에 영향을 주는지 검증.

### 사전 준비 — Blue ACTIVE 상태 확인

```bash
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  curl -s http://localhost:8080/lifecycle/status
# 기대: {"state":"ACTIVE",...}
```

### 처리 지연 주입

> **[개선 사항]** Sidecar Container에 `curl`이 포함되어 `port-forward` 없이 직접 장애 주입이 가능하다.

```bash
# 각 Blue Pod에 curl로 직접 장애 주입 (port-forward 불필요)
for i in 0 1 2; do
  echo "=== consumer-blue-$i ==="
  kubectl exec -n kafka-bg-test consumer-blue-$i -c switch-sidecar -- \
    curl -s -X PUT http://localhost:8080/fault/processing-delay \
      -H "Content-Type: application/json" -d '{"delayMs": 200}'
  echo ""
done
```

> **대안:** ConfigMap 기반 장애 주입도 가능하다. Sidecar Reconciler가 fault 필드를 감지하여 적용한다:
> ```bash
> kubectl patch configmap kafka-consumer-state -n kafka-bg-test --type merge -p '{
>   "data": {
>     "consumer-blue-0": "{\"lifecycle\":\"ACTIVE\",\"fault\":{\"processingDelayMs\":200}}",
>     "consumer-blue-1": "{\"lifecycle\":\"ACTIVE\",\"fault\":{\"processingDelayMs\":200}}",
>     "consumer-blue-2": "{\"lifecycle\":\"ACTIVE\",\"fault\":{\"processingDelayMs\":200}}"
>   }
> }'
> ```

### Lag 증가 대기 및 확인

```bash
# Lag 증가 대기 (약 2분)
echo "Waiting for lag to build (2 minutes)..."
sleep 120

# Lag 확인 (ACTIVE 파티션의 LAG > 500 확인)
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group --describe
# 기대: Blue 할당 파티션의 LAG > 500
```

### 수행

```bash
SCENARIO3_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Switch start (with lag): $SCENARIO3_START"

# Lag > 500 상태에서 전환
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"green"}}'

# Switch Controller 로그에서 전환 과정 관찰 (별도 터미널)
kubectl logs -n kafka-bg-test -l app=bg-switch-controller --tail=5

# 전환 완료 대기 (약 1~2초)
sleep 5

# Green ACTIVE 확인
kubectl exec -n kafka-bg-test consumer-green-0 -c switch-sidecar -- \
  curl -s http://localhost:8080/lifecycle/status
# 기대: {"state":"ACTIVE",...}

SCENARIO3_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Switch end: $SCENARIO3_END"
echo "Duration: $(( $(date -d "$SCENARIO3_END" +%s) - $(date -d "$SCENARIO3_START" +%s) )) seconds"
```

> **관찰 포인트:** Switch Controller는 현재 **Lag 확인 없이 즉시 전환**한다.
> Lag 소진 대기 모드는 미구현(P2 이슈)이므로, Lag이 있어도 전환 시간에 영향 없음.

### 장애 주입 해제

```bash
# Blue Consumer에 지연 해제 (port-forward 불필요, Sidecar curl 사용)
for i in 0 1 2; do
  kubectl exec -n kafka-bg-test consumer-blue-$i -c switch-sidecar -- \
    curl -s -X PUT http://localhost:8080/fault/processing-delay \
      -H "Content-Type: application/json" -d '{"delayMs": 0}'
  echo ""
done
```

### 검증 기준

| 항목 | 기준 |
|------|------|
| Lag 발생 중 전환 시간 | < 5초 (정상 시와 동일) |
| Lag 소진 대기 중 메시지 유실 | 0건 |
| 전략별 Lag 처리 방식 차이 | 측정 및 기록 |

---

## 시나리오 4: Rebalance 장애 주입 (전략 C 전용) — P0 해결 검증

### 목표

전환 직후 PAUSED 측 Pod를 강제 재시작하여 Rebalance를 유발하고:
1. **[핵심]** Rebalance 후에도 PAUSED 상태가 유지되는지 확인 (P0 해결 검증)
2. 양쪽 동시 Active(Dual-Active) 발생 여부를 모니터링
3. Static Membership 동작을 검증
4. 4-레이어 안전망의 동작을 확인

> **[개선 사항]** 이전에는 PAUSED 측 Pod 재시작 시 `INITIAL_STATE=ACTIVE` env var로 인해
> Dual-Active가 발생하는 P0 버그가 있었다. 아키텍처 개선 후:
> - Consumer 기본 PAUSED 시작 (Dual-Active 원천 차단)
> - Sidecar Reconciler가 5초 주기로 desired state 확인 후 올바른 상태로 전환
> - 이 시나리오에서 **Dual-Active가 발생하지 않아야** 한다.

### 사전 준비 — Blue ACTIVE로 복원

```bash
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"blue"}}'
sleep 10

kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  curl -s http://localhost:8080/lifecycle/status
# 기대: {"state":"ACTIVE",...}

sleep 20  # 안정화 대기
```

### 수행

```bash
SCENARIO4_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)

# 1. Blue → Green 전환
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"green"}}'

# 2. 전환 완료 대기 (약 1~2초)
sleep 3

# 3. Desired State ConfigMap 확인 — Controller가 선기록했는지 검증
kubectl get configmap kafka-consumer-state -n kafka-bg-test \
  -o jsonpath='{.data}' | python3 -m json.tool
# 기대: Blue Pod들은 {"lifecycle":"PAUSED"}, Green Pod들은 {"lifecycle":"ACTIVE"}

# 4. 전환 직후 Blue Pod 1개 강제 삭제 → Rebalance 유발
#    (Blue는 이제 PAUSED 상태여야 함)
kubectl delete pod consumer-blue-0 -n kafka-bg-test
echo "consumer-blue-0 deleted at $(date -u +%Y-%m-%dT%H:%M:%SZ)"

# 5. StatefulSet이 Pod를 재생성할 때까지 대기
echo "Waiting for pod recreation (30 seconds)..."
sleep 30
```

### 재생성 후 상태 확인

```bash
# Pod 재생성 확인
kubectl get pod consumer-blue-0 -n kafka-bg-test
# 기대: Running, READY 2/2

# ★ 핵심 확인: 재생성된 Blue-0의 lifecycle 상태
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  curl -s http://localhost:8080/lifecycle/status
# ★ 기대: PAUSED (ConfigMap의 active=green이므로)
# ★ 이전 결과(P0 버그): ACTIVE → Dual-Active 발생
# ★ 개선 후 기대: PAUSED → Dual-Active 미발생

# Volume Mount 파일 확인 — desired state가 PAUSED인지 검증
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  cat /etc/consumer-state/consumer-blue-0
# 기대: {"lifecycle":"PAUSED"}

# Sidecar Reconciler 로그 확인 — reconcile 성공 여부
kubectl logs -n kafka-bg-test consumer-blue-0 -c switch-sidecar --tail=10
# 기대: "reconcile succeeded" 또는 "desired state matches actual" 로그

# 양쪽 동시 Active 확인
echo "=== All consumer states ==="
for pod in consumer-blue-0 consumer-blue-1 consumer-blue-2 \
           consumer-green-0 consumer-green-1 consumer-green-2; do
  echo -n "$pod: "
  kubectl exec -n kafka-bg-test "$pod" -c switch-sidecar -- \
    curl -s http://localhost:8080/lifecycle/status 2>/dev/null
  echo ""
done
# 기대: Blue 전체 PAUSED, Green 전체 ACTIVE (Dual-Active 없음)
```

> **P0 해결 확인:** 재시작된 Blue-0이 PAUSED로 유지되면 P0 버그가 해결된 것이다.
>
> 해결 원리:
> 1. Consumer가 기본 **PAUSED**로 시작 (ACTIVE로 시작하지 않으므로 Dual-Active 불가)
> 2. Sidecar의 메모리 캐시(L2) 또는 Volume Mount 파일(L4)에서 desired state = PAUSED 확인
> 3. Reconcile loop(L3)이 desired(PAUSED) == actual(PAUSED)이므로 상태 유지
>
> 만약 PAUSED 측이 아닌 **ACTIVE 측** Pod가 재시작되면:
> 1. Consumer가 PAUSED로 시작
> 2. Sidecar 캐시에서 desired state = ACTIVE 확인
> 3. Reconcile loop이 ~5초 후 resume 호출 → ACTIVE로 전환

### Static Membership 검증

Static Membership(`group.instance.id = ${HOSTNAME}`)의 동작을 검증한다.

#### 4-1. session.timeout.ms(45초) 이내 복귀 시 — Rebalance 미발생

```bash
# Blue-1 Pod 삭제 (StatefulSet이 자동 재생성)
kubectl delete pod consumer-blue-1 -n kafka-bg-test
echo "Deleted at $(date -u +%T)"

# Pod 복귀 대기 (보통 10~20초)
kubectl wait --for=condition=Ready pod/consumer-blue-1 \
  -n kafka-bg-test --timeout=60s
echo "Ready at $(date -u +%T)"

# Kafka Broker 로그에서 Rebalance 미발생 확인
kubectl logs -n kafka kafka-cluster-dual-role-0 --tail=50 | \
  grep -E "JoinGroup|SyncGroup|Rebalance" | tail -5
# 기대: session.timeout.ms(45초) 이내 복귀이므로 JoinGroup/SyncGroup 없음
# Static Membership에서는 같은 group.instance.id로 재연결 시
# 기존 파티션 할당이 유지됨
```

#### 4-2. session.timeout.ms(45초) 초과 시 — Rebalance 발생

```bash
# Blue StatefulSet을 2로 축소 (Blue-2 제거)
kubectl scale statefulset consumer-blue -n kafka-bg-test --replicas=2
echo "Scaled to 2 at $(date -u +%T)"

# 45초 초과 대기 (50초)
echo "Waiting 50 seconds for session timeout..."
sleep 50

# Broker 로그에서 Rebalance 발생 확인
kubectl logs -n kafka kafka-cluster-dual-role-0 --tail=50 | \
  grep -E "JoinGroup|SyncGroup|Rebalance" | tail -5
# 기대: session.timeout.ms 초과로 Rebalance 발생

# Blue StatefulSet 복원
kubectl scale statefulset consumer-blue -n kafka-bg-test --replicas=3
sleep 30
```

### 정리 — 롤백

```bash
# 시나리오 4 종료 후 Blue ACTIVE로 복원
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"blue"}}'
sleep 10

# 상태 확인 (Sidecar Reconciler가 자동으로 올바른 상태로 전환)
check_all
# 기대: Blue 전체 ACTIVE, Green 전체 PAUSED

# [참고] 만약 Dual-Active가 발생한 경우의 수동 복구 (정상적으로는 불필요):
# emergency_set_state green PAUSED
```

### 검증 기준

| 항목 | 기준 |
|------|------|
| Rebalance 이후 pause 상태 유지 | 필수 (P0 해결로 통과 기대) |
| 양쪽 동시 Active 발생 | 0회 (P0 해결로 통과 기대) |
| Static Membership: 45초 이내 복귀 시 Rebalance 미발생 | 필수 |
| Static Membership: 45초 초과 시 Rebalance 발생 | 확인 |

---

## 시나리오 5: 전환 실패 후 자동 롤백

### 목표

Green Consumer에 높은 에러율(80%)을 주입한 상태에서 전환하고, Switch Controller가 자동으로 롤백하는지 검증.

### 사전 준비 — Blue ACTIVE 확인 & Green 에러율 주입

```bash
# Blue ACTIVE 확인
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  curl -s http://localhost:8080/lifecycle/status
# 기대: {"state":"ACTIVE",...}

# Green Consumer에 에러율 주입 (Sidecar curl 사용, port-forward 불필요)
for i in 0 1 2; do
  echo "=== consumer-green-$i ==="
  kubectl exec -n kafka-bg-test consumer-green-$i -c switch-sidecar -- \
    curl -s -X PUT http://localhost:8080/fault/error-rate \
      -H "Content-Type: application/json" -d '{"errorRatePercent": 80}'
  echo ""
done
```

### 수행

```bash
SCENARIO5_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)

# Blue → Green 전환
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"green"}}'

# Switch Controller 로그 관찰 (별도 터미널)
kubectl logs -n kafka-bg-test -l app=bg-switch-controller -f --tail=1
```

> **[P2 알려진 이슈] 자동 롤백 미구현**
>
> Switch Controller는 lifecycle 상태(ACTIVE/PAUSED)만 확인하며,
> **에러율/Consumer Lag 기반 자동 롤백 로직이 구현되어 있지 않다.**
> 따라서 Green이 80% 에러율로 동작하더라도 Controller는 전환 성공으로 판단한다.
>
> 자동 롤백 미동작 확인 후 **수동 롤백**으로 복구해야 한다.

### 자동 롤백 미동작 확인 후 수동 롤백

```bash
# 30초 대기 후 Blue 상태 확인 — 여전히 PAUSED (자동 롤백 없음)
sleep 30
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  curl -s http://localhost:8080/lifecycle/status
# 기대 (현재 구현): {"state":"PAUSED",...} — 자동 롤백 미발생

# 수동 롤백 수행
ROLLBACK_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"blue"}}'

sleep 5

kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  curl -s http://localhost:8080/lifecycle/status
# 기대: {"state":"ACTIVE",...}

ROLLBACK_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Manual rollback duration: $(( $(date -d "$ROLLBACK_END" +%s) - $(date -d "$ROLLBACK_START" +%s) )) seconds"
```

### 장애 주입 해제

```bash
# Sidecar curl로 직접 해제 (port-forward 불필요)
for i in 0 1 2; do
  kubectl exec -n kafka-bg-test consumer-green-$i -c switch-sidecar -- \
    curl -s -X PUT http://localhost:8080/fault/error-rate \
      -H "Content-Type: application/json" -d '{"errorRatePercent": 0}'
  echo ""
done
```

### 검증 기준

| 항목 | 기준 |
|------|------|
| 자동 롤백 수행 여부 | 확인 (현재 미구현) |
| 수동 롤백 후 Blue 정상 소비 재개 | 필수 |
| 메시지 유실 | 0건 |

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
| 5. 자동 롤백 | _초 | _초(수동) | _건 | _건 | 0회 | PASS/FAIL |
```

### 실제 테스트 결과 (2026-02-21)

| 시나리오 | 전환 시간 | 롤백 시간 | 유실 | 중복 | 동시 Active | 결과 |
|----------|-----------|-----------|------|------|-------------|------|
| 1. 정상 전환 | 1.04초 | - | (*) | (*) | 0회 | **통과** |
| 2. 즉시 롤백 | 1.04초 | 1.03초 | (*) | (*) | 0회 | **통과** |
| 3. Lag 중 전환 | 1.04초 | 1.03초 | (*) | (*) | 0회 | **통과** |
| 4. Rebalance 장애 | 1.08초 | 1.03초 | (*) | (*) | **1회** | **미통과** |
| 5. 자동 롤백 | 1.04초 | (수동)1.03초 | (*) | (*) | 0회 | **부분 통과** |

> (*) Validator로 Loki 기반 유실/중복 측정 가능하나, ~50% "missing"은 전략 C 구조적 특성(PAUSED 측 파티션 미소비)이며 실제 유실 아님.

### 개선 후 테스트 결과 템플릿

> Phase 4 통합 검증에서 채울 예정. 아키텍처 개선 후 시나리오 1~7 재검증 결과.

```markdown
| 시나리오 | 전환 시간 | 롤백 시간 | 유실 | 중복 | 동시 Active | 결과 |
|----------|-----------|-----------|------|------|-------------|------|
| 1. 정상 전환 | _초 | - | _건 | _건 | 0회 | PASS/FAIL |
| 2. 즉시 롤백 | _초 | _초 | _건 | _건 | 0회 | PASS/FAIL |
| 3. Lag 중 전환 | _초 | - | _건 | _건 | 0회 | PASS/FAIL |
| 4. Rebalance 장애 (P0) | _초 | - | _건 | _건 | 0회 | PASS/FAIL |
| 5. 자동 롤백 | _초 | _초(수동) | _건 | _건 | 0회 | PASS/FAIL |
| 6. L2 Consumer 재시작 복구 | - | _초 | - | - | 0회 | PASS/FAIL |
| 7. L4 Controller 다운 fallback | - | _초 | - | - | 0회 | PASS/FAIL |
```

---

## 시나리오 6: L2 검증 — Consumer 재시작 복구

> **[신규 시나리오]** 4-레이어 안전망의 L2(Controller → Sidecar HTTP push)와 L3(Reconcile loop)의
> 동작을 검증한다.

### 목표

ACTIVE 측 Consumer 프로세스를 강제 종료한 후, Sidecar의 캐시 기반 Reconciler가 ~5-10초 내에
Consumer를 올바른 상태(ACTIVE)로 복구하는지 검증.

### 사전 준비

```bash
# Blue=ACTIVE, Green=PAUSED 상태 확인
check_all
check_active  # 기대: blue
```

### 수행

```bash
SCENARIO6_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)

# 1. ACTIVE 측(Green) Consumer 프로세스 강제 종료
#    (현재 active=blue라면 Blue의 Consumer를 종료)
kubectl exec -n kafka-bg-test consumer-blue-0 -c consumer -- kill 1
echo "Consumer process killed at $(date -u +%Y-%m-%dT%H:%M:%SZ)"

# 2. Consumer Container가 재시작될 때까지 대기
#    (Container 재시작이므로 Sidecar는 영향 없음, 메모리 캐시 유지)
echo "Waiting for consumer restart (15 seconds)..."
sleep 15

# 3. Sidecar Reconciler가 Consumer를 ACTIVE로 복구했는지 확인
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  curl -s http://localhost:8080/lifecycle/status
# 기대: {"state":"ACTIVE",...}
# Sidecar 캐시에 desired=ACTIVE가 저장되어 있으므로,
# Consumer 기동 완료 후 다음 reconcile 주기(5초)에 resume 호출

SCENARIO6_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Recovery time: $(( $(date -d "$SCENARIO6_END" +%s) - $(date -d "$SCENARIO6_START" +%s) )) seconds"
```

### 확인

```bash
# Sidecar 로그에서 reconcile 동작 확인
kubectl logs -n kafka-bg-test consumer-blue-0 -c switch-sidecar --tail=20
# 기대: "reconcile succeeded" 로그 (Consumer 재시작 후 desired=ACTIVE 적용)

# 전체 상태 확인 (Dual-Active 없음)
check_all
```

### 검증 기준

| 항목 | 기준 |
|------|------|
| Consumer 재시작 후 복구 시간 | < 30초 (Consumer 기동 ~17초 + Reconcile ~5초) |
| Sidecar 캐시 기반 복구 | Sidecar 로그에 reconcile succeeded 확인 |
| Dual-Active 미발생 | 필수 |

---

## 시나리오 7: L4 검증 — Controller 다운 시 Volume Mount fallback

> **[신규 시나리오]** 4-레이어 안전망의 L4(Volume Mount File Polling)가 최후의 fallback으로
> 동작하는지 검증한다. Controller가 완전히 다운된 상태에서 ConfigMap을 수동으로 패치하고,
> kubelet의 Volume Mount 갱신을 통해 Sidecar가 새 desired state를 적용하는 과정을 확인한다.

### 목표

1. Controller를 0으로 스케일 다운
2. `kafka-consumer-state` ConfigMap을 수동으로 패치하여 Blue/Green 역할 교체
3. Volume Mount 갱신(60-120초) 후 Sidecar가 새 desired state를 적용하는지 확인

### 사전 준비

```bash
# Blue=ACTIVE, Green=PAUSED 상태 확인
check_all
check_active  # 기대: blue

# Desired State ConfigMap 확인
check_desired_state
```

### 수행

```bash
SCENARIO7_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)

# 1. Controller 스케일 다운 (L1, L2 경로 차단)
kubectl scale deploy bg-switch-controller -n kafka-bg-test --replicas=0
echo "Controller scaled to 0 at $(date -u +%Y-%m-%dT%H:%M:%SZ)"
sleep 5

# Controller가 완전히 종료되었는지 확인
kubectl get pods -n kafka-bg-test -l app=bg-switch-controller
# 기대: No resources found

# 2. kafka-consumer-state ConfigMap 수동 패치 (Blue→PAUSED, Green→ACTIVE)
kubectl patch configmap kafka-consumer-state -n kafka-bg-test --type merge \
  -p '{"data":{"consumer-blue-0":"{\"lifecycle\":\"PAUSED\"}","consumer-blue-1":"{\"lifecycle\":\"PAUSED\"}","consumer-blue-2":"{\"lifecycle\":\"PAUSED\"}","consumer-green-0":"{\"lifecycle\":\"ACTIVE\"}","consumer-green-1":"{\"lifecycle\":\"ACTIVE\"}","consumer-green-2":"{\"lifecycle\":\"ACTIVE\"}"}}'
echo "ConfigMap patched at $(date -u +%Y-%m-%dT%H:%M:%SZ)"

# 3. Volume Mount 갱신 대기 (kubelet syncFrequency에 따라 60-120초)
echo "Waiting for Volume Mount update (120 seconds)..."
echo "중간 확인을 위해 30초마다 상태를 출력합니다."

for wait in 30 60 90 120; do
  sleep 30
  echo "=== ${wait}초 경과 ==="
  # Volume Mount 파일이 갱신되었는지 확인
  kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
    cat /etc/consumer-state/consumer-blue-0
  echo ""
  # Consumer 상태 확인
  kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
    curl -s http://localhost:8080/lifecycle/status
  echo ""
done

SCENARIO7_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "L4 fallback time: $(( $(date -d "$SCENARIO7_END" +%s) - $(date -d "$SCENARIO7_START" +%s) )) seconds"
```

### 확인

```bash
# 전체 상태 확인
check_all
# 기대: Blue 전체 PAUSED, Green 전체 ACTIVE (Volume Mount fallback으로 전환 완료)

# Sidecar 로그에서 파일 기반 reconcile 확인
kubectl logs -n kafka-bg-test consumer-blue-0 -c switch-sidecar --tail=20
# 기대: Volume Mount 파일에서 desired state를 읽어 reconcile 수행 로그
```

### 정리

```bash
# Controller 복원
kubectl scale deploy bg-switch-controller -n kafka-bg-test --replicas=1
sleep 10

# active-version ConfigMap도 정합성 맞추기 (green으로 변경)
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"green"}}'
sleep 5

# Blue ACTIVE로 원복
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"blue"}}'
sleep 10

check_all
# 기대: Blue=ACTIVE, Green=PAUSED
```

### 검증 기준

| 항목 | 기준 |
|------|------|
| Volume Mount 갱신 후 상태 전환 | 120초 이내 |
| Controller 없이 Sidecar 자동 복구 | 확인 |
| Dual-Active 미발생 | 필수 |

> **참고:** L4 경로는 최후의 fallback이므로 60-120초 지연은 허용 범위이다.
> 실제 운영에서 Controller가 완전히 다운된 상황에서도 시스템이 결국 수렴(eventual consistency)함을 검증하는 시나리오이다.
> 긴급 상황에서는 `emergency_set_state` 헬퍼 함수로 Sidecar에 직접 push하여 ~1초 내 복구할 수도 있다:
> ```bash
> emergency_set_state blue PAUSED
> emergency_set_state green ACTIVE
> ```

---

## 발견된 버그 및 수정 이력

테스트 수행 중 발견된 버그와 수정 사항:

### 테스트 전 수정 (테스트 수행 전제 조건)

| # | 분류 | 내용 | 근본 원인 | 수정 |
|---|------|------|-----------|------|
| B1 | Consumer | Consumer Group이 `bg-test-group`이 아닌 `bgTestConsumerListener`로 생성 | `@KafkaListener(id=LISTENER_ID)`에서 `groupId` 미지정 시 `id`가 group.id로 사용됨 | `groupId = "${spring.kafka.consumer.group-id:bg-test-group}"` 추가 |
| B2 | Controller | `WaitForState`가 영원히 타임아웃 | `StatusResponse` 구조체의 JSON 태그 `"status"`와 Consumer API의 `"state"` 불일치. Go json.Decoder는 매칭 실패 시 zero value("") 반환 | `json:"status"` → `json:"state"` 변경 |

### 아키텍처 개선으로 해결된 버그

| # | 우선순위 | 내용 | 근본 원인 | 해결 방법 |
|---|----------|------|-----------|-----------|
| B3 | P1 | Sidecar 초기 연결 실패 후 재시도 안 함 | Consumer 기동(~17초) 전 3회 재시도 후 포기, ConfigMap 변경 없으면 재시도 안 함 | **해결**: Reconciler의 5초 주기 polling으로 Consumer 기동 완료 후 자동 reconcile |
| B4 | **P0** | PAUSED 측 Pod 재시작 시 Dual-Active | `INITIAL_STATE=ACTIVE` 정적 env var → ConfigMap 상태 미참조 | **해결**: Consumer 기본 PAUSED + Sidecar Reconciler가 desired state 기반으로 전환 |

> **아키텍처 개선 요약:**
> - Sidecar: K8s API Watch(`configmap_watcher.go`) 삭제 → File Polling + Reconciler(`reconciler/reconciler.go`) + HTTP Push 수신
> - Consumer: `INITIAL_STATE` env var 제거, application.yaml `initial-state` 기본값 PAUSED
> - Controller: `kafka-consumer-state` ConfigMap 선기록 + Sidecar HTTP push 추가
> - 4-레이어 안전망 구현 (L1: Controller→Consumer, L2: Controller→Sidecar, L3: Reconcile, L4: Volume Mount)

### 테스트 중 발견 (미수정)

| # | 우선순위 | 내용 | 근본 원인 |
|---|----------|------|-----------|
| B5 | P2 | Lease holder 업데이트 실패 | 이전 Lease 만료 전 재획득 시도 시 에러 (기능 영향 없음) |

### Validator 버그 수정 (테스트 후)

| # | 내용 | 수정 |
|---|------|------|
| B6 | Spring Boot 로그 prefix로 JSON 파싱 실패 | `_parse_json_line()`에 `line.find("{")` fallback 추가 |
| B7 | Consumer `"groupId"` vs Validator `"group_id"` 필드명 불일치 | 양쪽 키 모두 지원 |
| B8 | timezone-aware vs naive datetime 비교 오류 | `datetime.fromtimestamp(tz=utc)` 사용 |

---

## 트러블슈팅

### Switch Controller가 전환 시 타임아웃

```
"message":"timeout waiting for state PAUSED"
```

원인: Consumer Pod가 응답하지 않거나, lifecycle API가 비정상.
해결:
```bash
# Consumer Pod 상태 확인
kubectl get pods -n kafka-bg-test
# Pod가 Running/Ready인지 확인

# Consumer lifecycle API 직접 호출
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  curl -s http://localhost:8080/lifecycle/status
# 응답이 없으면 Consumer Container 로그 확인
kubectl logs -n kafka-bg-test consumer-blue-0 -c consumer --tail=20
```

### 전환 후 이전 상태 복구 안 됨

Switch Controller가 ConfigMap 변경을 감지하지 못하는 경우:
```bash
# Controller 재시작
kubectl rollout restart deployment bg-switch-controller -n kafka-bg-test
sleep 10

# 또는 수동으로 각 Consumer의 lifecycle 상태를 직접 제어 (Sidecar curl 사용)
# Blue pause (Green이 ACTIVE일 때)
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  curl -s -X POST http://localhost:8080/lifecycle/pause

# 또는 Sidecar의 desired-state endpoint에 직접 push
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  curl -s -X POST http://localhost:8082/desired-state \
    -H "Content-Type: application/json" -d '{"lifecycle":"PAUSED"}'
```

### Sidecar Reconciler가 동작하지 않을 때

Sidecar 로그를 확인한다:
```bash
kubectl logs -n kafka-bg-test consumer-blue-0 -c switch-sidecar --tail=20
```

가능한 원인:
1. Volume Mount 파일이 없음: `kubectl exec ... -- ls /etc/consumer-state/`로 확인
2. Consumer가 아직 기동 중: Consumer 로그 확인, 기동 완료 후 다음 reconcile 주기(5초)에 자동 적용
3. Desired state와 actual state가 이미 일치: 정상 동작 (SKIP 로그)

### Java 앱 빌드 시 "invalid target release: 17" 오류

호스트 환경에서 기본 Java가 8인 경우 발생:
```bash
# Java 17로 명시 지정하여 빌드
JAVA_HOME=/usr/lib/jvm/java-17-openjdk-amd64 mvn package -DskipTests -B

# minikube 이미지 빌드 (containerd 런타임 → docker-env 대신 image build 사용)
minikube -p kafka-bg-test image build -t bg-test-consumer:latest apps/consumer/
```

---

## 다음 단계

- [07-strategy-b-test.md](07-strategy-b-test.md): 전략 B 테스트 수행 (별도 Consumer Group + Offset 동기화)
  - 전략 C의 파티션 분할 문제가 발생하지 않는 구조
  - Blue/Green이 독립 Consumer Group을 사용하므로 PAUSED 측 Lag 누적 없음
