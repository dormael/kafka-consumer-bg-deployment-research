# 테스트 가이드: 실패를 줄이기 위한 실전 체크리스트

> **목적:** Task 05(전략 C) 테스트 경험에서 발견된 실패 원인을 정리하여 Task 06(전략 B), Task 07(전략 E) 테스트 시 반복 실패를 방지한다.
> **최종 수정:** 2026-02-21

---

## 목차

1. [환경 정보 빠른 참조](#1-환경-정보-빠른-참조)
2. [테스트 전 필수 점검표](#2-테스트-전-필수-점검표)
3. [자주 실패하는 원인 TOP 10](#3-자주-실패하는-원인-top-10)
4. [HTTP 클라이언트 제약과 해결책](#4-http-클라이언트-제약과-해결책)
5. [전략별 구조적 차이와 함정](#5-전략별-구조적-차이와-함정)
6. [Validator 결과 해석 가이드](#6-validator-결과-해석-가이드)
7. [시나리오별 주의사항 매트릭스](#7-시나리오별-주의사항-매트릭스)
8. [알려진 버그와 우회 방법](#8-알려진-버그와-우회-방법)
9. [헬퍼 스크립트](#9-헬퍼-스크립트)
10. [시나리오 실행 후 상태 복원 절차](#10-시나리오-실행-후-상태-복원-절차)
11. [리소스 제약과 타이밍 가이드](#11-리소스-제약과-타이밍-가이드)
12. [트러블슈팅 의사결정 트리](#12-트러블슈팅-의사결정-트리)

---

## 1. 환경 정보 빠른 참조

| 항목 | 값 |
|------|-----|
| **K8s 버전** | v1.23.8 (EOL, 단일 노드) |
| **Kafka 버전** | 3.8.0 (KRaft, Strimzi 0.43.0) |
| **Kafka Broker Pod** | `kafka-cluster-dual-role-0` (namespace: `kafka`) |
| **Topic** | `bg-test-topic` (8 partitions, RF=1) |
| **Consumer Group (전략 C)** | `bg-test-group` (Blue+Green 공유) |
| **Consumer Group (전략 B)** | `bg-test-group-blue`, `bg-test-group-green` (별도) |
| **Producer TPS** | 100 msg/sec |
| **Grafana** | `http://192.168.58.2:30080` (admin / admin123) |
| **Loki** | `svc/loki:3100` (namespace: `monitoring`) |
| **minikube 프로필** | `kafka-bg-test` |

### 핵심 타임아웃 값

| 파라미터 | 값 | 영향 |
|----------|-----|------|
| `session.timeout.ms` | 45,000ms | Static Membership: Pod 부재 시 이 시간 후 Rebalance |
| `heartbeat.interval.ms` | 15,000ms | Broker에 heartbeat 전송 주기 |
| `max.poll.interval.ms` | 300,000ms | 5분 내 poll() 미호출 시 Group에서 제외 |
| `terminationGracePeriodSeconds` | 60s | Pod 종료 유예 시간 |
| Consumer 기동 시간 | ~17초 | Spring Boot + Kafka Consumer 초기화 |
| Sidecar 재시도 | 3회 (포기 후 미재시도) | P1 버그 — Consumer 기동 전 실패하면 끝 |

---

## 2. 테스트 전 필수 점검표

> **규칙: 6개 모두 통과해야 테스트를 시작한다.** 하나라도 실패하면 테스트 결과를 신뢰할 수 없다.

```bash
# --- 점검 1: 모든 Pod Running/Ready ---
kubectl get pods -n kafka-bg-test
# 기대: 모든 Pod STATUS=Running, READY=1/1 또는 2/2

kubectl get pods -n kafka
# 기대: kafka-cluster-dual-role-0 STATUS=Running

kubectl get pods -n monitoring
# 기대: prometheus, grafana, loki, promtail 모두 Running

# --- 점검 2: Producer 동작 확인 ---
kubectl exec -n kafka-bg-test deploy/bg-test-producer -- \
  wget -qO- http://localhost:8080/producer/stats
# 기대: messagesPerSecond 값이 > 0, running: true

# --- 점검 3: Consumer 상태 확인 (전략 C 기준) ---
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  wget -qO- http://localhost:8080/lifecycle/status
# 기대: {"state":"ACTIVE","stateCode":0,...}

kubectl exec -n kafka-bg-test consumer-green-0 -c switch-sidecar -- \
  wget -qO- http://localhost:8080/lifecycle/status
# 기대: {"state":"PAUSED","stateCode":2,...}

# --- 점검 4: ConfigMap 상태 확인 ---
kubectl get configmap kafka-consumer-active-version -n kafka-bg-test \
  -o jsonpath='{.data.active}' && echo ""
# 기대: "blue" (초기 상태)

# --- 점검 5: Consumer Group Lag 확인 ---
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group --describe 2>/dev/null
# 기대: ACTIVE 측 파티션의 LAG ≈ 0

# --- 점검 6: Switch Controller 정상 동작 ---
kubectl get pods -n kafka-bg-test -l app=bg-switch-controller
# 기대: 1/1 Running
kubectl logs -n kafka-bg-test -l app=bg-switch-controller --tail=5
# 기대: 에러 메시지 없음
```

---

## 3. 자주 실패하는 원인 TOP 10

아래는 Task 05 테스트 중 실제로 실패/혼란을 야기한 원인을 빈도순으로 정리한 것이다.

### #1. Validator 결과를 "실제 유실"로 오해 (전략 C 전용)

- **증상:** Validator가 ~50% message loss를 보고
- **원인:** 전략 C에서 PAUSED 측에 할당된 파티션(~4/8개)은 소비되지 않음 — 구조적 특성
- **방지:** ACTIVE 측 파티션에서의 유실만 확인. Validator 결과에서 missing sequence가 어느 파티션에 속하는지 교차 확인 필요
- **전략 B에서는:** 별도 Consumer Group이므로 이 문제 없음

### #2. Container 내 wget으로 PUT 요청 시도

- **증상:** 장애 주입(processing-delay, error-rate)이 적용되지 않음
- **원인:** Container의 BusyBox `wget`은 PUT 메서드를 지원하지 않음
- **방지:** PUT 요청은 반드시 `kubectl port-forward` + 호스트 `curl -X PUT` 사용

### #3. Consumer Group ID 불일치

- **증상:** `kafka-consumer-groups.sh`에서 group 조회 시 데이터 없음
- **원인:** (수정 전) `@KafkaListener(id=...)` 에서 `groupId` 미지정 시 id가 group.id로 사용됨
- **방지:** Consumer 로그에서 실제 group.id 확인
  ```bash
  kubectl logs -n kafka-bg-test consumer-blue-0 -c consumer | grep -i "group"
  # "bg-test-group"이 보여야 함
  ```
- **상태:** B1 버그로 수정 완료. 재빌드/재배포 확인 필요

### #4. Switch Controller JSON 필드명 불일치

- **증상:** Switch Controller가 전환 시 영원히 타임아웃
- **원인:** (수정 전) Go 구조체의 `json:"status"`와 Consumer API의 `"state"` 불일치
- **방지:** Controller 로그에서 `timeout waiting for state` 발생 시 Consumer API 응답과 Go 코드 비교
- **상태:** B2 버그로 수정 완료

### #5. Pod 재시작 후 Dual-Active (P0 미수정)

- **증상:** PAUSED 측 Pod 재시작 후 양쪽 모두 ACTIVE
- **원인:** `INITIAL_STATE=ACTIVE`가 정적 env var → ConfigMap 무시
- **방지:** 시나리오 4 테스트 시 수동 pause 필요 (아래 [8절](#8-알려진-버그와-우회-방법) 참조)

### #6. port-forward 종료 시 에러 출력

- **증상:** `error copying from local connection to remote stream` 에러
- **원인:** `kill`/`pkill`로 port-forward를 종료하면 Go 런타임이 연결 정리 중 에러 출력
- **방지:** 무시해도 됨. API 호출은 이미 성공 완료 상태

### #7. Sidecar 초기 연결 실패 (P1)

- **증상:** Consumer 재시작 후 Sidecar가 lifecycle 명령을 전달하지 못함
- **원인:** Consumer 기동(~17초) 전에 Sidecar가 3회 재시도 후 포기
- **방지:** Consumer Pod 재시작 후 30초 대기, 필요 시 수동 lifecycle 호출

### #8. Loki 쿼리 시 빈 결과

- **증상:** Validator 실행 시 producer/consumer 로그가 0건
- **원인:** Loki port-forward 미실행, 또는 시간 범위가 잘못됨
- **방지:**
  1. `kubectl port-forward -n monitoring svc/loki 3100:3100 &` 확인
  2. 시간은 UTC 기준 (`date -u +%Y-%m-%dT%H:%M:%SZ`)
  3. Loki 직접 쿼리로 데이터 존재 확인:
     ```bash
     curl -s "http://localhost:3100/loki/api/v1/query_range" \
       --data-urlencode 'query={namespace="kafka-bg-test",container="consumer"}' \
       --data-urlencode 'start=2026-02-21T06:50:00Z' \
       --data-urlencode 'end=2026-02-21T07:00:00Z' \
       --data-urlencode 'limit=5' | python3 -m json.tool | head -20
     ```

### #9. Java 빌드 시 "invalid target release: 17"

- **증상:** `mvn package` 실패
- **원인:** 호스트 기본 Java가 8인 경우
- **방지:**
  ```bash
  JAVA_HOME=/usr/lib/jvm/java-17-openjdk-amd64 mvn package -DskipTests -B
  ```

### #10. minikube 이미지 빌드 방식 혼동

- **증상:** `ImagePullBackOff` — K8s가 이미지를 찾지 못함
- **원인:** containerd 런타임에서 `eval $(minikube docker-env)` 방식 불가
- **방지:** 반드시 `minikube -p kafka-bg-test image build` 사용
  ```bash
  minikube -p kafka-bg-test image build -t bg-test-consumer:latest apps/consumer/
  ```

---

## 4. HTTP 클라이언트 제약과 해결책

| 용도 | HTTP 메서드 | 방법 | 예시 |
|------|------------|------|------|
| 상태 확인 | GET | Pod 내 `wget -qO-` | `kubectl exec ... -c switch-sidecar -- wget -qO- http://localhost:8080/lifecycle/status` |
| 장애 주입 | PUT | `port-forward` + `curl` | 아래 참조 |
| pause/resume | POST | `port-forward` + `curl` 또는 Controller가 자동 수행 | 아래 참조 |

### 장애 주입 패턴 (PUT 요청)

```bash
# 1. 개별 Pod에 port-forward (Service 경유 시 로드밸런싱 문제)
for i in 0 1 2; do
  kubectl port-forward -n kafka-bg-test consumer-blue-$i 1808$i:8080 &
done
sleep 2

# 2. 장애 주입
for i in 0 1 2; do
  curl -s -X PUT http://localhost:1808$i/fault/processing-delay \
    -H "Content-Type: application/json" -d '{"delayMs": 200}'
done

# 3. port-forward 종료
pkill -f "kubectl port-forward.*consumer-blue" 2>/dev/null
```

> **주의:** `svc/consumer-blue-svc`로 port-forward하면 로드밸런싱되어 일부 Pod에만 적용될 수 있다. 각 Pod에 **개별** port-forward하는 것이 확실하다.

### 수동 lifecycle 제어 패턴 (POST 요청)

```bash
kubectl port-forward -n kafka-bg-test consumer-blue-0 18080:8080 &
sleep 2
curl -s -X POST http://localhost:18080/lifecycle/pause
# 또는: curl -s -X POST http://localhost:18080/lifecycle/resume
kill %1 2>/dev/null
```

---

## 5. 전략별 구조적 차이와 함정

### 전략 비교 요약

| 항목 | 전략 C (Pause/Resume) | 전략 B (Offset Sync) | 전략 E (Kafka Connect) |
|------|----------------------|---------------------|----------------------|
| Consumer Group | 1개 공유 (`bg-test-group`) | 2개 별도 (`-blue`, `-green`) | Connect 내부 Group |
| 전환 메커니즘 | Pause → Resume | Scale Down → Offset Sync → Scale Up | Connector Stop/Start |
| 전환 시간 | ~1초 | ~30초~2분 | TBD |
| PAUSED 측 Lag | **항상 누적** (구조적) | 없음 (비활성 Group) | 없음 |
| Dual-Active 위험 | Sidecar/Controller 로직에 의존 | Scale 순서에 의존 | Connector 상태에 의존 |
| Rebalance 영향 | 같은 Group → 전체 영향 | 각 Group 독립 | Connect 내부 관리 |

### 전략 C 함정: 파티션 분배와 "유실"

```
bg-test-group (6 consumers, 8 partitions):
  Blue-0: partition 0, 1  ← ACTIVE → 소비 중
  Blue-1: partition 2     ← ACTIVE → 소비 중
  Blue-2: partition 3     ← ACTIVE → 소비 중
  Green-0: partition 4, 5 ← PAUSED → Lag 누적 (유실 아님)
  Green-1: partition 6    ← PAUSED → Lag 누적 (유실 아님)
  Green-2: partition 7    ← PAUSED → Lag 누적 (유실 아님)
```

- Validator 기준 ~50% "missing" = **PAUSED 파티션의 미소비 메시지**
- 전환 후 역할이 바뀌면 이전 ACTIVE 측이 PAUSED가 되어 다시 Lag 누적
- **ACTIVE 측 파티션에서만 유실 0건 확인**이 올바른 검증 방법

### 전략 B 함정: Offset 동기화 Gap

```
전환 절차:
  1. Blue Scale Down (소비 중단)    ← 이 시점 Blue의 committed offset = X
  2. Green Offset을 X로 동기화
  3. Green Scale Up                 ← 이 시점부터 X+1 소비 시작

  문제: Step 1~3 사이 Producer가 계속 전송 (100 msg/s × ~30s = ~3000 messages)
  → Green이 Scale Up되면 이 Gap 메시지를 모두 소비해야 함 (유실은 아님, 지연일 뿐)
```

- **주의:** `--reset-offsets --to-current`는 **Blue Group의 현재 committed offset**을 기준으로 동기화
- Blue Scale Down 전에 `commitSync()`가 완료되었는지 확인 필요 (약 5초 대기)
- **Offset 동기화 실행 시 Green Group에 활성 Consumer가 없어야 함** (replicas=0 상태에서만 실행)

### 전략 E 함정: (Task 07 대비 사전 정리)

- Kafka Connect는 내부적으로 자체 Consumer Group 사용 (`connect-<connector-name>`)
- Connector를 STOPPED 상태로 생성하려면 Kafka 3.5+ 필요 (KIP-980) → 3.8.0에서 충족
- **Config Topic 이름**이 Blue/Green Connect 클러스터 간 다른지 확인 필요

---

## 6. Validator 결과 해석 가이드

### 정상 결과 예시 (전략 C)

```
Total Produced: 30,000
Total Consumed: 14,952        ← ~50%만 소비됨 (PAUSED 파티션 때문)
Missing (Loss): 15,049 (50%)  ← 구조적 특성, 실제 유실 아님
Duplicates: 1 (0.007%)        ← at-least-once 보장에 의한 중복, 허용 범위
Verdict: FAIL                 ← Validator는 기계적으로 FAIL 판정
```

### 전략 C 결과 해석 방법

1. **Missing이 ~50%이면** → PAUSED 파티션 미소비 (정상)
2. **Missing이 50% 훨씬 초과하면** → 실제 유실 가능성 조사 필요
3. **Duplicates < 1%이면** → 정상 (at-least-once 보장)
4. **Duplicates > 5%이면** → Rebalance 중 중복 소비 의심

### 전략 B 결과 해석 방법

1. **Missing이 0%에 가까워야** → 별도 Group이므로 전체 파티션 소비 가능
2. **Missing이 있다면** → Offset 동기화 Gap 또는 Scale Up 지연 의심
3. **Duplicates** → Offset 동기화 후 일부 메시지가 Blue/Green 양쪽에서 소비될 수 있음

### Validator 실행 시 공통 주의사항

| 항목 | 주의 |
|------|------|
| 시간대 | 반드시 UTC (`date -u +%Y-%m-%dT%H:%M:%SZ`) |
| Loki 접근 | `kubectl port-forward -n monitoring svc/loki 3100:3100 &` 먼저 실행 |
| 시간 범위 | `--start`는 전환 전 최소 1분, `--end`는 전환 후 최소 2분 |
| 전략 지정 | `--strategy C` 또는 `--strategy B` 정확히 지정 |

---

## 7. 시나리오별 주의사항 매트릭스

### 전략 C 시나리오

| 시나리오 | 핵심 주의사항 | 흔한 실수 | 검증 기준 |
|----------|-------------|----------|-----------|
| **1. 정상 전환** | PAUSED 측 Lag은 구조적 | Validator "FAIL"을 실패로 오해 | 전환 < 5초, ACTIVE 유실 0 |
| **2. 즉시 롤백** | 롤백 전 Blue→ACTIVE 복원 확인 | 이전 시나리오 상태에서 바로 시작 | 롤백 < 5초, Blue Lag < 100 |
| **3. Lag 중 전환** | 장애 주입은 PUT (port-forward 필수) | wget으로 PUT 시도 | 전환 시간 = 정상 시와 동일 |
| **4. Rebalance** | P0 Dual-Active 발생 가능 | 수동 pause 없이 다음 시나리오 진행 | Static Membership 동작 확인 |
| **5. 자동 롤백** | 자동 롤백 미구현 (P2) | 자동 롤백 발생을 기대하고 대기 | 수동 롤백 후 복구 확인 |

### 전략 B 시나리오

| 시나리오 | 핵심 주의사항 | 흔한 실수 | 검증 기준 |
|----------|-------------|----------|-----------|
| **1. 정상 전환** | Offset 동기화 순서 중요 | Green에 활성 Consumer 있는 상태에서 offset reset | 전환 < 30초, 유실 0 |
| **2. 즉시 롤백** | 롤백도 동일한 Offset Sync 절차 | 단순히 ConfigMap만 변경 | 롤백 < 60초 |
| **3. Lag 중 전환** | Blue Scale Down 전 commitSync 확인 | 즉시 Scale Down | committed offset 정확성 |
| **4. Rebalance** | Green Group 내부 Rebalance만 발생 | Blue Group에 영향 있다고 오해 | Group 격리 확인 |
| **5. 자동 롤백** | Argo Rollouts 연동 여부 확인 | Controller 자동 롤백 기대 | Argo 또는 수동 롤백 |

---

## 8. 알려진 버그와 우회 방법

### P0: PAUSED 측 Pod 재시작 시 Dual-Active

**영향 시나리오:** 시나리오 4 (Rebalance 장애 주입)

**근본 원인:**
```yaml
# StatefulSet의 정적 env var
- name: INITIAL_STATE
  value: "ACTIVE"   # ← ConfigMap 상태와 무관하게 항상 ACTIVE로 시작
```

**우회 방법 (테스트 중 수동 복구):**
```bash
# 재시작된 Pod를 수동으로 pause
kubectl port-forward -n kafka-bg-test consumer-blue-0 18080:8080 &
sleep 2
curl -s -X POST http://localhost:18080/lifecycle/pause
kill %1 2>/dev/null

# 전체 Blue를 일괄 pause (Dual-Active 발생 시)
for i in 0 1 2; do
  kubectl port-forward -n kafka-bg-test consumer-blue-$i 1808$i:8080 &
done
sleep 2
for i in 0 1 2; do
  curl -s -X POST http://localhost:1808$i/lifecycle/pause
done
pkill -f "kubectl port-forward.*consumer-blue" 2>/dev/null
```

### P1: Sidecar 초기 연결 실패

**영향 시나리오:** Pod 재시작이 포함된 모든 시나리오

**근본 원인:** Consumer 기동(~17초) 전에 Sidecar가 3회 재시도(기본 간격) 후 포기. 이후 ConfigMap 변경이 없으면 재시도하지 않음.

**우회 방법:**
```bash
# Consumer Pod 재시작 후 30초 대기 (Consumer 기동 완료 확인)
kubectl wait --for=condition=Ready pod/consumer-blue-0 \
  -n kafka-bg-test --timeout=60s

# 이후 수동 lifecycle 제어 또는 ConfigMap을 더미 변경하여 Sidecar 재트리거
# 더미 변경 (active 값을 같은 값으로 patch — Sidecar는 변경 이벤트를 받음)
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"trigger":"dummy"}}'
```

### P2: 자동 롤백 미구현

**영향 시나리오:** 시나리오 5 (전환 실패 후 자동 롤백)

**현재 상태:** Switch Controller는 lifecycle 상태(ACTIVE/PAUSED)만 확인. 에러율/Lag 기반 자동 롤백 없음.

**테스트 방법:** 자동 롤백 미동작을 확인한 후 **수동 롤백** 수행.

---

## 9. 헬퍼 스크립트

테스트 시작 전 터미널에 아래 함수를 등록하면 반복 명령을 단축할 수 있다.

```bash
# === Consumer 상태 확인 ===
check_status() {
  local pod=$1
  kubectl exec -n kafka-bg-test "$pod" -c switch-sidecar -- \
    wget -qO- http://localhost:8080/lifecycle/status 2>/dev/null
}

check_all() {
  echo "=== Blue ==="
  for pod in consumer-blue-0 consumer-blue-1 consumer-blue-2; do
    echo -n "$pod: "; check_status "$pod"; echo ""
  done
  echo "=== Green ==="
  for pod in consumer-green-0 consumer-green-1 consumer-green-2; do
    echo -n "$pod: "; check_status "$pod"; echo ""
  done
}

# === Consumer Lag 확인 ===
check_lag() {
  local group=${1:-bg-test-group}
  kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
    bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
      --group "$group" --describe 2>/dev/null
}

# === Switch Controller 로그 ===
sc_logs() {
  local lines=${1:-20}
  kubectl logs -n kafka-bg-test -l app=bg-switch-controller --tail="$lines"
}

# === ConfigMap 현재 상태 ===
check_active() {
  kubectl get configmap kafka-consumer-active-version -n kafka-bg-test \
    -o jsonpath='{.data.active}' && echo ""
}

# === 전환 트리거 ===
switch_to() {
  local color=$1
  echo "Switching to: $color at $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
    --type merge -p "{\"data\":{\"active\":\"$color\"}}"
}

# === 장애 주입/해제 (color, delay_ms) ===
inject_delay() {
  local color=$1 delay=${2:-200}
  for i in 0 1 2; do
    kubectl port-forward -n kafka-bg-test consumer-${color}-$i 1808$i:8080 &
  done
  sleep 2
  for i in 0 1 2; do
    echo "consumer-${color}-$i:"
    curl -s -X PUT http://localhost:1808$i/fault/processing-delay \
      -H "Content-Type: application/json" -d "{\"delayMs\": $delay}"
    echo ""
  done
  pkill -f "kubectl port-forward.*consumer-${color}" 2>/dev/null
}

inject_error_rate() {
  local color=$1 rate=${2:-80}
  for i in 0 1 2; do
    kubectl port-forward -n kafka-bg-test consumer-${color}-$i 1808$i:8080 &
  done
  sleep 2
  for i in 0 1 2; do
    echo "consumer-${color}-$i:"
    curl -s -X PUT http://localhost:1808$i/fault/error-rate \
      -H "Content-Type: application/json" -d "{\"errorRatePercent\": $rate}"
    echo ""
  done
  pkill -f "kubectl port-forward.*consumer-${color}" 2>/dev/null
}

# === Timestamp 기록 ===
timestamp() {
  date -u +%Y-%m-%dT%H:%M:%SZ
}
```

### 사용 예시

```bash
# 전체 Consumer 상태 확인
check_all

# Blue → Green 전환
switch_to green

# Switch Controller 로그 확인
sc_logs 10

# Consumer Lag 확인
check_lag                    # 전략 C: bg-test-group
check_lag bg-test-group-blue # 전략 B: blue group

# 장애 주입 (Blue에 200ms 지연)
inject_delay blue 200

# 장애 해제 (Blue 지연 0ms)
inject_delay blue 0

# Green에 에러율 80% 주입
inject_error_rate green 80

# Green 에러율 해제
inject_error_rate green 0
```

---

## 10. 시나리오 실행 후 상태 복원 절차

> **규칙:** 각 시나리오 종료 후 반드시 초기 상태(Blue=ACTIVE, Green=PAUSED)로 복원한 뒤 다음 시나리오를 시작한다.

### 전략 C 상태 복원

```bash
# 1. ConfigMap을 blue로 설정
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"blue"}}'

# 2. Switch Controller가 전환을 완료할 때까지 대기
sleep 10

# 3. 상태 확인
check_all
# 기대: Blue=ACTIVE, Green=PAUSED

# 4. Dual-Active인 경우 수동 복구 (Green pause 강제)
# check_all 결과에서 Green이 ACTIVE인 Pod가 있으면:
for i in 0 1 2; do
  kubectl port-forward -n kafka-bg-test consumer-green-$i 1808$i:8080 &
done
sleep 2
for i in 0 1 2; do
  curl -s -X POST http://localhost:1808$i/lifecycle/pause
done
pkill -f "kubectl port-forward.*consumer-green" 2>/dev/null

# 5. 장애 주입 해제 확인
inject_delay blue 0
inject_delay green 0
inject_error_rate blue 0
inject_error_rate green 0

# 6. StatefulSet replicas 복원 (시나리오 4에서 변경한 경우)
kubectl scale statefulset consumer-blue -n kafka-bg-test --replicas=3
kubectl scale statefulset consumer-green -n kafka-bg-test --replicas=3
sleep 30

# 7. 추가 안정화 대기
sleep 20

# 8. 최종 상태 확인
check_all
check_lag
```

### 전략 B 상태 복원

```bash
# 1. Green Scale Down
kubectl scale deployment consumer-b-green -n kafka-bg-test --replicas=0

# 2. Blue Scale Up (4 replicas)
kubectl scale deployment consumer-b-blue -n kafka-bg-test --replicas=4

# 3. ConfigMap 복원
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"blue"}}'

# 4. Blue Ready 대기
kubectl rollout status deployment consumer-b-blue -n kafka-bg-test --timeout=60s

# 5. Lag 확인
check_lag bg-test-group-blue
```

---

## 11. 리소스 제약과 타이밍 가이드

### 단일 노드 리소스 예산

| 카테고리 | CPU | Memory |
|----------|-----|--------|
| 인프라 (Prometheus, Grafana, Loki 등) | ~0.7 cores | ~1.4 Gi |
| Kafka (Broker + Exporter) | ~0.6 cores | ~1.2 Gi |
| 워크로드 (Producer, Consumer×6, Controller) | ~1.9 cores | ~2.3 Gi |
| **합계** | **~3.2 cores** | **~4.9 Gi** |

### 타이밍 참조

| 이벤트 | 예상 시간 | 비고 |
|--------|-----------|------|
| ConfigMap patch → Controller 감지 | < 1초 | Watch 기반 |
| Controller → Consumer pause/resume 완료 | ~1초 | HTTP 호출 |
| Consumer 기동 (Cold Start) | ~17초 | Spring Boot + Kafka 초기화 |
| StatefulSet Pod 재생성 | ~20~30초 | 이미지 pull 없음 (imagePullPolicy: Never) |
| Consumer Lag 안정화 (전환 후) | ~2분 | TPS 100 기준 |
| Static Membership session.timeout 도달 | 45초 | 이 전에 복귀하면 Rebalance 미발생 |
| Offset 동기화 (전략 B) | ~5초 | kafka-consumer-groups.sh 실행 시간 |
| Green Scale Up + Ready (전략 B) | ~30~60초 | Pod 생성 + JVM 기동 |

### 시나리오 간 안정화 대기

| 구간 | 최소 대기 시간 | 이유 |
|------|---------------|------|
| 전환 후 Lag 안정화 | 2분 | TPS 100 × 120초 = 12,000 msg 처리 |
| 시나리오 간 복원 | 30초 | Switch Controller 전환 + Consumer 상태 안정 |
| Pod 재시작 후 | 30초 | Consumer 기동(17초) + Sidecar 연결(5초) + 여유 |
| Scale Up 후 (전략 B) | 60초 | Rebalance + Consumer 기동 |
| 장애 주입 후 Lag 축적 | 2분 | delayMs=200 기준, Lag > 500 도달 |

---

## 12. 트러블슈팅 의사결정 트리

### Pod가 Running이 아닐 때

```
Pod STATUS가 Running이 아님
  ├─ CrashLoopBackOff
  │   ├─ 로그 확인: kubectl logs <pod> -c consumer --previous
  │   ├─ Java 관련 → JAVA_OPTS 메모리 부족? (-Xmx192m)
  │   └─ Kafka 연결 실패 → bootstrap-servers 확인
  ├─ ImagePullBackOff
  │   ├─ imagePullPolicy: Never 확인
  │   └─ minikube image ls | grep bg-test 로 이미지 존재 확인
  └─ Pending
      └─ kubectl describe pod <pod> → Events 확인 (리소스 부족?)
```

### Switch Controller 전환 실패 시

```
전환이 완료되지 않음
  ├─ Controller 로그에 "timeout waiting for state" 있음
  │   ├─ Consumer lifecycle API 직접 호출 → 응답 확인
  │   ├─ 응답이 빈 문자열 → Consumer Container 다운
  │   └─ 응답은 있으나 state 불일치 → JSON 필드명 확인 (B2 수정 여부)
  ├─ Controller 로그에 "lease acquisition failed" 있음
  │   ├─ 이전 Lease 만료 대기 (15초)
  │   └─ 다른 Controller 인스턴스가 Lease 보유 중인지 확인
  └─ Controller Pod가 없음
      └─ kubectl get deploy bg-switch-controller -n kafka-bg-test
```

### Validator 결과가 예상과 다를 때

```
Validator 결과 이상
  ├─ Produced = 0
  │   ├─ Loki port-forward 확인
  │   ├─ 시간 범위 UTC 확인
  │   └─ Producer Pod 로그에 JSON 전송 로그 있는지 확인
  ├─ Consumed = 0
  │   ├─ Consumer 로그에 수신 로그 있는지 확인
  │   └─ Consumer Group ID가 올바른지 확인 (B1 수정 여부)
  ├─ Missing ~50% (전략 C)
  │   └─ 정상 — PAUSED 파티션 미소비 (구조적 특성)
  ├─ Missing > 50% (전략 C)
  │   └─ 실제 유실 가능성 — 시간 범위 확인, Consumer 장애 확인
  └─ Duplicates > 5%
      └─ Rebalance 중 중복 소비 — Rebalance 이벤트 시각과 교차 확인
```

---

## 부록: 시나리오 실행 순서 체크리스트

각 시나리오 실행 시 아래 순서를 따른다:

```
□ 1. 점검표 6개 항목 확인 (2절)
□ 2. 시작 시각 기록: SWITCH_START=$(timestamp)
□ 3. (필요 시) 장애 주입
□ 4. ConfigMap patch 또는 Scale 변경으로 전환 트리거
□ 5. Switch Controller 로그 실시간 관찰 (별도 터미널)
□ 6. 전환 완료 확인: check_all
□ 7. 종료 시각 기록: SWITCH_END=$(timestamp)
□ 8. 안정화 대기 (2분)
□ 9. Consumer Lag 확인: check_lag
□ 10. Validator 실행 (Loki port-forward 필수)
□ 11. 장애 주입 해제
□ 12. 상태 복원 (10절)
□ 13. 결과 기록 (report/ 디렉토리)
```
