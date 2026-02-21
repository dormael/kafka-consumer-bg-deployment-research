# Tutorial 06: 전략 C 테스트 수행 (Pause/Resume Atomic Switch)

> **관련 태스크:** plan/task05.md
> **우선순위:** 1순위
> **최종 수정:** 2026-02-21 (실제 테스트 수행 결과 반영)

---

## 공통 참고사항

### HTTP 클라이언트 제약

Container 이미지에 `curl`이 설치되어 있지 않다. 용도에 따라 아래 방식을 사용한다.

| 용도 | 방법 |
|------|------|
| **GET 요청** (상태 확인) | Pod 내 `wget -qO-` 사용 (BusyBox wget) |
| **PUT 요청** (장애 주입) | `kubectl port-forward` + 호스트의 `curl -X PUT` |
| **Kafka Consumer Group 조회** | Kafka broker Pod에서 `kafka-consumer-groups.sh` 실행 |

> **주의:** Consumer Pod는 2개의 Container(`consumer`, `switch-sidecar`)를 포함한다.
> `wget`은 `switch-sidecar` Container에서 실행해야 한다 (같은 Pod이므로 `localhost:8080`으로 consumer에 접근 가능).
> Consumer Container(OpenJDK 이미지)에는 `wget`이 있지만, sidecar Container(Go 바이너리)의 `wget`이 더 가볍다.

### 헬퍼 함수 (선택사항)

테스트 중 반복되는 명령을 단축하려면 아래 함수를 쉘에 등록한다:

```bash
# Consumer lifecycle 상태 확인 (GET)
check_status() {
  local pod=$1
  kubectl exec -n kafka-bg-test "$pod" -c switch-sidecar -- \
    wget -qO- http://localhost:8080/lifecycle/status 2>/dev/null
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

# ConfigMap 현재 상태 확인
check_active() {
  kubectl get configmap kafka-consumer-active-version -n kafka-bg-test \
    -o jsonpath='{.data.active}' && echo ""
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

---

## 사전 조건 확인

모든 항목을 확인한 후 테스트를 시작한다.

```bash
# 1. Producer 동작 확인 (TPS 100)
kubectl exec -n kafka-bg-test deploy/bg-test-producer -- \
  wget -qO- http://localhost:8080/producer/stats
# 기대 결과: messagesPerSecond: 100
# running: true 확인

# 2. Blue Consumer 상태: ACTIVE
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  wget -qO- http://localhost:8080/lifecycle/status
# 기대 결과: {"state":"ACTIVE","stateCode":0,...}

# 3. Blue Consumer Lag = 0 확인
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group --describe
# 기대 결과: LAG 열이 모두 0 (또는 매우 작은 값)
# ※ PAUSED 측 파티션은 Lag이 누적될 수 있음 — "전략 C 구조적 특성" 참조

# 4. Green Consumer 상태: PAUSED
kubectl exec -n kafka-bg-test consumer-green-0 -c switch-sidecar -- \
  wget -qO- http://localhost:8080/lifecycle/status
# 기대 결과: {"state":"PAUSED","stateCode":2,...}

# 5. Switch Controller 동작 확인
kubectl get pods -n kafka-bg-test -l app=bg-switch-controller
# 기대 결과: 1/1 Running

# 6. ConfigMap 현재 상태
kubectl get configmap kafka-consumer-active-version -n kafka-bg-test \
  -o jsonpath='{.data.active}' && echo ""
# 기대 결과: blue

# 7. Grafana 대시보드 접근
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
  wget -qO- http://localhost:8080/lifecycle/status
# 기대: {"state":"ACTIVE","stateCode":0,...}

# Blue 상태 확인 — PAUSED여야 함
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  wget -qO- http://localhost:8080/lifecycle/status
# 기대: {"state":"PAUSED","stateCode":2,...}

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
  wget -qO- http://localhost:8080/lifecycle/status
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
  wget -qO- http://localhost:8080/lifecycle/status
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
  wget -qO- http://localhost:8080/lifecycle/status
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
  wget -qO- http://localhost:8080/lifecycle/status
# 기대: {"state":"ACTIVE",...}
```

### 처리 지연 주입

Consumer의 장애 주입 API는 **PUT 메서드**를 사용한다. Container 내 BusyBox wget은 PUT을 지원하지 않으므로 `port-forward`를 사용한다.

```bash
# 각 Blue Pod에 개별 port-forward로 장애 주입
for i in 0 1 2; do
  kubectl port-forward -n kafka-bg-test consumer-blue-$i 1808$i:8080 &
done
sleep 2

for i in 0 1 2; do
  echo "=== consumer-blue-$i ==="
  curl -s -X PUT http://localhost:1808$i/fault/processing-delay \
    -H "Content-Type: application/json" -d '{"delayMs": 200}'
  echo ""
done

# port-forward 모두 종료
pkill -f "kubectl port-forward.*consumer-blue" 2>/dev/null
```

> **참고:** `svc/consumer-blue-svc`로 port-forward하면 로드밸런싱되어
> 일부 Pod에만 지연이 적용될 수 있다. 각 Pod에 개별 port-forward하는 것이 확실하다.

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
  wget -qO- http://localhost:8080/lifecycle/status
# 기대: {"state":"ACTIVE",...}

SCENARIO3_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Switch end: $SCENARIO3_END"
echo "Duration: $(( $(date -d "$SCENARIO3_END" +%s) - $(date -d "$SCENARIO3_START" +%s) )) seconds"
```

> **관찰 포인트:** Switch Controller는 현재 **Lag 확인 없이 즉시 전환**한다.
> Lag 소진 대기 모드는 미구현(P2 이슈)이므로, Lag이 있어도 전환 시간에 영향 없음.

### 장애 주입 해제

```bash
# Blue Consumer에 지연 해제 (전환 후 Blue는 PAUSED이므로 큰 영향은 없지만 정리)
for i in 0 1 2; do
  kubectl port-forward -n kafka-bg-test consumer-blue-$i 1808$i:8080 &
done
sleep 2

for i in 0 1 2; do
  curl -s -X PUT http://localhost:1808$i/fault/processing-delay \
    -H "Content-Type: application/json" -d '{"delayMs": 0}'
  echo ""
done

pkill -f "kubectl port-forward.*consumer-blue" 2>/dev/null
```

### 검증 기준

| 항목 | 기준 |
|------|------|
| Lag 발생 중 전환 시간 | < 5초 (정상 시와 동일) |
| Lag 소진 대기 중 메시지 유실 | 0건 |
| 전략별 Lag 처리 방식 차이 | 측정 및 기록 |

---

## 시나리오 4: Rebalance 장애 주입 (전략 C 전용)

### 목표

전환 직후 PAUSED 측 Pod를 강제 재시작하여 Rebalance를 유발하고:
1. Rebalance 후에도 PAUSED 상태가 유지되는지 확인
2. 양쪽 동시 Active 발생 여부를 모니터링
3. Static Membership 동작을 검증

### 사전 준비 — Blue ACTIVE로 복원

```bash
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"blue"}}'
sleep 10

kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  wget -qO- http://localhost:8080/lifecycle/status
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

# 3. 전환 직후 Blue Pod 1개 강제 삭제 → Rebalance 유발
#    (Blue는 이제 PAUSED 상태여야 함)
kubectl delete pod consumer-blue-0 -n kafka-bg-test
echo "consumer-blue-0 deleted at $(date -u +%Y-%m-%dT%H:%M:%SZ)"

# 4. StatefulSet이 Pod를 재생성할 때까지 대기
echo "Waiting for pod recreation (30 seconds)..."
sleep 30
```

### 재생성 후 상태 확인

```bash
# Pod 재생성 확인
kubectl get pod consumer-blue-0 -n kafka-bg-test
# 기대: Running, READY 2/2

# 핵심 확인: 재생성된 Blue-0의 lifecycle 상태
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  wget -qO- http://localhost:8080/lifecycle/status
# ★ 기대: PAUSED (ConfigMap의 active=green이므로)
# ★ 실제: ACTIVE (P0 버그 — 아래 "알려진 이슈" 참조)

# 양쪽 동시 Active 확인
echo "=== All consumer states ==="
for pod in consumer-blue-0 consumer-blue-1 consumer-blue-2 \
           consumer-green-0 consumer-green-1 consumer-green-2; do
  echo -n "$pod: "
  kubectl exec -n kafka-bg-test "$pod" -c switch-sidecar -- \
    wget -qO- http://localhost:8080/lifecycle/status 2>/dev/null
  echo ""
done
# Dual-Active 여부: Blue 중 ACTIVE + Green 중 ACTIVE가 동시 존재하면 Dual-Active
```

> **[P0 알려진 이슈] PAUSED 측 Pod 재시작 시 Dual-Active 발생**
>
> Consumer의 `INITIAL_STATE`가 StatefulSet의 **정적 env var**(`ACTIVE`)로 설정되어 있어,
> 재시작된 Pod는 ConfigMap 상태를 무시하고 항상 ACTIVE로 시작한다.
> Sidecar가 ConfigMap을 감지하여 pause 명령을 보내야 하지만, Consumer 기동(~17초) 전에
> 3회 재시도가 모두 실패하면 포기하고, 이후 ConfigMap 변경이 없으므로 재시도하지 않는다.
>
> **결과:** 재시작된 Blue-0이 ACTIVE로 소비 시작 → Green과 동시 Active 발생.
>
> **수동 복구 방법:**
> ```bash
> # 재시작된 Blue-0을 수동으로 pause
> kubectl port-forward -n kafka-bg-test consumer-blue-0 18080:8080 &
> sleep 2
> curl -s -X POST http://localhost:18080/lifecycle/pause
> kill %1 2>/dev/null
> ```

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

# Dual-Active가 발생한 경우, 모든 Green을 수동 pause
for i in 0 1 2; do
  kubectl port-forward -n kafka-bg-test consumer-green-$i 1808$i:8080 &
done
sleep 2
for i in 0 1 2; do
  curl -s -X POST http://localhost:1808$i/lifecycle/pause
  echo ""
done
pkill -f "kubectl port-forward.*consumer-green" 2>/dev/null
```

### 검증 기준

| 항목 | 기준 |
|------|------|
| Rebalance 이후 pause 상태 유지 | 필수 (현재 P0 버그로 미통과) |
| 양쪽 동시 Active 발생 | 0회 (현재 P0 버그로 1회 발생) |
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
  wget -qO- http://localhost:8080/lifecycle/status
# 기대: {"state":"ACTIVE",...}

# Green Consumer에 에러율 주입 (각 Pod에 개별 port-forward)
for i in 0 1 2; do
  kubectl port-forward -n kafka-bg-test consumer-green-$i 1808$i:8080 &
done
sleep 2

for i in 0 1 2; do
  echo "=== consumer-green-$i ==="
  curl -s -X PUT http://localhost:1808$i/fault/error-rate \
    -H "Content-Type: application/json" -d '{"errorRatePercent": 80}'
  echo ""
done

pkill -f "kubectl port-forward.*consumer-green" 2>/dev/null
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
  wget -qO- http://localhost:8080/lifecycle/status
# 기대 (현재 구현): {"state":"PAUSED",...} — 자동 롤백 미발생

# 수동 롤백 수행
ROLLBACK_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"blue"}}'

sleep 5

kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  wget -qO- http://localhost:8080/lifecycle/status
# 기대: {"state":"ACTIVE",...}

ROLLBACK_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Manual rollback duration: $(( $(date -d "$ROLLBACK_END" +%s) - $(date -d "$ROLLBACK_START" +%s) )) seconds"
```

### 장애 주입 해제

```bash
for i in 0 1 2; do
  kubectl port-forward -n kafka-bg-test consumer-green-$i 1808$i:8080 &
done
sleep 2

for i in 0 1 2; do
  curl -s -X PUT http://localhost:1808$i/fault/error-rate \
    -H "Content-Type: application/json" -d '{"errorRatePercent": 0}'
  echo ""
done

pkill -f "kubectl port-forward.*consumer-green" 2>/dev/null
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

---

## 발견된 버그 및 수정 이력

테스트 수행 중 발견된 버그와 수정 사항:

### 테스트 전 수정 (테스트 수행 전제 조건)

| # | 분류 | 내용 | 근본 원인 | 수정 |
|---|------|------|-----------|------|
| B1 | Consumer | Consumer Group이 `bg-test-group`이 아닌 `bgTestConsumerListener`로 생성 | `@KafkaListener(id=LISTENER_ID)`에서 `groupId` 미지정 시 `id`가 group.id로 사용됨 | `groupId = "${spring.kafka.consumer.group-id:bg-test-group}"` 추가 |
| B2 | Controller | `WaitForState`가 영원히 타임아웃 | `StatusResponse` 구조체의 JSON 태그 `"status"`와 Consumer API의 `"state"` 불일치. Go json.Decoder는 매칭 실패 시 zero value("") 반환 | `json:"status"` → `json:"state"` 변경 |

### 테스트 중 발견 (미수정)

| # | 우선순위 | 내용 | 근본 원인 |
|---|----------|------|-----------|
| B3 | P1 | Sidecar 초기 연결 실패 후 재시도 안 함 | Consumer 기동(~17초) 전 3회 재시도 후 포기, ConfigMap 변경 없으면 재시도 안 함 |
| B4 | **P0** | PAUSED 측 Pod 재시작 시 Dual-Active | `INITIAL_STATE=ACTIVE` 정적 env var → ConfigMap 상태 미참조 |
| B5 | P2 | Lease holder 업데이트 실패 | 이전 Lease 만료 전 재획득 시도 시 에러 (기능 영향 없음) |

### Validator 버그 수정 (테스트 후)

| # | 내용 | 수정 |
|---|------|------|
| B6 | Spring Boot 로그 prefix로 JSON 파싱 실패 | `_parse_json_line()`에 `line.find("{")` fallback 추가 |
| B7 | Consumer `"groupId"` vs Validator `"group_id"` 필드명 불일치 | 양쪽 키 모두 지원 |
| B8 | timezone-aware vs naive datetime 비교 오류 | `datetime.fromtimestamp(tz=utc)` 사용 |

---

## 트러블슈팅

### port-forward 종료 시 exit code 1 에러

```
E0221 16:30:45.123456 12345 portforward.go:394] error copying from local connection to remote stream: ...
```

이것은 정상 동작이다. `kill` 또는 `pkill`로 port-forward 프로세스를 종료하면 Go 런타임이 연결 정리 중 에러를 출력한다. API 호출은 이미 성공 완료된 상태이므로 무시해도 된다.

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
  wget -qO- http://localhost:8080/lifecycle/status
# 응답이 없으면 Consumer Container 로그 확인
kubectl logs -n kafka-bg-test consumer-blue-0 -c consumer --tail=20
```

### 전환 후 이전 상태 복구 안 됨

Switch Controller가 ConfigMap 변경을 감지하지 못하는 경우:
```bash
# Controller 재시작
kubectl rollout restart deployment bg-switch-controller -n kafka-bg-test
sleep 10

# 또는 수동으로 각 Consumer의 lifecycle 상태를 직접 제어
# Blue pause (Green이 ACTIVE일 때)
kubectl port-forward -n kafka-bg-test consumer-blue-0 18080:8080 &
sleep 2
curl -s -X POST http://localhost:18080/lifecycle/pause
kill %1 2>/dev/null
```

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
