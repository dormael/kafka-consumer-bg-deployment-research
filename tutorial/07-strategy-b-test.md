# Tutorial 07: 전략 B 테스트 수행 (별도 Consumer Group + Offset 동기화)

> **관련 태스크:** plan/task06.md
> **우선순위:** 2순위
> **최종 수정:** 2026-02-22

---

## 공통 참고사항

### HTTP 클라이언트

전략 B에서는 **Sidecar가 없으므로** Consumer Pod에 직접 접근해야 한다.
Consumer 이미지에 `curl`이 포함되어 있지 않으므로, **`kubectl port-forward`**를 사용한다.

| 용도 | 방법 |
|------|------|
| **GET 요청** (상태 확인) | `kubectl port-forward` + `curl` |
| **PUT 요청** (장애 주입) | `kubectl port-forward` + `curl` |
| **Kafka Consumer Group 조회** | Kafka broker Pod에서 `kafka-consumer-groups.sh` 실행 |

> **전략 C와의 차이:** 전략 C에서는 Sidecar Container에 `curl`이 있어 `kubectl exec -c switch-sidecar`로
> HTTP 요청이 가능했다. 전략 B에는 Sidecar가 없으므로 `port-forward` 방식을 사용한다.

### 헬퍼 함수

전환/롤백 절차가 여러 단계로 구성되므로, 헬퍼 스크립트를 `source`로 로드하여 사용한다:

```bash
source tools/strategy-b-switch.sh
```

사용 가능한 함수:

| 함수 | 용도 |
|------|------|
| `check_offsets` | Blue/Green 그룹 offset 조회 |
| `check_b_status` | Deployment/Pod 상태 조회 |
| `switch_to_green` | Blue→Green 전환 자동화 (8단계) |
| `switch_to_blue` | Green→Blue 롤백 (역방향, 7단계) |
| `inject_fault` | `port-forward` 기반 장애 주입 |
| `clear_fault` | 장애 주입 해제 |

### 환경 정보

| 항목 | 값 |
|------|-----|
| Grafana | `http://192.168.58.2:30080` (admin / admin123) |
| Loki | ClusterIP `svc/loki:3100` (monitoring 네임스페이스) |
| Kafka Broker Pod | `kafka-cluster-dual-role-0` (kafka 네임스페이스) |
| Blue Consumer Group | `bg-test-group-blue` |
| Green Consumer Group | `bg-test-group-green` |
| Topic | `bg-test-topic` (8 partitions) |
| Blue Deployment | `consumer-b-blue` (replicas: 3) |
| Green Deployment | `consumer-b-green` (replicas: 0, 대기) |
| Active Version ConfigMap | `kafka-consumer-active-version` |

---

## 아키텍처 개요: 전략 B

### 별도 Consumer Group 구조

```
Producer (100 TPS)
    │
    ▼
Kafka Topic: bg-test-topic (8 partitions)
    │
    ├── Consumer Group: bg-test-group-blue
    │     ├── consumer-b-blue-xxx (partition 0,1,2)
    │     ├── consumer-b-blue-yyy (partition 3,4,5)
    │     └── consumer-b-blue-zzz (partition 6,7)
    │     → 3 replicas, ACTIVE (소비 중)
    │
    └── Consumer Group: bg-test-group-green
          → 0 replicas (대기 중, 전환 시 scale up)
```

### 전략 C 대비 구조적 장단점

| 항목 | 전략 C (Pause/Resume) | 전략 B (Offset 동기화) |
|------|----------------------|----------------------|
| Consumer Group | 동일 (`bg-test-group`) | **별도** (`blue/green`) |
| 전환 방식 | HTTP pause/resume | **Scale down + Offset reset + Scale up** |
| 전환 시간 | ~1초 | **30~60초** (Pod 시작/종료 포함) |
| Sidecar | 필요 (4-레이어 안전망) | **불필요** |
| K8s 워크로드 | StatefulSet | **Deployment** |
| Static Membership | 사용 | **미사용** |
| PAUSED 측 Lag 문제 | 있음 (~50% 파티션 Lag 누적) | **없음** (별도 Group) |
| Dual-Active 위험 | 있음 (P0으로 해결) | **없음** (Scale 기반, 한쪽만 존재) |
| Offset 동기화 | 불필요 (같은 Group) | **필수** (kafka-consumer-groups.sh) |
| 롤백 복잡도 | 낮음 (ConfigMap 변경만) | **높음** (역방향 offset 동기화 필요) |

### Offset 동기화 흐름

```
[Blue ACTIVE, 3 replicas]    [Green STANDBY, 0 replicas]

  1. Blue scale down → 0
     └── graceful shutdown, offset commit 완료

  2. Green group offset reset (--to-current)
     └── 현재 log-end-offset으로 설정

  3. Green scale up → 3
     └── 새 Pod 시작, Group 내 Rebalance 발생
     └── log-end-offset부터 소비 시작

  4. ConfigMap 업데이트 (active: green)

     ※ 역방향(Green→Blue)도 동일 절차
```

> **`--to-current`의 의미:**
> - Blue scale down 후, Blue의 committed offset이 곧 "current" offset
> - 이 시점에서 Producer가 계속 메시지를 보내고 있으므로 log-end-offset은 계속 증가
> - Green의 offset을 log-end-offset으로 설정하면, Blue 종료 후 ~ Green 시작 전 사이의 메시지는
>   Green이 시작된 후에야 소비됨 (유실은 아니지만 지연 발생)

---

## 사전 조건

### 1. 전략 C 리소스 Scale Down

전략 B 테스트 전에 전략 C 리소스를 중단하여 리소스 절약 및 Consumer Group 충돌을 방지한다:

```bash
# 전략 C Consumer Scale Down
kubectl scale statefulset consumer-blue consumer-green -n kafka-bg-test --replicas=0
# StatefulSet이므로 Pod가 완전히 종료될 때까지 대기
kubectl wait --for=jsonpath='{.status.readyReplicas}'=0 \
  statefulset/consumer-blue -n kafka-bg-test --timeout=60s 2>/dev/null || true
kubectl wait --for=jsonpath='{.status.readyReplicas}'=0 \
  statefulset/consumer-green -n kafka-bg-test --timeout=60s 2>/dev/null || true

# Switch Controller Scale Down (전략 B에서는 사용하지 않음)
kubectl scale deployment bg-switch-controller -n kafka-bg-test --replicas=0

# 확인
kubectl get statefulset,deployment -n kafka-bg-test
# 기대: consumer-blue 0/0, consumer-green 0/0, bg-switch-controller 0/0
```

### 2. 전략 B 매니페스트 배포

```bash
# 전략 B Consumer 배포
kubectl apply -f k8s/strategy-b/consumer-b-blue-deployment.yaml
kubectl apply -f k8s/strategy-b/consumer-b-green-deployment.yaml

# Blue Deployment 확인 (3/3 Ready)
kubectl rollout status deployment consumer-b-blue -n kafka-bg-test --timeout=120s

# Green Deployment 확인 (0/0, 대기 상태)
kubectl get deployment consumer-b-green -n kafka-bg-test
# 기대: READY 0/0
```

### 3. Producer 동작 확인

```bash
kubectl exec -n kafka-bg-test deploy/bg-test-producer -- \
  curl -s http://localhost:8080/producer/stats
# 기대: messagesPerSecond: 100, running: true
```

### 4. Blue Consumer 정상 소비 확인

```bash
# Blue Consumer Group offset 확인
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-blue --describe
# 기대: 8개 파티션 모두 할당, LAG ≈ 0 (또는 감소 중)

# 헬퍼 함수 로드 및 상태 확인
source tools/strategy-b-switch.sh
check_b_status
# 기대: consumer-b-blue READY 3, consumer-b-green READY 0
```

### 5. ConfigMap 초기 상태

```bash
# Active version을 blue로 설정
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"blue"}}'

kubectl get configmap kafka-consumer-active-version -n kafka-bg-test \
  -o jsonpath='{.data.active}' && echo ""
# 기대: blue
```

---

## 전략 B 구조적 특성 (테스트 전 필독)

전략 B는 Blue/Green이 **별도 Consumer Group**을 사용한다.

```
Consumer Group: bg-test-group-blue (ACTIVE)
  consumer-b-blue-xxx: partition 0, 1, 2
  consumer-b-blue-yyy: partition 3, 4, 5
  consumer-b-blue-zzz: partition 6, 7
  → 8개 파티션을 3개 Pod가 소비 (전체 소비)

Consumer Group: bg-test-group-green (STANDBY, 0 replicas)
  → Pod 없음, offset 미존재
```

**전략 C와의 결정적 차이:**
- 전략 C: 같은 Group 내에서 PAUSED 측에 할당된 파티션은 Lag 누적 (구조적 한계)
- **전략 B: ACTIVE 측이 8개 파티션 전체를 소비** → PAUSED 측 Lag 문제 없음
- Validator 결과에서 ~50% "missing"이 나타나지 않음

**전략 B의 약점:**
- 전환 시간이 30~60초 (Pod 시작/종료 시간 포함)
- Offset 동기화가 필수 (수동 또는 자동화 스크립트)
- 전환 중 잠깐의 소비 공백 발생 (Blue down ~ Green ready 사이)

---

## 시나리오 1: 정상 Blue → Green 전환

### 목표

Blue ACTIVE → Green ACTIVE 전환이 60초 이내에 완료되고, Offset 동기화가 정확히 이루어지는지 검증.

### 수행

```bash
# 헬퍼 스크립트 로드 (아직 안 했다면)
source tools/strategy-b-switch.sh

# 현재 상태 확인
check_b_status

# 전환 전 offset 기록
check_offsets
```

**방법 A: 자동화 스크립트 사용**

```bash
# 한 번에 전환 (8단계 자동 실행)
switch_to_green
```

**방법 B: 수동 단계별 실행**

```bash
# 1. 시작 시각 기록
SWITCH_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Switch start: $SWITCH_START"

# 2. Blue의 현재 Offset 확인
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-blue --describe

# 3. Blue Consumer 스케일 다운 (소비 중단)
kubectl scale deployment consumer-b-blue -n kafka-bg-test --replicas=0

# 4. Blue graceful shutdown 대기 (offset commit 완료)
echo "Blue shutdown 대기 (10초)..."
sleep 10

# 5. Blue의 최종 Offset 확인 (committed offset)
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-blue --describe

# 6. Green Consumer Group의 Offset을 현재 log-end-offset으로 동기화
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-green --topic bg-test-topic \
    --reset-offsets --to-current --execute

# 7. Green Consumer 스케일 업
kubectl scale deployment consumer-b-green -n kafka-bg-test --replicas=3

# 8. Green Consumer Ready 대기
kubectl rollout status deployment consumer-b-green -n kafka-bg-test --timeout=120s

# 9. ConfigMap 업데이트
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"green"}}'

# 10. 완료 시각 기록
SWITCH_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Switch end: $SWITCH_END"
echo "Duration: $(( $(date -d "$SWITCH_END" +%s) - $(date -d "$SWITCH_START" +%s) )) seconds"
```

### 전환 완료 확인

```bash
# Green Consumer 상태 확인
check_b_status
# 기대: consumer-b-blue READY 0, consumer-b-green READY 3

# Green Consumer Group Lag 확인
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-green --describe
# 기대: 8개 파티션 모두 할당, LAG 감소 중
```

### 안정화 대기 (2분)

```bash
sleep 120

# Green Consumer Lag 확인 (안정화 후)
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-green --describe
# 기대: LAG ≈ 0 (안정)
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
  --strategy B \
  --output report/validation-b-scenario1.md

# 4. port-forward 종료
kill $LOKI_PF_PID 2>/dev/null
```

> **Validator 결과 해석:**
> - 전략 B에서는 전략 C와 달리 PAUSED 측 Lag 문제가 없으므로, "missing" 비율이 현저히 낮아야 함
> - Blue shutdown ~ Green 시작 사이의 소비 공백에서 발생한 메시지는 Green 시작 후 소비됨
> - `Duplicates`: at-least-once 보장에 의한 소량 중복 가능

### Grafana 스크린샷 저장

브라우저에서 `http://192.168.58.2:30080` → 대시보드를 열어 스크린샷 저장:

- Consumer Lag 시계열 → `report/screenshots/b-scenario1-lag.png`
- Messages/sec → `report/screenshots/b-scenario1-tps.png`

### 검증 기준

| 항목 | 기준 | 통과 조건 |
|------|------|-----------|
| 전환 완료 시간 | < 60초 | 필수 |
| 메시지 유실 | 0건 | 필수 |
| Green Consumer Lag (전환 후 2분) | < 100 | 필수 |
| Offset 동기화 정확도 | 100% | 필수 |

---

## 시나리오 2: 전환 직후 즉시 롤백

### 목표

Blue→Green 전환 후 즉시 Green→Blue 롤백이 60초 이내에 완료되고, Blue가 정상 소비를 재개하는지 검증.

### 사전 준비

시나리오 1 이후 상태(Green=ACTIVE 3개, Blue=0개)에서 시작.

```bash
# 현재 상태 확인
check_b_status
# 기대: consumer-b-blue 0, consumer-b-green 3
```

### 수행

**방법 A: 자동화 스크립트 사용**

```bash
switch_to_blue
```

**방법 B: 수동 단계별 실행**

```bash
ROLLBACK_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Rollback start: $ROLLBACK_START"

# 1. Green 현재 offset 확인
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-green --describe

# 2. Green scale down
kubectl scale deployment consumer-b-green -n kafka-bg-test --replicas=0

# 3. Green shutdown 대기
echo "Green shutdown 대기 (10초)..."
sleep 10

# 4. Blue group offset 동기화 (Green의 현재 offset으로)
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-blue --topic bg-test-topic \
    --reset-offsets --to-current --execute

# 5. Blue scale up
kubectl scale deployment consumer-b-blue -n kafka-bg-test --replicas=3

# 6. Blue rollout 대기
kubectl rollout status deployment consumer-b-blue -n kafka-bg-test --timeout=120s

# 7. ConfigMap 업데이트
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"blue"}}'

ROLLBACK_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Rollback end: $ROLLBACK_END"
echo "Rollback duration: $(( $(date -d "$ROLLBACK_END" +%s) - $(date -d "$ROLLBACK_START" +%s) )) seconds"
```

### 검증

```bash
# Blue Consumer Lag 확인
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-blue --describe
# 기대: 8개 파티션 할당, LAG 감소 중

# 안정화 대기 후 확인
sleep 60
check_offsets
# 기대: Blue LAG ≈ 0
```

### 검증 기준

| 항목 | 기준 |
|------|------|
| 롤백 완료 시간 | < 60초 |
| Blue 재개 후 Consumer Lag | < 100 유지 |
| 메시지 유실 | 0건 |
| Offset 동기화 정확도 | Green 마지막 offset = Blue 시작 offset |

---

## 시나리오 3: Consumer Lag 발생 중 전환

### 목표

Blue Consumer에 처리 지연을 주입하여 Lag > 500을 유발한 상태에서 전환을 수행하고,
Lag이 있는 상태에서의 Offset 동기화 동작과 중복/유실을 관찰한다.

### 사전 준비 — Blue ACTIVE 상태 확인

```bash
check_b_status
# 기대: consumer-b-blue READY 3, consumer-b-green READY 0
```

### 처리 지연 주입

> **참고:** 전략 B에서는 Sidecar가 없으므로 `port-forward`를 사용한다.

```bash
# 헬퍼 함수로 장애 주입
inject_fault consumer-b-blue processing-delay '{"delayMs":200}'
```

또는 수동으로:

```bash
# 각 Blue Pod에 port-forward로 장애 주입
for pod in $(kubectl get pods -n kafka-bg-test -l strategy=b,color=blue \
  -o jsonpath='{.items[*].metadata.name}'); do
  echo "=== $pod ==="
  kubectl port-forward -n kafka-bg-test "$pod" 18080:8080 &
  PF_PID=$!
  sleep 1
  curl -s -X PUT http://localhost:18080/fault/processing-delay \
    -H "Content-Type: application/json" -d '{"delayMs":200}'
  echo ""
  kill $PF_PID 2>/dev/null
  wait $PF_PID 2>/dev/null
done
```

### Lag 증가 대기

```bash
# Lag 증가 대기 (약 2분)
echo "Lag 증가 대기 (2분)..."
sleep 120

# Lag 확인 (LAG > 500 확인)
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-blue --describe
# 기대: LAG > 500 (Blue가 소비를 따라잡지 못하는 상태)
```

### 수행

```bash
SCENARIO3_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Switch start (with lag): $SCENARIO3_START"

# Lag > 500 상태에서 전환
switch_to_green
```

> **관찰 포인트 (전략 B 고유):**
> - Blue를 scale down하면 Blue의 committed offset < log-end-offset (Lag 존재)
> - `--to-current`는 log-end-offset으로 설정하므로, **Blue가 미소비한 Lag 메시지는 건너뛰게 됨**
> - 이는 전략 B의 본질적 한계: Lag 상태에서 전환 시 **메시지 유실 가능**
> - 전략 C에서는 같은 Group이므로 Green이 이어서 소비하지만, 전략 B는 별도 Group이므로 수동 동기화 필요

### 장애 주입 해제

```bash
# Blue에 대한 장애 해제 (현재 scale down 상태이므로 Green에는 영향 없음)
# Blue가 다시 scale up될 때를 위해 기록만 해둠
# Green은 장애 없이 정상 소비 중
```

### 유실/중복 확인

```bash
# Green Consumer Lag 확인
sleep 60
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-green --describe
# Lag 상태에서 전환했으므로, Green이 log-end-offset부터 소비 시작
# Blue가 미소비한 메시지(Lag 부분)는 유실 가능성 있음

# Validator로 확인
kubectl port-forward -n monitoring svc/loki 3100:3100 &
LOKI_PF_PID=$!
sleep 2

TEST_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
python3 tools/validator/validator.py \
  --source loki \
  --loki-url http://localhost:3100 \
  --start "$SCENARIO3_START" \
  --end "$TEST_END" \
  --switch-start "$SCENARIO3_START" \
  --switch-end "$SWITCH_END" \
  --strategy B \
  --output report/validation-b-scenario3.md

kill $LOKI_PF_PID 2>/dev/null
```

### 검증 기준

| 항목 | 기준 |
|------|------|
| 전환 완료 시간 | < 60초 |
| Lag 상태 전환 시 메시지 유실 | **측정 및 기록** (전략 B 한계) |
| Green 시작 후 Lag 회복 | < 2분 |

> **전략 C vs 전략 B 비교:**
> - 전략 C: Lag 상태에서도 같은 Group이므로 Green이 이어서 소비 (유실 없음)
> - 전략 B: 별도 Group + `--to-current`이므로 Lag 메시지 건너뜀 가능

---

## 시나리오 4: Rebalance 관찰 (전략 B 고유)

### 목표

전략 B는 별도 Consumer Group을 사용하므로, 한쪽의 Rebalance가 다른 쪽에 영향을 주지 않는지 확인한다.
또한 Green scale up 시 Group 내 Rebalance 과정을 관찰한다.

### 사전 준비

시나리오 3에서 Green=ACTIVE 상태이므로, Blue=ACTIVE로 복원한다:

```bash
switch_to_blue

# 안정화 대기
sleep 30
check_b_status
check_offsets
```

### 수행 — Green Scale Up 시 Rebalance 관찰

```bash
SCENARIO4_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)

# 1. Blue가 정상 소비 중인 상태에서 Green을 scale up
echo "[Step 1] Green scale up (3 replicas)..."
kubectl scale deployment consumer-b-green -n kafka-bg-test --replicas=3

# 2. Green Pod 시작 대기
kubectl rollout status deployment consumer-b-green -n kafka-bg-test --timeout=120s

# 3. Green Group 내 Rebalance 관찰
echo ""
echo "[Step 3] Green Group Rebalance 로그 확인..."
kubectl logs -n kafka-bg-test -l strategy=b,color=green --tail=30 | \
  grep -iE "rebalance|assign|revok|join" | head -20
# 기대: Green Group 내에서 8개 파티션이 3개 Pod에 분배되는 Rebalance 로그
```

### Blue 격리 확인

```bash
# 4. Blue Consumer가 Green의 Rebalance에 영향받지 않는지 확인
echo ""
echo "[Step 4] Blue Consumer Group 상태 (영향 없어야 함)..."
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-blue --describe
# 기대: Blue 파티션 할당 변경 없음, LAG 안정적

# 5. Green Consumer Group 상태
echo ""
echo "[Step 5] Green Consumer Group 상태..."
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-green --describe
# 기대: 8개 파티션이 3개 Pod에 분배됨
```

### Blue Scale Down 시 격리 확인

```bash
# 6. Blue scale down → Green에 영향 없음 확인
echo ""
echo "[Step 6] Blue scale down..."
kubectl scale deployment consumer-b-blue -n kafka-bg-test --replicas=0
sleep 10

# 7. Green 상태 확인 (Blue 종료에 영향 없어야 함)
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-green --describe
# 기대: Green 파티션 할당 변화 없음 (별도 Group이므로)

echo ""
echo "[Step 7] Green 로그에서 Rebalance 미발생 확인..."
kubectl logs -n kafka-bg-test -l strategy=b,color=green --tail=10 --since=30s | \
  grep -iE "rebalance|assign|revok" | head -5
# 기대: Blue shutdown으로 인한 Rebalance 로그 없음 (별도 Group이므로)
```

### 정리

```bash
# Blue 복원
kubectl scale deployment consumer-b-blue -n kafka-bg-test --replicas=3
kubectl rollout status deployment consumer-b-blue -n kafka-bg-test --timeout=120s

# Green scale down
kubectl scale deployment consumer-b-green -n kafka-bg-test --replicas=0
sleep 5

# ConfigMap 업데이트
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"blue"}}'

check_b_status
```

### 검증 기준

| 항목 | 기준 |
|------|------|
| Green scale up 시 Green Group 내 Rebalance 발생 | 확인 (정상) |
| Green Rebalance 시 Blue Group 영향 | 0 (격리 확인) |
| Blue scale down 시 Green Group 영향 | 0 (격리 확인) |

> **전략 C vs 전략 B 비교:**
> - 전략 C: 같은 Group이므로 한쪽 Pod 변동이 전체 Rebalance 유발
> - 전략 B: **별도 Group이므로 완전 격리** — 이것이 전략 B의 핵심 장점

---

## 시나리오 5: 전환 실패 후 수동 롤백

### 목표

Green Consumer에 높은 에러율(80%)을 주입한 상태에서 전환하고,
에러를 관찰한 후 수동으로 롤백하는 절차를 검증한다.

### 사전 준비 — Blue ACTIVE 확인

```bash
check_b_status
# 기대: consumer-b-blue READY 3, consumer-b-green READY 0
```

### Green에 에러율 사전 주입

Green은 현재 replicas=0이므로, 전환 후 scale up된 Green Pod에 에러율을 주입해야 한다.
따라서 **먼저 전환하고, Green이 시작된 후 에러율을 주입**한다.

```bash
SCENARIO5_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Scenario 5 start: $SCENARIO5_START"

# 1. Blue→Green 전환 실행
switch_to_green

# 2. Green Pod가 Ready 상태인지 확인
check_b_status
# 기대: consumer-b-green READY 3
```

### Green에 에러율 주입

```bash
# 3. Green Pod에 80% 에러율 주입
echo "Green에 에러율 80% 주입..."
inject_fault consumer-b-green error-rate '{"errorRatePercent":80}'
```

### 에러 관찰

```bash
# 4. 30초 대기 후 Green 로그에서 에러 확인
sleep 30

echo "=== Green Consumer 에러 로그 ==="
kubectl logs -n kafka-bg-test -l strategy=b,color=green --tail=20 | \
  grep -iE "error|exception|fail" | head -10

# 5. Green Consumer Lag 확인 (에러로 인해 Lag 증가)
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-green --describe
# 기대: 에러율 80%로 인해 LAG 급증
```

> **참고:** 전략 B에서는 Switch Controller를 사용하지 않으므로, 자동 롤백은 구현되어 있지 않다.
> 에러 상황을 감지한 운영자가 수동으로 롤백해야 한다.

### 수동 롤백

```bash
# 6. 에러 확인 후 수동 롤백 결정
ROLLBACK_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Manual rollback start: $ROLLBACK_START"

# 에러율 해제 먼저 (Green scale down 시 필요 없지만 정리 차원)
clear_fault consumer-b-green error-rate

# Green→Blue 롤백
switch_to_blue

ROLLBACK_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Manual rollback end: $ROLLBACK_END"
echo "Rollback duration: $(( $(date -d "$ROLLBACK_END" +%s) - $(date -d "$ROLLBACK_START" +%s) )) seconds"
```

### 검증

```bash
# Blue 정상 소비 확인
sleep 60
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-blue --describe
# 기대: LAG ≈ 0
```

### 검증 기준

| 항목 | 기준 |
|------|------|
| 자동 롤백 수행 여부 | 미구현 (전략 B는 수동 전환) |
| 수동 롤백 후 Blue 정상 소비 재개 | 필수 |
| 메시지 유실 | 측정 및 기록 |
| 롤백 완료 시간 | < 60초 |

---

## 결과 요약 템플릿

각 시나리오 수행 후 아래 표를 채워 `report/` 디렉토리에 저장한다.

```markdown
| 시나리오 | 전환 시간 | 롤백 시간 | 유실 | 중복 | 결과 |
|----------|-----------|-----------|------|------|------|
| 1. 정상 전환 | _초 | - | _건 | _건 | PASS/FAIL |
| 2. 즉시 롤백 | - | _초 | _건 | _건 | PASS/FAIL |
| 3. Lag 중 전환 | _초 | - | _건 | _건 | PASS/FAIL |
| 4. Rebalance 관찰 | N/A | N/A | N/A | N/A | 관찰 기록 |
| 5. 수동 롤백 | _초 | _초 | _건 | _건 | PASS/FAIL |
```

---

## 정리 및 전략 C 원복

테스트 완료 후 전략 C 리소스를 복원하려면:

```bash
# 1. 전략 B 리소스 정리
kubectl scale deployment consumer-b-blue consumer-b-green -n kafka-bg-test --replicas=0
kubectl delete -f k8s/strategy-b/ 2>/dev/null || true

# 2. 전략 C 리소스 복원
kubectl scale statefulset consumer-blue -n kafka-bg-test --replicas=3
kubectl scale statefulset consumer-green -n kafka-bg-test --replicas=3
kubectl scale deployment bg-switch-controller -n kafka-bg-test --replicas=1

# 3. 전략 C 상태 확인
kubectl rollout status statefulset consumer-blue -n kafka-bg-test --timeout=120s
kubectl rollout status statefulset consumer-green -n kafka-bg-test --timeout=120s

# 4. ConfigMap active=blue 설정 (전략 C 기본값)
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"blue"}}'

# 5. 전략 C 정상 동작 확인
sleep 30
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  curl -s http://localhost:8080/lifecycle/status
# 기대: {"state":"ACTIVE",...}
```

---

## 트러블슈팅

### Green scale up 후 소비가 시작되지 않음

```bash
# Green Consumer Group 상태 확인
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-green --describe
# "Consumer group 'bg-test-group-green' has no active members" 가 나오면
# Green Pod가 아직 Ready가 아닌 것

# Pod 상태 확인
kubectl get pods -n kafka-bg-test -l strategy=b,color=green
kubectl describe pod -n kafka-bg-test -l strategy=b,color=green | tail -20
```

### Offset reset 실패 (Group is not empty)

```bash
# Consumer Group에 active member가 있으면 reset 불가
# 먼저 해당 그룹의 모든 Consumer를 중지해야 함
kubectl scale deployment consumer-b-green -n kafka-bg-test --replicas=0
sleep 10

# 다시 시도
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-green --topic bg-test-topic \
    --reset-offsets --to-current --execute
```

### port-forward가 끊어짐

`port-forward`는 일시적이므로 끊어질 수 있다. 재실행하면 된다:

```bash
# 특정 Pod에 대한 port-forward
kubectl port-forward -n kafka-bg-test <pod-name> 18080:8080 &
```

### 전략 C와 전략 B Consumer Group 충돌

전략 C(`bg-test-group`)와 전략 B(`bg-test-group-blue`, `bg-test-group-green`)는
서로 다른 Consumer Group이므로 충돌하지 않는다. 다만 같은 Topic을 소비하므로,
**양쪽이 동시에 ACTIVE이면 메시지가 각각 한 번씩 소비됨** (중복이 아닌 독립 소비).

전략 B 테스트 시 전략 C를 scale down하는 것은 **리소스 절약**이 주 목적이다.

---

## 다음 단계

- [08-strategy-e-test.md](08-strategy-e-test.md): 전략 E 테스트 수행 (Kafka Connect 기반)
