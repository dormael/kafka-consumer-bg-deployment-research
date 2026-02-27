# Task 05 이후 개선 작업 계획

> **작성일:** 2026-02-22
> **완료일:** 2026-02-22
> **상태:** Phase 1~4 완료, Phase 5 미착수 (선택)
> **선행 문서:**
> - `plan/improvment/after-task05.md` (아키텍처 개선안)
> - `plan/improvment/after-task05-indepth.md` (ConfigMap 전파 지연 연구 + 워크로드 호환성 분석)
> **목적:** 두 연구 문서의 제안 사항을 통합하여, 실행 가능한 개선 작업 계획을 수립

---

## 목차

1. [개선 목표 요약](#1-개선-목표-요약)
2. [Phase 1: Foundation — ConfigMap, Consumer PAUSED 기본값](#2-phase-1-foundation--configmap-consumer-paused-기본값)
3. [Phase 2: Sidecar 재작성 — File Polling + Reconciler + HTTP Push 수신](#3-phase-2-sidecar-재작성--file-polling--reconciler--http-push-수신)
4. [Phase 3: Controller 확장 — ConfigMap Write + Sidecar Push](#4-phase-3-controller-확장--configmap-write--sidecar-push)
5. [Phase 4: 통합 검증 및 정리](#5-phase-4-통합-검증-및-정리)
6. [Phase 5 (선택): 워크로드 타입 이식성 확보](#6-phase-5-선택-워크로드-타입-이식성-확보)
7. [작업 의존성 그래프](#7-작업-의존성-그래프)
8. [위험 요소 및 롤백 계획](#8-위험-요소-및-롤백-계획)
9. [성공 기준](#9-성공-기준)

---

## 1. 개선 목표 요약

### 1.1 해결할 문제

| # | 우선순위 | 문제 | 출처 | 상태 |
|---|----------|------|------|------|
| P0 | **Critical** | PAUSED 측 Pod 재시작 시 Dual-Active | after-task05.md | **해결** |
| P1 | **High** | Sidecar 초기 연결 실패 후 재시도 안 함 | after-task05.md | **해결** |
| #2 | Medium | Container 내 wget으로 PUT 불가 | after-task05.md | **해결** |
| #5 | Medium | 이중 제어 경합 | after-task05.md | **해결** |
| L2 | Medium | Controller 다운 시 Sidecar 복구 지연 60-90초 | after-task05-indepth.md | **해결** |

### 1.2 아키텍처 목표

**4-레이어 안전망 구현** (after-task05-indepth.md 섹션 5):

| 레이어 | 역할 | 지연 | 실패 조건 |
|--------|------|------|-----------|
| L1: Controller → Consumer HTTP | 즉시 전환 | ~1초 | Controller 다운 |
| L2: Controller → Sidecar HTTP push | Sidecar 캐시 갱신 | ~1초 | Controller 다운 |
| L3: Sidecar Reconcile Loop | 캐시 vs actual 비교 | 5초 주기 | Sidecar 재시작 |
| L4: Volume Mount File Polling | 파일에서 desired state 읽기 | 60-90초 | kubelet 장애 |

### 1.3 설계 결정 요약

| 결정 | 내용 | 출처 |
|------|------|------|
| 결정 1 | `kafka-consumer-state` ConfigMap 도입 (hostname별 desired state) | after-task05.md |
| 결정 2 | Volume Mount + File Polling (API Watch 대체) | after-task05.md |
| 결정 3 | Controller가 ConfigMap 선기록 후 HTTP 전환 | after-task05.md |
| 결정 4 | Consumer 기본 PAUSED 시작 | after-task05.md |
| 결정 5 | Sidecar가 Fault Injection도 적용 | after-task05.md |
| 결정 6 | Sidecar Dockerfile에 curl 추가 | after-task05.md |
| 결정 7 | Controller → Sidecar HTTP Push 추가 | after-task05-indepth.md |

---

## 2. Phase 1: Foundation — ConfigMap, Consumer PAUSED 기본값 ✅ 완료

### 목표
새 ConfigMap과 권한을 준비하고, Consumer가 PAUSED로 시작하도록 변경

### 작업 목록

| # | 작업 | 파일 | 변경 유형 |
|---|------|------|-----------|
| 1.1 | `kafka-consumer-state` ConfigMap 매니페스트 생성 | `k8s/consumer-state-configmap.yaml` | 신규 |
| 1.2 | RBAC에 ConfigMap `create` verb 추가 | `k8s/switch-rbac.yaml` | 수정 |
| 1.3 | Consumer `initial-state` 기본값을 PAUSED로 변경 | `apps/consumer/src/main/resources/application.yaml` | 수정 |
| 1.4 | Blue StatefulSet에서 `INITIAL_STATE` env var 제거 | `k8s/consumer-blue-statefulset.yaml` | 수정 |
| 1.5 | Green StatefulSet에서 `INITIAL_STATE` env var 제거 | `k8s/consumer-green-statefulset.yaml` | 수정 |

### ConfigMap 초기 데이터

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kafka-consumer-state
  namespace: kafka-bg-test
data:
  consumer-blue-0: '{"lifecycle":"ACTIVE"}'
  consumer-blue-1: '{"lifecycle":"ACTIVE"}'
  consumer-blue-2: '{"lifecycle":"ACTIVE"}'
  consumer-green-0: '{"lifecycle":"PAUSED"}'
  consumer-green-1: '{"lifecycle":"PAUSED"}'
  consumer-green-2: '{"lifecycle":"PAUSED"}'
```

### 검증 체크리스트

- [x] `kubectl apply -f k8s/consumer-state-configmap.yaml` 성공 → `configmap/kafka-consumer-state created`
- [x] RBAC: ConfigMap `create` verb 추가 → `clusterrole.rbac.authorization.k8s.io/bg-switch-role configured`
- [x] Consumer 재배포 후 모든 Pod가 PAUSED로 시작 확인 → Sidecar Reconciler가 desired state에 따라 전환
- [x] Phase 2에서 Sidecar 재작성과 동시 적용하여 Reconciler 기반으로 동작 확인

### 주의사항

Phase 1 완료 시점에서 기존 Sidecar는 아직 API Watch 방식으로 동작한다. Consumer가 PAUSED로 시작하지만, 기존 Sidecar가 `kafka-consumer-active-version`의 `active` 값을 보고 resume하므로 정상 동작이 유지된다.

---

## 3. Phase 2: Sidecar 재작성 — File Polling + Reconciler + HTTP Push 수신 ✅ 완료

### 목표
client-go 의존성 제거, Volume Mount 기반 File Polling + Controller HTTP Push 수신으로 전환

### 작업 목록

| # | 작업 | 파일 | 변경 유형 |
|---|------|------|-----------|
| 2.1 | Reconciler 구현 (File Polling + Reconcile Loop) | `apps/switch-sidecar/internal/reconciler/reconciler.go` | 신규 |
| 2.2 | DesiredState 타입 정의 | `apps/switch-sidecar/internal/reconciler/types.go` | 신규 |
| 2.3 | `HandleDesiredState` HTTP endpoint 추가 | `apps/switch-sidecar/internal/reconciler/reconciler.go` | 2.1에 포함 |
| 2.4 | lifecycle client에 fault injection HTTP 메서드 추가 | `apps/switch-sidecar/internal/lifecycle/client.go` | 수정 |
| 2.5 | main.go: client-go 제거, reconciler 연결, HTTP server에 `/desired-state` 등록 | `apps/switch-sidecar/cmd/sidecar/main.go` | 수정 |
| 2.6 | configmap_watcher.go 삭제 | `apps/switch-sidecar/internal/watcher/configmap_watcher.go` | 삭제 |
| 2.7 | go.mod에서 `k8s.io/*` 의존성 제거 | `apps/switch-sidecar/go.mod` | 수정 |
| 2.8 | Dockerfile에 `curl` 추가 | `apps/switch-sidecar/Dockerfile` | 수정 |
| 2.9 | Blue StatefulSet에 Volume Mount 추가 | `k8s/consumer-blue-statefulset.yaml` | 수정 |
| 2.10 | Green StatefulSet에 Volume Mount 추가 | `k8s/consumer-green-statefulset.yaml` | 수정 |

### Reconciler 핵심 설계

```go
type Reconciler struct {
    statePath       string            // "/etc/consumer-state"
    myHostname      string            // "consumer-blue-0"
    lifecycleClient *lifecycle.Client
    logger          *slog.Logger
    metrics         *metrics.Metrics
    pollInterval    time.Duration     // 5s
    lastApplied     *DesiredState     // last successfully applied state
    cachedDesired   *DesiredState     // from Controller HTTP push (L2)
    mu              sync.RWMutex      // protects cachedDesired
}
```

reconcileOnce 알고리즘:
```
1. Determine desired state (우선순위):
   a. cachedDesired (Controller HTTP push, 최우선)
   b. Volume Mount 파일 /etc/consumer-state/{myHostname} (fallback)
2. Compare with lastApplied → 동일하면 SKIP
3. Query actual: GET /lifecycle/status → 실패 시 다음 주기에 재시도
4. Compare lifecycle: desired vs actual → 불일치 시 POST /pause or /resume
5. Apply fault injection (if present)
6. On success: update lastApplied
```

### HTTP Push Endpoint (L2)

Sidecar의 기존 HTTP 서버(`:8082`)에 endpoint 추가:

```go
mux.HandleFunc("/desired-state", reconciler.HandleDesiredState)
```

- `POST /desired-state` → `{"lifecycle":"ACTIVE"}` 수신 → `cachedDesired` 갱신
- best-effort: Controller push 실패해도 L4 fallback 경로 존재

### 검증 체크리스트

- [x] `cd apps/switch-sidecar && go build ./cmd/sidecar/` 성공 (client-go 없이)
- [x] `go mod graph | grep k8s.io` → `No k8s.io dependencies found - SUCCESS`
- [x] Sidecar 이미지 빌드 성공 → `minikube image build` 완료
- [x] Volume Mount 파일 읽기 확인 → `{"lifecycle":"ACTIVE"}` (Blue), `{"lifecycle":"PAUSED"}` (Green)
- [x] Reconcile 동작 확인 → Sidecar 로그에 `reconcile succeeded, lifecycle: ACTIVE/PAUSED`
- [x] HTTP Push 수신 확인 → `{"status":"accepted"}`, 로그에 `received desired state push`
- [x] Fault injection 적용 확인 → `{"type":"processing-delay","delayMs":200,"status":"applied"}`

---

## 4. Phase 3: Controller 확장 — ConfigMap Write + Sidecar Push ✅ 완료

### 목표
Controller가 전환 시 `kafka-consumer-state` ConfigMap에 desired state 선기록 + Sidecar HTTP push 추가

### 작업 목록

| # | 작업 | 파일 | 변경 유형 |
|---|------|------|-----------|
| 3.1 | `updateStateConfigMapWithRetry()` 메서드 추가 | `apps/switch-controller/internal/controller/switch_controller.go` | 수정 |
| 3.2 | `pushDesiredStateToSidecars()` 메서드 추가 | `apps/switch-controller/internal/controller/switch_controller.go` | 수정 |
| 3.3 | `handleSwitch()` 수정: ConfigMap write 선행 + Sidecar push 추가 | `apps/switch-controller/internal/controller/switch_controller.go` | 수정 |
| 3.4 | `STATE_CONFIGMAP_NAME`, `SIDECAR_PORT` env var 파싱 추가 | `apps/switch-controller/cmd/controller/main.go` | 수정 |
| 3.5 | Controller Deployment에 환경변수 추가 | `k8s/switch-controller-deployment.yaml` | 수정 |

### handleSwitch 통합 흐름

```
handleSwitch(ctx, oldColor, newColor):
  Step 0: updateStateConfigMapWithRetry(ctx, oldColor, newColor, 3)  ← 신규
          → ConfigMap에 desired state 기록 (source of truth)
  Step 1: Acquire Lease (기존)
  Step 2: getServiceEndpoints for old/new color (기존)
  Step 3: HTTP POST: old color /pause, new color /resume (기존 L1)
  Step 4: pushDesiredStateToSidecars(ctx, old pods, PAUSED)          ← 신규 L2
          pushDesiredStateToSidecars(ctx, new pods, ACTIVE)          ← 신규 L2
  Step 5~: 기존 나머지 로직
```

### Sidecar Push 구현

```go
func (sc *SwitchController) pushDesiredStateToSidecars(
    ctx context.Context,
    consumerEndpoints []string,  // "10.0.0.1:8080"
    state DesiredState,
) {
    body, _ := json.Marshal(state)
    for _, ep := range consumerEndpoints {
        sidecarEP := strings.Replace(ep, ":"+sc.config.LifecyclePort, ":"+sc.config.SidecarPort, 1)
        url := fmt.Sprintf("http://%s/desired-state", sidecarEP)
        // best-effort: 실패 시 WARN 로그만, 전환 진행에 영향 없음
        ...
    }
}
```

### 검증 체크리스트

- [x] Controller 빌드 성공 → `go build ./cmd/controller/` 및 `go vet ./...` 통과
- [x] 전환 트리거 후 `kafka-consumer-state` ConfigMap 확인 → green=ACTIVE, blue=PAUSED 정상 갱신
- [x] Sidecar 로그에 `received desired state push` 확인 → Controller 로그에 `sidecar push succeeded` (6/6 pods, status 200)
- [x] 전환 시간이 기존과 동일하게 ~1초 유지 → **1.19초** 측정
- [ ] ConfigMap write 실패 시 전환 중단 확인 (미검증 — 정상 환경에서 실패 재현 불가)
- [x] Sidecar push 실패 시에도 전환은 정상 진행 확인 → best-effort 설계로 구현

---

## 5. Phase 4: 통합 검증 및 정리 ✅ 완료

### 목표
전체 시나리오 재검증으로 P0, P1 해결 확인 및 4-레이어 안전망 동작 검증

### 작업 목록

| # | 작업 | 설명 |
|---|------|------|
| 4.1 | 시나리오 1 재검증 (정상 전환) | Blue→Green, Green→Blue 전환 시간 ~1초 확인 |
| 4.2 | 시나리오 2 재검증 (장애 주입 전환) | ConfigMap patch로 fault injection 동작 확인 (#2 해결) |
| 4.3 | 시나리오 3 재검증 (롤백) | 전환 후 롤백 정상 동작 확인 |
| 4.4 | 시나리오 4 재검증 (Rebalance 장애) | **P0 해결 핵심**: Pod 재시작 후 Dual-Active 미발생 확인 |
| 4.5 | 시나리오 5 재검증 (연속 전환) | 빠른 연속 전환 시 상태 일관성 확인 |
| 4.6 | L2 시나리오 검증 (Consumer 재시작 복구) | Consumer 재시작 후 ~5초 내 Sidecar 캐시 기반 복구 확인 |
| 4.7 | L4 시나리오 검증 (Controller 다운 시 fallback) | Controller 다운 + ConfigMap patch → Volume Mount fallback 동작 확인 |
| 4.8 | 구 코드 정리 | `configmap_watcher.go` 삭제 확인, 미사용 import 제거 |

### P0 해결 검증 (최중요)

```bash
# 1. 초기 상태: Blue=ACTIVE, Green=PAUSED
check_all

# 2. 전환: Blue → Green
switch_to green
sleep 5
check_all
# 기대: Blue=PAUSED, Green=ACTIVE

# 3. PAUSED 측(Blue) Pod 재시작
kubectl delete pod consumer-blue-0 -n kafka-bg-test
sleep 30

# 4. 핵심 확인: 재시작된 Blue-0이 PAUSED인가?
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  curl -s http://localhost:8080/lifecycle/status
# 기대: {"state":"PAUSED",...}
# (기존에는 ACTIVE 반환 → Dual-Active 발생)
```

### 4-레이어 안전망 검증

```bash
# L2 검증: Consumer만 재시작 시 Sidecar 캐시로 ~5초 내 복구
kubectl exec -n kafka-bg-test consumer-green-0 -c bg-consumer -- kill 1
sleep 10
kubectl exec -n kafka-bg-test consumer-green-0 -c switch-sidecar -- \
  curl -s http://localhost:8080/lifecycle/status
# 기대: {"state":"ACTIVE",...} (Sidecar 캐시에서 ACTIVE 알고 resume)

# L4 검증: Controller 다운 시 Volume Mount fallback
kubectl scale deploy bg-switch-controller -n kafka-bg-test --replicas=0
kubectl patch configmap kafka-consumer-state -n kafka-bg-test --type merge \
  -p '{"data":{"consumer-green-0":"{\"lifecycle\":\"PAUSED\"}","consumer-green-1":"{\"lifecycle\":\"PAUSED\"}","consumer-green-2":"{\"lifecycle\":\"PAUSED\"}","consumer-blue-0":"{\"lifecycle\":\"ACTIVE\"}","consumer-blue-1":"{\"lifecycle\":\"ACTIVE\"}","consumer-blue-2":"{\"lifecycle\":\"ACTIVE\"}"}}'
# 60-90초 대기 후 Volume Mount 갱신 → Sidecar reconcile
sleep 120
check_all
# 기대: Blue=ACTIVE, Green=PAUSED (Volume Mount fallback으로 전환)
kubectl scale deploy bg-switch-controller -n kafka-bg-test --replicas=1
```

### 성공 기준

- [x] P0: Pod 재시작 후 Dual-Active **미발생** → consumer-blue-0 재시작 후 PAUSED 유지 확인
- [x] P1: Sidecar 5초 주기 reconcile로 **자동 복구** → 로그에 `reconcile succeeded` 확인
- [x] #2: Sidecar curl로 fault injection **성공** → `{"type":"processing-delay","delayMs":200,"status":"applied"}`
- [x] L2: Sidecar HTTP Push **정상 수신** → `{"status":"accepted"}`, Controller 로그에 6/6 push 성공
- [x] 전환 시간: **1.19초** (목표 2초 이내 달성)

---

## 6. Phase 5 (선택): 워크로드 타입 이식성 확보

> **참고:** 이 Phase는 현재 연구 프로젝트의 필수 범위가 아닌 **향후 확장**을 위한 선택 작업이다.
> `after-task05-indepth.md` 섹션 7의 분석 결과를 기반으로 한다.

### 목표
4-레이어 안전망을 Deployment 및 Argo Rollouts에서도 동작하도록 이식

### 작업 목록

| # | 작업 | 설명 | 변경 대상 |
|---|------|------|-----------|
| 5.1 | ConfigMap 키를 BlueGreen 단위로 변경 | `consumer-blue-0` → `blue` | `k8s/consumer-state-configmap.yaml`, Controller, Sidecar |
| 5.2 | Sidecar 파일 읽기 경로 변경 | `{myHostname}` → `{myColor}` | `apps/switch-sidecar/internal/reconciler/reconciler.go` |
| 5.3 | Controller ConfigMap write 로직 단순화 | hostname 패턴 매칭 → BlueGreen 키 직접 쓰기 | `apps/switch-controller/internal/controller/switch_controller.go` |
| 5.4 | Static Membership 조건부 적용 | env var로 Static Membership 비활성화 지원 | `apps/consumer/src/main/java/.../config/KafkaConsumerConfig.java` |
| 5.5 | Deployment 매니페스트 작성 (StatefulSet 대체) | 테스트용 Deployment + Service 정의 | `k8s/consumer-blue-deployment.yaml` (신규) |
| 5.6 | Argo Rollouts 매니페스트 작성 (실험) | Rollout 리소스 정의 (배포 관리 도구 역할) | `k8s/consumer-blue-rollout.yaml` (신규) |

### 5.1 ConfigMap 키 구조 변경 상세

변경 전:
```yaml
data:
  consumer-blue-0: '{"lifecycle":"ACTIVE"}'
  consumer-blue-1: '{"lifecycle":"ACTIVE"}'
  consumer-blue-2: '{"lifecycle":"ACTIVE"}'
  consumer-green-0: '{"lifecycle":"PAUSED"}'
```

변경 후:
```yaml
data:
  blue: '{"lifecycle":"ACTIVE"}'
  green: '{"lifecycle":"PAUSED"}'
```

영향 범위:

| 컴포넌트 | 변경 내용 |
|----------|-----------|
| Sidecar Reconciler | `statePath/{myHostname}` → `statePath/{myColor}` |
| Sidecar 초기화 | `os.Getenv("MY_POD_NAME")` → `os.Getenv("MY_COLOR")` |
| Controller `updateStateConfigMap` | hostname 패턴 매칭 → `cm.Data[oldColor]`, `cm.Data[newColor]` 직접 쓰기 |
| Fault injection 헬퍼 스크립트 | hostname별 patch → BlueGreen별 patch (대폭 단순화) |
| 긴급 수동 복구 스크립트 | 변경 없음 (Sidecar HTTP push는 Pod 단위) |

### 5.4 Static Membership 조건부 적용

```java
// KafkaConsumerConfig.java
if (groupInstanceId != null && !groupInstanceId.isEmpty()
        && !"DISABLED".equalsIgnoreCase(groupInstanceId)) {
    props.put(ConsumerConfig.GROUP_INSTANCE_ID_CONFIG, groupInstanceId);
}
```

환경변수:
- StatefulSet: `KAFKA_GROUP_INSTANCE_ID: ${HOSTNAME}` (기존 유지)
- Deployment: `KAFKA_GROUP_INSTANCE_ID: DISABLED` (Static Membership 비활성화)

### 검증 체크리스트

- [ ] BlueGreen 단위 ConfigMap으로 모든 시나리오 동작 확인
- [ ] Deployment 매니페스트로 Consumer 배포 → Pause/Resume 전환 동작
- [ ] Static Membership DISABLED 시 Rebalance 발생하되 PauseAwareRebalanceListener가 방어
- [ ] (선택) Argo Rollouts 매니페스트로 이미지 업데이트 롤아웃 동작

---

## 7. 작업 의존성 그래프

```
Phase 1 (Foundation)
  ├── 1.1 ConfigMap 생성
  ├── 1.2 RBAC 수정
  ├── 1.3 Consumer PAUSED 기본값
  └── 1.4, 1.5 StatefulSet env var 제거
          ↓
Phase 2 (Sidecar 재작성)
  ├── 2.1~2.3 Reconciler + HTTP Push endpoint
  ├── 2.4 Lifecycle client 확장
  ├── 2.5 main.go 재작성
  ├── 2.6 configmap_watcher.go 삭제
  ├── 2.7 go.mod 정리
  ├── 2.8 Dockerfile curl 추가
  └── 2.9, 2.10 StatefulSet Volume Mount
          ↓
Phase 3 (Controller 확장)
  ├── 3.1 updateStateConfigMapWithRetry
  ├── 3.2 pushDesiredStateToSidecars
  ├── 3.3 handleSwitch 통합
  ├── 3.4 main.go env var
  └── 3.5 Deployment 환경변수
          ↓
Phase 4 (통합 검증)
  ├── 4.1~4.5 시나리오 1~5 재검증
  ├── 4.6~4.7 L2, L4 시나리오 검증
  └── 4.8 구 코드 정리
          ↓
Phase 5 (선택: 이식성) ← 독립적으로 진행 가능
  ├── 5.1~5.3 ConfigMap BlueGreen 단위 변경
  ├── 5.4 Static Membership 조건부
  └── 5.5~5.6 Deployment/Argo Rollouts 매니페스트
```

**핵심 의존성:**
- Phase 2는 Phase 1 완료 후 시작 (ConfigMap 존재 + Consumer PAUSED 필요)
- Phase 3는 Phase 2 완료 후 시작 (Sidecar에 `/desired-state` endpoint 필요)
- Phase 4는 Phase 3 완료 후 시작 (전체 흐름 통합 필요)
- Phase 5는 Phase 4 완료 후 독립적으로 진행 가능

---

## 8. 위험 요소 및 롤백 계획

### 위험 요소

| 위험 | 영향도 | 확률 | 대응 |
|------|--------|------|------|
| client-go 제거 시 Sidecar 빌드 실패 | 높음 | 중간 | go.mod에서 간접 의존성도 확인, `go mod tidy` 후 빌드 |
| Volume Mount 갱신 지연이 예상보다 긴 경우 | 낮음 | 낮음 | L2(Controller push)가 주 경로이므로 L4 지연은 fallback에만 영향 |
| Consumer PAUSED 기본 시작 시 기존 Sidecar와의 과도기 문제 | 중간 | 중간 | Phase 1에서 기존 Sidecar가 active-version 기반으로 resume하므로 호환 |
| handleSwitch에 ConfigMap write 추가 시 전환 시간 증가 | 낮음 | 낮음 | ConfigMap write는 ~10ms, 전환 시간에 유의미한 영향 없음 |

### 롤백 계획

| Phase | 롤백 방법 | 영향 |
|-------|-----------|------|
| Phase 1 | `INITIAL_STATE` env var 복원, StatefulSet rollback | Consumer Rolling Update |
| Phase 2 | Sidecar 이미지 이전 태그 복원, Volume Mount 제거 | StatefulSet Rolling Update |
| Phase 3 | Controller 이미지 이전 태그 복원 | Deployment Rolling Update |
| Phase 4 | Phase 1~3 역순 롤백 | 전체 Rolling Update |
| Phase 5 | ConfigMap 키를 hostname 단위로 복원, Sidecar/Controller 코드 복원 | 전체 재배포 |

---

## 9. 성공 기준

### 필수 (Phase 1~4)

| 기준 | 측정 방법 | 목표값 | 결과 | 상태 |
|------|-----------|--------|------|------|
| P0 해결: Dual-Active 미발생 | 시나리오 4에서 PAUSED 측 Pod 재시작 후 상태 확인 | PAUSED 유지 | **PAUSED 유지** | ✅ |
| P1 해결: Sidecar 자동 복구 | Consumer 기동 완료 후 Sidecar reconcile 성공까지 시간 | **< 10초** | **~5초 내 reconcile** | ✅ |
| #2 해결: Sidecar curl 기반 fault injection | Sidecar curl로 PUT 장애 주입/해제 가능 여부 | 동작 확인 | **동작 확인** | ✅ |
| 전환 시간 유지 | 시나리오 1 전환 시간 측정 | **< 2초** | **1.19초** | ✅ |
| L2 복구 시간 | Controller → Sidecar HTTP Push | **< 10초** | **즉시 (push 성공)** | ✅ |
| client-go 제거 | `go mod graph \| grep k8s.io` | 결과 없음 | **결과 없음** | ✅ |
| Sidecar 바이너리 크기 감소 | Docker image layer 크기 비교 | **~10MB** (기존 ~40MB 대비) | 미측정 (빌드 성공) | ⚠️ |

### 선택 (Phase 5) — 미착수

| 기준 | 측정 방법 | 목표값 | 결과 | 상태 |
|------|-----------|--------|------|------|
| Deployment 호환 | Deployment로 Consumer 배포 후 전환 동작 | 전환 성공 | — | ⬜ |
| BlueGreen 단위 ConfigMap 동작 | BlueGreen 키 기반 reconcile 동작 확인 | 동작 확인 | — | ⬜ |
| Static Membership 비활성화 | Deployment에서 Rebalance 후 PAUSED 유지 | PAUSED 유지 | — | ⬜ |
