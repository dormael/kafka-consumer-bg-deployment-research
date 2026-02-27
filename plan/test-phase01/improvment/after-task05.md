# 아키텍처 개선안: kafka-consumer-state ConfigMap + Sidecar Reconciler

> **작성일:** 2026-02-22
> **선행 작업:** Task 05 (전략 C 테스트) 완료
> **목적:** P0(Dual-Active), P1(Sidecar 재시도 실패), #2(wget PUT 제약) 등 근본 원인을 해결하는 아키텍처 개선

---

## 목차

1. [배경 및 문제 요약](#1-배경-및-문제-요약)
2. [아키텍처 결정 사항](#2-아키텍처-결정-사항)
3. [현재 vs 제안 아키텍처 비교](#3-현재-vs-제안-아키텍처-비교)
4. [구현 단계 (Phase 1~4)](#4-구현-단계-phase-14)
5. [파일별 변경 상세](#5-파일별-변경-상세)
6. [Volume Mount 설계](#6-volume-mount-설계)
7. [Sidecar Reconciler 설계](#7-sidecar-reconciler-설계)
8. [Controller ConfigMap Write 설계](#8-controller-configmap-write-설계)
9. [마이그레이션 전략](#9-마이그레이션-전략)
10. [Edge Cases](#10-edge-cases)
11. [검증 계획](#11-검증-계획)
12. [test-guide.md 영향](#12-test-guidemd-영향)

---

## 1. 배경 및 문제 요약

Task 05(전략 C) 테스트에서 다음 문제가 발견되었다:

| # | 우선순위 | 문제 | 근본 원인 |
|---|----------|------|-----------|
| P0 | **Critical** | PAUSED 측 Pod 재시작 시 Dual-Active | `INITIAL_STATE=ACTIVE` 정적 env var — ConfigMap 상태를 참조하지 않음 |
| P1 | **High** | Sidecar 초기 연결 실패 후 재시도 안 함 | Consumer 기동(~17초) 전에 3회 재시도 후 포기, ConfigMap 변경 없으면 재시도 없음 |
| #2 | Medium | Container 내 wget으로 PUT 불가 | BusyBox `wget`이 PUT 미지원 — 장애 주입에 `port-forward` + 호스트 `curl` 필요 |
| #5 | Medium | 이중 제어 경합 | Controller HTTP + Sidecar HTTP 두 경로가 동시에 Consumer 제어 — 상태 불일치 가능 |
| #7 | Medium | Sidecar-Consumer 기동 순서 문제 | Sidecar는 즉시 시작, Consumer는 ~17초 후 Ready — 초기 명령 유실 |

### 공통 근본 원인

현재 아키텍처는 **두 가지 독립적 제어 경로**가 존재한다:

1. **Controller 경로**: ConfigMap Watch에서 HTTP로 각 Consumer Pod에 직접 pause/resume
2. **Sidecar 경로**: ConfigMap Watch에서 HTTP로 동일 Consumer에 pause/resume

이 두 경로는 동기화되지 않으며, Consumer가 "누구의 명령을 따를지" 결정할 수 없다. 또한 Consumer의 초기 상태가 env var로 고정되어 있어 ConfigMap과의 정합성이 보장되지 않는다.

---

## 2. 아키텍처 결정 사항

### 결정 1: `kafka-consumer-state` ConfigMap 도입

hostname별 desired state를 JSON으로 관리하는 전용 ConfigMap을 신규 생성한다.

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kafka-consumer-state
  namespace: kafka-bg-test
data:
  consumer-blue-0: '{"lifecycle":"PAUSED"}'
  consumer-blue-1: '{"lifecycle":"PAUSED"}'
  consumer-blue-2: '{"lifecycle":"PAUSED"}'
  consumer-green-0: '{"lifecycle":"ACTIVE"}'
  consumer-green-1: '{"lifecycle":"ACTIVE"}'
  consumer-green-2: '{"lifecycle":"ACTIVE"}'
```

**근거:**
- hostname 단위 제어로 개별 Pod에 다른 상태 지정 가능 (Rolling Update 등 미래 확장)
- JSON 구조로 향후 fault injection 등 추가 필드 확장 용이
- 기존 `kafka-consumer-active-version`(active: blue/green)과 분리하여 역할 명확화

### 결정 2: Volume Mount + File Polling (API Watch 대체)

Sidecar가 K8s API Watch 대신 ConfigMap Volume Mount 파일을 읽는 방식으로 전환한다.

**근거:**
- client-go 의존성 제거로 Sidecar 바이너리 크기 ~40MB에서 ~10MB로 감소, 메모리 사용량 감소
- Sidecar에 별도 ServiceAccount 권한 불필요 (보안 향상)
- kubelet이 파일 갱신을 보장하므로 자체 Watch 구현 불필요
- 파일 시스템 접근만 필요하므로 Container 이미지에 K8s 런타임 의존성 없음

### 결정 3: Controller가 ConfigMap 선기록 후 HTTP 전환

Controller가 전환 시 **먼저** `kafka-consumer-state` ConfigMap에 desired state를 기록한 후, HTTP로 Consumer에 즉시 전환을 수행한다. Sidecar는 **fallback** 역할만 담당한다.

```
전환 흐름:
  1. Controller: ConfigMap에 desired state 기록 (Blue=PAUSED, Green=ACTIVE)
  2. Controller: HTTP로 Blue pause, Green resume (즉시 반영, ~1초)
  3. Sidecar: 파일 변경 감지에서 desired state와 actual state 비교, 불일치 시 reconcile
```

**근거:**
- HTTP 직접 호출은 ~1초 내 즉시 반영 (성능 유지)
- ConfigMap 기록은 "진실의 원천(source of truth)" 확보
- Sidecar의 reconcile은 Controller 실패, Consumer 재시작 등 예외 상황을 자동 복구

### 결정 4: Consumer 기본 PAUSED 시작

Consumer가 `INITIAL_STATE` env var 대신 기본 PAUSED로 시작하고, Sidecar가 ConfigMap 기반으로 올바른 상태로 전환한다.

**근거:**
- PAUSED로 시작하면 Dual-Active가 불가능 (P0 해결)
- Sidecar가 "이 Pod는 ACTIVE여야 한다"고 판단하면 resume하여 정상 동작
- Consumer 기동 완료 전까지 자연스럽게 소비하지 않음

### 결정 5: Sidecar가 Fault Injection도 적용

`kafka-consumer-state` ConfigMap에 fault injection 필드를 추가하고, Sidecar가 lifecycle뿐 아니라 fault injection도 적용한다.

```json
{
  "lifecycle": "ACTIVE",
  "fault": {
    "processingDelayMs": 200,
    "errorRatePercent": 0,
    "commitDelayMs": 0
  }
}
```

**근거:**
- Container 내 wget의 PUT 제약 문제(#2) 해결로 ConfigMap patch만으로 모든 설정 가능
- 테스트 시 `kubectl patch`만으로 장애 주입/해제 가능 (port-forward 불필요)
- 원자적 업데이트로 여러 Pod에 동시 적용

### 결정 6: Sidecar Dockerfile에 curl 추가

BusyBox wget의 PUT 미지원 문제를 보완하기 위해 Sidecar 이미지에 `curl`을 추가한다.

**근거:**
- Sidecar가 Consumer lifecycle API에 PUT 요청을 보내야 할 경우 대비
- 디버깅 시 `kubectl exec -c switch-sidecar -- curl` 사용 가능
- Alpine의 `curl` 패키지는 ~2MB로 이미지 크기 영향 미미

---

## 3. 현재 vs 제안 아키텍처 비교

### 현재 아키텍처 데이터 흐름

```
kubectl patch configmap kafka-consumer-active-version (active: blue->green)
        |
        +---------------------------+
        v                           v
 Switch Controller              Switch Sidecar (각 Pod)
 (API Watch: active-version)    (API Watch: active-version)
        |                           |
        | HTTP POST /pause,resume   | HTTP POST /pause,resume
        v                           v
    Consumer lifecycle API (동일 Consumer에 2개 경로에서 명령)
```

**문제점:**
- 이중 제어: Controller와 Sidecar 모두 Consumer에 직접 HTTP 호출
- 상태 비동기: Consumer의 `INITIAL_STATE` env var이 ConfigMap과 독립적
- 복구 불가: Sidecar가 3회 실패 후 포기, ConfigMap 변경 없으면 재시도 안 함

### 제안 아키텍처 데이터 흐름

```
kubectl patch configmap kafka-consumer-active-version (active: blue->green)
        |
        v
 Switch Controller (API Watch: active-version)
        |
        +-- (1) ConfigMap Write: kafka-consumer-state
        |       consumer-blue-*  -> {"lifecycle":"PAUSED"}
        |       consumer-green-* -> {"lifecycle":"ACTIVE"}
        |
        +-- (2) HTTP POST: Blue pods /pause, Green pods /resume (즉시 반영)
        |
        v
 kafka-consumer-state ConfigMap
        |
        | (Volume Mount -> kubelet 파일 갱신, ~60초 이내)
        v
 Switch Sidecar (각 Pod, File Polling)
        |
        | (3) 파일 읽기 -> desired vs actual 비교
        |     불일치 시 HTTP POST /pause 또는 /resume + PUT /fault/*
        v
    Consumer lifecycle API (단일 제어 경로: Sidecar만 직접 호출)
```

**핵심 개선:**
- 단일 source of truth: `kafka-consumer-state` ConfigMap
- Controller는 ConfigMap에 기록 + HTTP 즉시 반영 (성능)
- Sidecar는 파일 기반 reconcile하여 항상 desired state로 수렴 (안정성)

### 역할 분리표

| 컴포넌트 | 현재 역할 | 제안 역할 |
|----------|-----------|-----------|
| `kafka-consumer-active-version` | 전환 트리거 + 상태 저장 | 전환 트리거만 (active: blue/green) |
| `kafka-consumer-state` | 없음 | hostname별 desired state 저장 |
| Switch Controller | ConfigMap Watch에서 HTTP 직접 전환 | ConfigMap Watch에서 State 기록 + HTTP 즉시 전환 |
| Switch Sidecar | ConfigMap API Watch에서 HTTP 전환 | File Polling에서 Reconcile (fallback) |
| Consumer `INITIAL_STATE` | env var로 고정 (ACTIVE/PAUSED) | 삭제하고 기본 PAUSED, Sidecar가 올바른 상태로 전환 |
| Fault Injection | `port-forward` + `curl -X PUT` | ConfigMap patch로 Sidecar가 적용 |

---

## 4. 구현 단계 (Phase 1~4)

### Phase 1: Foundation — ConfigMap, Consumer PAUSED 기본값

**목표:** 새 ConfigMap과 권한을 준비하고, Consumer가 PAUSED로 시작하도록 변경

**작업:**

1. `k8s/consumer-state-configmap.yaml` 생성
2. `k8s/switch-rbac.yaml` 수정: ConfigMap rules에 `create` verb 추가
3. `apps/consumer/src/main/resources/application.yaml` 수정: `consumer.initial-state` 기본값을 `PAUSED`로 변경
4. `k8s/consumer-blue-statefulset.yaml` 수정: `INITIAL_STATE` env var 제거
5. `k8s/consumer-green-statefulset.yaml` 수정: 동일

**검증:**
- [ ] `kubectl apply -f k8s/consumer-state-configmap.yaml` 성공
- [ ] Consumer 재배포 후 모든 Pod가 PAUSED로 시작
- [ ] Sidecar 로그에 에러 없음 (기존 동작 유지)

### Phase 2: Sidecar 재작성 — File Polling + Reconciler

**목표:** client-go 의존성 제거, Volume Mount 기반 File Polling으로 전환

**작업:**

1. `apps/switch-sidecar/internal/reconciler/reconciler.go` 신규 생성
2. `apps/switch-sidecar/internal/lifecycle/client.go` 수정: fault injection HTTP 메서드 추가
3. `apps/switch-sidecar/cmd/sidecar/main.go` 수정: client-go 제거, reconciler 연결
4. `apps/switch-sidecar/internal/watcher/configmap_watcher.go` 삭제
5. `apps/switch-sidecar/go.mod` 수정: `k8s.io/*` 의존성 제거
6. `apps/switch-sidecar/Dockerfile` 수정: `curl` 추가

**검증:**
- [ ] `go build ./cmd/sidecar/` 성공 (client-go 없이)
- [ ] Sidecar가 Volume Mount 파일을 읽어 desired state 확인
- [ ] Sidecar가 Consumer에 pause/resume 명령을 올바르게 전송
- [ ] Sidecar가 fault injection 설정을 올바르게 적용

### Phase 3: Controller 확장 — ConfigMap Write + handleSwitch 통합

**목표:** Controller가 전환 시 `kafka-consumer-state` ConfigMap에 desired state를 먼저 기록

**작업:**

1. `apps/switch-controller/internal/controller/switch_controller.go` 수정:
   - `updateStateConfigMap()` 메서드 추가
   - `handleSwitch()` 시작 시 ConfigMap write 선행
2. `apps/switch-controller/cmd/controller/main.go` 수정: `STATE_CONFIGMAP_NAME` env var 추가

**검증:**
- [ ] 전환 트리거 시 `kafka-consumer-state` ConfigMap이 올바르게 업데이트됨
- [ ] 전환 시간이 기존과 동일하게 ~1초 유지
- [ ] ConfigMap write 실패 시 전환이 중단되는지 확인

### Phase 4: 통합 및 정리 — StatefulSet Volume Mount, 최종 검증

**목표:** StatefulSet에 Volume Mount를 추가하고 전체 흐름 검증

**작업:**

1. `k8s/consumer-blue-statefulset.yaml` 수정: Volume + VolumeMount 추가
2. `k8s/consumer-green-statefulset.yaml` 수정: 동일
3. 전체 시나리오 재검증 (시나리오 1~5)

**검증:**
- [ ] 시나리오 4 (Rebalance 장애): Pod 재시작 후 Dual-Active 미발생 (P0 해결)
- [ ] Sidecar 3회 실패 후에도 주기적 reconcile로 복구 (P1 해결)
- [ ] `kubectl patch`로 fault injection 가능 (#2 해결)
- [ ] 전환 시간 5초 미만 유지

---

## 5. 파일별 변경 상세

### 신규 파일

| 파일 | 설명 |
|------|------|
| `k8s/consumer-state-configmap.yaml` | hostname별 desired state ConfigMap 매니페스트 |
| `apps/switch-sidecar/internal/reconciler/reconciler.go` | File Polling 기반 reconcile loop |

### 수정 파일

| 파일 | 변경 내용 |
|------|-----------|
| `apps/switch-controller/internal/controller/switch_controller.go` | `updateStateConfigMap()` 추가, `handleSwitch()` 시작에 ConfigMap write 선행 |
| `apps/switch-controller/cmd/controller/main.go` | `STATE_CONFIGMAP_NAME` env var 파싱 추가 |
| `apps/switch-sidecar/internal/lifecycle/client.go` | `SetProcessingDelay()`, `SetErrorRate()`, `SetCommitDelay()` PUT 메서드 추가 |
| `apps/switch-sidecar/cmd/sidecar/main.go` | client-go import 제거, `rest.InClusterConfig()` 제거, reconciler 초기화로 교체 |
| `apps/switch-sidecar/go.mod` | `k8s.io/api`, `k8s.io/apimachinery`, `k8s.io/client-go` 및 간접 의존성 제거 |
| `apps/switch-sidecar/Dockerfile` | `curl` 추가 |
| `k8s/consumer-blue-statefulset.yaml` | `INITIAL_STATE` env var 제거, Volume + VolumeMount 추가 |
| `k8s/consumer-green-statefulset.yaml` | 동일 |
| `k8s/switch-rbac.yaml` | ConfigMap rules에 `create` verb 추가 |
| `apps/consumer/src/main/resources/application.yaml` | `consumer.initial-state` 기본값 `ACTIVE`를 `PAUSED`로 변경 |
| `k8s/switch-controller-deployment.yaml` | `STATE_CONFIGMAP_NAME` env var 추가 |

### 삭제 파일

| 파일 | 사유 |
|------|------|
| `apps/switch-sidecar/internal/watcher/configmap_watcher.go` | API Watch 방식 제거, reconciler로 대체 |

---

## 6. Volume Mount 설계

### 마운트 구조

```yaml
# StatefulSet spec.template.spec 내
volumes:
  - name: consumer-state
    configMap:
      name: kafka-consumer-state

# Sidecar container 내
volumeMounts:
  - name: consumer-state
    mountPath: /etc/consumer-state
    readOnly: true
```

### 파일 구조 (Pod 내부에서 보이는 형태)

```
/etc/consumer-state/
  +-- consumer-blue-0    -> '{"lifecycle":"PAUSED"}'
  +-- consumer-blue-1    -> '{"lifecycle":"PAUSED"}'
  +-- consumer-blue-2    -> '{"lifecycle":"PAUSED"}'
  +-- consumer-green-0   -> '{"lifecycle":"ACTIVE"}'
  +-- consumer-green-1   -> '{"lifecycle":"ACTIVE"}'
  +-- consumer-green-2   -> '{"lifecycle":"ACTIVE"}'
```

### subPath 금지

**중요:** `subPath`를 사용하면 ConfigMap 업데이트 시 파일이 갱신되지 않는다.

```yaml
# 잘못된 예 (subPath 사용 - 파일이 갱신되지 않음)
volumeMounts:
  - name: consumer-state
    mountPath: /etc/consumer-state/my-state
    subPath: consumer-blue-0   # <-- 금지

# 올바른 예 (디렉토리 전체 마운트)
volumeMounts:
  - name: consumer-state
    mountPath: /etc/consumer-state
    readOnly: true
```

### Symlink 구조 (kubelet 내부 동작)

kubelet은 ConfigMap Volume을 다음과 같이 관리한다:

```
/etc/consumer-state/
  +-- ..data  -> ..2026_02_22_10_30_00.123456789/   (symlink to latest)
  +-- ..2026_02_22_10_30_00.123456789/              (actual data directory)
  |   +-- consumer-blue-0
  |   +-- consumer-blue-1
  |   +-- ...
  +-- consumer-blue-0 -> ..data/consumer-blue-0     (symlink)
  +-- ...
```

ConfigMap 업데이트 시 kubelet은:
1. 새 timestamped 디렉토리 생성
2. 새 데이터 파일 생성
3. `..data` symlink를 atomic swap (rename)
4. 이전 디렉토리 삭제

Sidecar는 `..data` symlink의 변경을 감지하거나, 파일 내용을 주기적으로 읽어 변경을 확인한다.

### 갱신 주기

| 설정 | 기본값 | 설명 |
|------|--------|------|
| `kubelet --sync-frequency` | 1분 | kubelet이 ConfigMap Volume을 갱신하는 주기 |
| ConfigMap TTL Cache | 1분 | kubelet의 ConfigMap 캐시 TTL |
| **최대 전파 지연** | **~2분** | 최악의 경우 ConfigMap 변경 후 파일 반영까지 |

**참고:** Controller의 HTTP 직접 호출이 즉시 반영(~1초)을 담당하므로, Volume Mount 전파 지연은 정상 동작에 영향을 주지 않는다. Volume Mount는 Controller가 실패했거나 Consumer가 재시작된 경우의 fallback이다.

---

## 7. Sidecar Reconciler 설계

### DesiredState 구조체

```go
// DesiredState represents the desired state for a consumer pod
// as stored in kafka-consumer-state ConfigMap.
type DesiredState struct {
    Lifecycle string     `json:"lifecycle"` // "ACTIVE" or "PAUSED"
    Fault     *FaultSpec `json:"fault,omitempty"`
}

// FaultSpec represents fault injection parameters.
type FaultSpec struct {
    ProcessingDelayMs int `json:"processingDelayMs,omitempty"`
    ErrorRatePercent  int `json:"errorRatePercent,omitempty"`
    CommitDelayMs     int `json:"commitDelayMs,omitempty"`
}
```

### Reconciler 구조체

```go
type Reconciler struct {
    statePath       string            // "/etc/consumer-state"
    myHostname      string            // "consumer-blue-0"
    lifecycleClient *lifecycle.Client
    logger          *slog.Logger
    metrics         *metrics.Metrics
    pollInterval    time.Duration     // 5s
    lastApplied     *DesiredState     // last successfully applied state
}
```

### reconcileOnce 알고리즘

```
func reconcileOnce():
  1. Read file: /etc/consumer-state/{myHostname}
     - File not found -> WARN log, retry next cycle
     - Read error -> ERROR log, retry next cycle

  2. JSON parse -> DesiredState
     - Parse error -> ERROR log, retry next cycle

  3. Compare with lastApplied
     - If identical -> SKIP (prevent unnecessary HTTP calls)

  4. Query current consumer state: GET /lifecycle/status
     - Failure -> WARN log (consumer may not be ready), retry next cycle

  5. Compare lifecycle state:
     - desired=ACTIVE, actual!=ACTIVE -> POST /lifecycle/resume
     - desired=PAUSED, actual!=PAUSED -> POST /lifecycle/pause

  6. Apply fault injection (if desired.Fault is present):
     - desired.Fault.ProcessingDelayMs != 0 -> PUT /fault/processing-delay
     - desired.Fault.ErrorRatePercent != 0 -> PUT /fault/error-rate
     - desired.Fault.CommitDelayMs != 0 -> PUT /fault/commit-delay
     - All values 0 -> PUT with 0 values to each fault endpoint (clear)

  7. On success: update lastApplied
     - On failure: do NOT update lastApplied -> automatic retry next cycle
```

### 실패 시 재시도 전략

| 실패 유형 | 동작 | 재시도 방식 |
|-----------|------|------------|
| 파일 없음 | WARN 로그 | 다음 poll 주기(5초)에 자동 재시도 |
| Consumer 미기동 | WARN 로그 | 다음 poll 주기(5초)에 자동 재시도 |
| HTTP 요청 실패 | ERROR 로그 | `lifecycle.Client`의 내장 재시도(3회) + 다음 poll 주기 |
| JSON 파싱 실패 | ERROR 로그 | 다음 poll 주기(5초)에 자동 재시도 |

**핵심 차이 (vs 현재 Sidecar):** 현재 Sidecar는 ConfigMap 변경 이벤트에만 반응하므로, 초기 실패 후 ConfigMap 변경이 없으면 영원히 재시도하지 않는다. 제안 방식은 **주기적 polling**이므로 Consumer가 기동 완료되면 자연스럽게 reconcile에 성공한다.

### Sidecar main.go 변경 개요

```go
// 변경 전 (client-go 의존)
import (
    "k8s.io/client-go/kubernetes"
    "k8s.io/client-go/rest"
    ".../watcher"
)
k8sConfig, err := rest.InClusterConfig()
clientset, _ := kubernetes.NewForConfig(k8sConfig)
cmWatcher := watcher.NewConfigMapWatcher(clientset, ...)
go cmWatcher.Watch(ctx)

// 변경 후 (client-go 제거)
import (
    ".../reconciler"
)
rec := reconciler.New(
    "/etc/consumer-state",
    os.Getenv("MY_POD_NAME"),
    lifecycleClient,
    logger,
    metrics,
    5*time.Second,
)
go rec.Run(ctx)
```

---

## 8. Controller ConfigMap Write 설계

### updateStateConfigMap 메서드

Controller가 전환 시 `kafka-consumer-state` ConfigMap에 desired state를 기록하는 메서드:

```go
func (sc *SwitchController) updateStateConfigMap(
    ctx context.Context,
    oldColor, newColor string,
) error {
    // 1. Read current ConfigMap
    cm, err := sc.client.CoreV1().ConfigMaps(sc.config.Namespace).Get(
        ctx, sc.config.StateConfigMapName, metav1.GetOptions{},
    )
    if err != nil {
        return fmt.Errorf("failed to get state configmap: %w", err)
    }

    // 2. Modify: old color -> PAUSED, new color -> ACTIVE
    for key := range cm.Data {
        if strings.HasPrefix(key, "consumer-"+oldColor+"-") {
            cm.Data[key] = `{"lifecycle":"PAUSED"}`
        } else if strings.HasPrefix(key, "consumer-"+newColor+"-") {
            cm.Data[key] = `{"lifecycle":"ACTIVE"}`
        }
    }

    // 3. Write back
    _, err = sc.client.CoreV1().ConfigMaps(sc.config.Namespace).Update(
        ctx, cm, metav1.UpdateOptions{},
    )
    if err != nil {
        return fmt.Errorf("failed to update state configmap: %w", err)
    }

    return nil
}
```

### Read-Modify-Write 충돌 재시도

K8s API의 optimistic concurrency control(ResourceVersion)을 활용한다:

```go
func (sc *SwitchController) updateStateConfigMapWithRetry(
    ctx context.Context,
    oldColor, newColor string,
    maxRetries int,
) error {
    for attempt := 0; attempt <= maxRetries; attempt++ {
        err := sc.updateStateConfigMap(ctx, oldColor, newColor)
        if err == nil {
            return nil
        }
        if !errors.IsConflict(err) {
            return err // non-conflict error, cannot retry
        }
        sc.logger.Warn("state configmap update conflict, retrying",
            "attempt", attempt)
    }
    return fmt.Errorf("failed to update state configmap after %d retries", maxRetries)
}
```

**참고:** ConfigMap 충돌은 동시에 여러 Controller가 쓰기를 시도할 때 발생할 수 있으나, Lease 기반 상호 배제로 인해 실제로는 거의 발생하지 않는다. 재시도는 방어적 구현이다.

### handleSwitch 통합

```go
func (sc *SwitchController) handleSwitch(ctx context.Context, oldColor, newColor string) {
    startTime := time.Now()
    sc.metrics.SwitchTotal.Inc()

    // Step 0 (new): Write desired state to State ConfigMap first
    if err := sc.updateStateConfigMapWithRetry(ctx, oldColor, newColor, 3); err != nil {
        sc.logger.Error("failed to update state configmap, aborting switch", "error", err)
        return
    }

    // Step 1: Acquire Lease (unchanged)
    if err := sc.leaseManager.AcquireLease(ctx, ...); err != nil {
        ...
    }

    // Step 2~9: existing logic unchanged (HTTP direct switch)
    ...
}
```

### Hostname 탐색

Controller가 ConfigMap에 기록할 hostname 목록을 구하는 방법:

1. **ConfigMap 기존 키 기반** (권장): `kafka-consumer-state` ConfigMap의 기존 키에서 `consumer-{color}-*` 패턴 매칭
2. **StatefulSet 네이밍 규칙**: `consumer-{color}-{0..N-1}` (StatefulSet replicas 수 기반)
3. **Endpoints 조회**: Service Endpoints에서 Pod IP로 Pod Name 매핑

권장 방식은 (1)로, ConfigMap에 이미 존재하는 키만 업데이트하므로 Scale Up/Down 시 별도 처리가 필요하지 않다.

---

## 9. 마이그레이션 전략

### 무중단 배포 순서

기존 동작을 유지하면서 순차적으로 전환한다:

```
Phase 1 (Foundation):
  1. kafka-consumer-state ConfigMap 생성 (apply)
  2. 권한 업데이트 (apply)
  3. Consumer application.yaml 수정 -> 이미지 재빌드
  4. StatefulSet 수정 (INITIAL_STATE 제거) -> Rolling Update
  ※ 이 시점에서 Consumer는 PAUSED로 시작하지만,
    기존 Sidecar(API Watch)가 active-version ConfigMap 기반으로 resume 수행

Phase 2 (Sidecar):
  5. Sidecar 코드 재작성 -> 이미지 재빌드
  6. StatefulSet에 Volume Mount 추가 -> Rolling Update
  ※ 새 Sidecar는 File Polling으로 동작, Consumer 재시작 시 자동 reconcile

Phase 3 (Controller):
  7. Controller 코드 수정 -> 이미지 재빌드
  8. Controller Deployment 환경변수 추가 -> Rolling Update
  ※ Controller가 State ConfigMap에 기록 시작

Phase 4 (통합 검증):
  9. 시나리오 1~5 재검증
  10. 구 코드 정리 (configmap_watcher.go 삭제)
```

### 롤백 계획

각 Phase에서 문제 발생 시:

| Phase | 롤백 방법 | 영향 |
|-------|-----------|------|
| Phase 1 | Consumer `INITIAL_STATE` env var 복원, StatefulSet rollback | Consumer 재시작 필요 |
| Phase 2 | Sidecar 이미지를 이전 태그로 복원, Volume Mount 제거 | StatefulSet Rolling Update |
| Phase 3 | Controller 이미지를 이전 태그로 복원 | Deployment Rolling Update |
| Phase 4 | Phase 1~3 역순 롤백 | 전체 Rolling Update |

---

## 10. Edge Cases

### Case 1: Controller 크래시 후 재시작

**시나리오:** Controller가 Step 0(ConfigMap write)과 Step 2(HTTP pause) 사이에서 크래시

**동작:**
- `kafka-consumer-state`에는 새 desired state가 기록됨
- `kafka-consumer-active-version`은 이미 새 값으로 변경됨 (사용자 patch)
- Controller 재시작 시 `active-version`의 현재 값을 읽어 초기화
- Sidecar는 Volume Mount 파일이 갱신되면 reconcile로 desired state 적용
- **결과:** ~2분 이내 자동 복구 (kubelet 파일 갱신 주기)

### Case 2: Scale Up (새 Pod 추가)

**시나리오:** `kubectl scale statefulset consumer-blue --replicas=4`

**동작:**
- `consumer-blue-3` Pod 생성하면 Consumer는 PAUSED로 시작
- `kafka-consumer-state` ConfigMap에 `consumer-blue-3` 키가 없음
- Sidecar는 "파일 없음"으로 WARN 로그, 다음 주기에 재시도
- **수동 조치 필요:** ConfigMap에 `consumer-blue-3` 키 추가

```bash
kubectl patch configmap kafka-consumer-state -n kafka-bg-test \
  --type merge -p '{"data":{"consumer-blue-3":"{\"lifecycle\":\"ACTIVE\"}"}}'
```

**향후 개선:** Controller가 StatefulSet replicas 수를 감시하여 자동으로 키를 추가하는 로직 (이번 구현 범위 밖)

### Case 3: 동시 쓰기 (ConfigMap 충돌)

**시나리오:** Controller와 사용자가 동시에 `kafka-consumer-state` ConfigMap을 수정

**동작:**
- K8s API 서버의 optimistic concurrency control (ResourceVersion)에 의해 후순위 쓰기가 실패 (HTTP 409 Conflict)
- Controller의 `updateStateConfigMapWithRetry()`가 3회까지 재시도
- **결과:** 최대 3회 재시도 후 성공 또는 에러 로그 + 전환 중단

### Case 4: Container 재시작 (OOMKill, CrashLoopBackOff)

**시나리오:** Consumer Container가 OOMKill로 재시작

**동작:**
- Consumer는 PAUSED로 재시작 (기본값)
- Sidecar는 동일 Pod에 있으므로 재시작되지 않음 (Container 단위 재시작)
- Sidecar의 다음 reconcile 주기(5초)에 Consumer 상태 조회, desired state로 전환
- **결과:** ~5초 이내 자동 복구

**예외:** Sidecar Container도 함께 재시작된 경우
- Sidecar 재시작하면 Volume Mount 파일은 이미 존재하므로 즉시 reconcile 시작
- Consumer 기동(~17초) 전에는 HTTP 요청 실패, 5초 주기로 재시도
- Consumer 기동 완료 후 첫 성공한 reconcile에서 desired state 적용
- **결과:** ~20~25초 이내 자동 복구

### Case 5: ConfigMap 전파 지연

**시나리오:** Controller가 State ConfigMap을 업데이트했으나 kubelet이 아직 파일을 갱신하지 않음

**동작:**
- Controller의 HTTP 직접 호출이 먼저 Consumer를 전환 (~1초)
- Sidecar는 파일 갱신 전까지 이전 desired state를 읽음
- **위험:** Sidecar가 이전 desired state를 보고 Controller의 전환을 되돌릴 수 있음

**방어 설계:**
- Sidecar의 `lastApplied` 캐시: 이전 reconcile에서 성공적으로 적용한 상태 기록
- 파일 내용이 `lastApplied`와 동일하면 SKIP하여 Controller의 HTTP 전환을 되돌리지 않음
- 파일 내용이 변경되면(kubelet 갱신 후) 새 desired state로 reconcile
- **결론:** 정상 동작에 영향 없음. Sidecar는 "파일이 변경될 때만" 새 desired state를 적용

### Case 6: Sidecar가 Consumer보다 먼저 기동

**시나리오:** Pod 시작 시 Sidecar가 Consumer보다 먼저 Ready

**동작:**
- Sidecar가 Volume Mount 파일을 읽어 desired state 확인 (예: ACTIVE)
- Consumer에 GET /lifecycle/status 요청하면 실패 (Consumer 미기동)
- Sidecar는 reconcile 실패로 `lastApplied` 미갱신, 5초 후 재시도
- Consumer 기동 완료(~17초) 후 첫 성공한 reconcile에서 desired state 적용
- **결과:** Consumer 기동 후 ~5초 이내 자동 전환 (P1 해결)

---

## 11. 검증 계획

### Phase별 체크리스트

#### Phase 1 검증

```bash
# 1. ConfigMap 생성 확인
kubectl get configmap kafka-consumer-state -n kafka-bg-test -o yaml

# 2. 권한 확인
kubectl auth can-i create configmaps \
  --as=system:serviceaccount:kafka-bg-test:bg-switch-sa -n kafka-bg-test

# 3. Consumer PAUSED 기본 시작 확인
kubectl rollout restart statefulset consumer-blue consumer-green -n kafka-bg-test
kubectl wait --for=condition=Ready pod/consumer-blue-0 \
  -n kafka-bg-test --timeout=60s
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  wget -qO- http://localhost:8080/lifecycle/status
# 기대: {"state":"PAUSED",...}
```

#### Phase 2 검증

```bash
# 1. Sidecar 이미지 빌드 성공
cd apps/switch-sidecar && go build ./cmd/sidecar/

# 2. Volume Mount 파일 확인
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  cat /etc/consumer-state/consumer-blue-0
# 기대: {"lifecycle":"ACTIVE"} 또는 {"lifecycle":"PAUSED"}

# 3. curl 사용 가능 확인
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  curl -s http://localhost:8080/lifecycle/status
# 기대: JSON 응답

# 4. Reconcile 동작 확인
kubectl logs -n kafka-bg-test consumer-blue-0 -c switch-sidecar --tail=20
# 기대: "reconcile succeeded" 또는 "desired state matches actual" 로그
```

#### Phase 3 검증

```bash
# 1. Controller에 STATE_CONFIGMAP_NAME 환경변수 확인
kubectl get deploy bg-switch-controller -n kafka-bg-test \
  -o jsonpath='{.spec.template.spec.containers[0].env}'

# 2. 전환 트리거 후 State ConfigMap 확인
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"green"}}'
sleep 5
kubectl get configmap kafka-consumer-state -n kafka-bg-test -o yaml
# 기대: green -> ACTIVE, blue -> PAUSED
```

#### Phase 4: 시나리오 4 재검증 (P0 해결 확인)

이것이 가장 중요한 검증이다:

```bash
# 1. 초기 상태 확인 (Blue=ACTIVE, Green=PAUSED)
check_all

# 2. 전환: Blue -> Green
switch_to green
sleep 5
check_all
# 기대: Blue=PAUSED, Green=ACTIVE

# 3. PAUSED 측(Blue) Pod 재시작
kubectl delete pod consumer-blue-0 -n kafka-bg-test
sleep 30

# 4. 핵심 확인: 재시작된 Blue-0은 PAUSED인가?
kubectl exec -n kafka-bg-test consumer-blue-0 -c switch-sidecar -- \
  wget -qO- http://localhost:8080/lifecycle/status
# 기대: {"state":"PAUSED",...}
# (현재 구현에서는 여기서 ACTIVE가 반환되어 Dual-Active 발생)

# 5. 전체 상태 확인
check_all
# 기대: Blue 전체 PAUSED, Green 전체 ACTIVE (Dual-Active 없음)
```

---

## 12. test-guide.md 영향

### 해결되는 항목

| # | 항목 | 해결 방법 |
|---|------|-----------|
| P0 | PAUSED 측 Pod 재시작 시 Dual-Active | Consumer 기본 PAUSED + Sidecar reconcile |
| P1 | Sidecar 초기 연결 실패 후 재시도 안 함 | Reconciler의 주기적 polling (5초) |
| #2 | Container 내 wget으로 PUT 불가 | ConfigMap patch로 fault injection (port-forward 불필요) |
| #5 | Pod 재시작 후 Dual-Active (P0과 동일) | 동일 |
| #7 | Sidecar 초기 연결 실패 (P1과 동일) | 동일 |

### 변경되는 테스트 절차

#### 장애 주입 방식 변경

```bash
# 변경 전: port-forward + curl -X PUT (test-guide.md 4절)
for i in 0 1 2; do
  kubectl port-forward -n kafka-bg-test consumer-blue-$i 1808$i:8080 &
done
sleep 2
for i in 0 1 2; do
  curl -s -X PUT http://localhost:1808$i/fault/processing-delay \
    -H "Content-Type: application/json" -d '{"delayMs": 200}'
done
pkill -f "kubectl port-forward.*consumer-blue" 2>/dev/null

# 변경 후: kubectl patch (간단)
kubectl patch configmap kafka-consumer-state -n kafka-bg-test --type merge -p '{
  "data": {
    "consumer-blue-0": "{\"lifecycle\":\"ACTIVE\",\"fault\":{\"processingDelayMs\":200}}",
    "consumer-blue-1": "{\"lifecycle\":\"ACTIVE\",\"fault\":{\"processingDelayMs\":200}}",
    "consumer-blue-2": "{\"lifecycle\":\"ACTIVE\",\"fault\":{\"processingDelayMs\":200}}"
  }
}'
```

#### 장애 해제 방식 변경

```bash
# 변경 후: fault 필드 제거
kubectl patch configmap kafka-consumer-state -n kafka-bg-test --type merge -p '{
  "data": {
    "consumer-blue-0": "{\"lifecycle\":\"ACTIVE\"}",
    "consumer-blue-1": "{\"lifecycle\":\"ACTIVE\"}",
    "consumer-blue-2": "{\"lifecycle\":\"ACTIVE\"}"
  }
}'
```

#### P0 우회 방법 제거

시나리오 4에서 수동 pause가 불필요해진다:

```bash
# 변경 전 (test-guide.md 8절): 수동 복구 필요
for i in 0 1 2; do
  kubectl port-forward -n kafka-bg-test consumer-blue-$i 1808$i:8080 &
done
sleep 2
for i in 0 1 2; do
  curl -s -X POST http://localhost:1808$i/lifecycle/pause
done
pkill -f "kubectl port-forward.*consumer-blue" 2>/dev/null

# 변경 후: 자동 복구 (Sidecar reconcile)
# -> 수동 개입 불필요. Pod 재시작 후 ~25초 이내 자동으로 PAUSED 전환
```

### 헬퍼 스크립트 업데이트

```bash
# === 장애 주입 (ConfigMap 기반, port-forward 불필요) ===
inject_delay_cm() {
  local color=$1 delay=${2:-200}
  local patch='{"data":{'
  local first=true
  for i in 0 1 2; do
    if [ "$first" = true ]; then first=false; else patch+=','; fi
    local escaped="{\\\"lifecycle\\\":\\\"ACTIVE\\\",\\\"fault\\\":{\\\"processingDelayMs\\\":${delay}}}"
    patch+="\"consumer-${color}-$i\":\"${escaped}\""
  done
  patch+='}}'
  kubectl patch configmap kafka-consumer-state -n kafka-bg-test --type merge -p "$patch"
}

inject_error_rate_cm() {
  local color=$1 rate=${2:-80}
  local patch='{"data":{'
  local first=true
  for i in 0 1 2; do
    if [ "$first" = true ]; then first=false; else patch+=','; fi
    local escaped="{\\\"lifecycle\\\":\\\"ACTIVE\\\",\\\"fault\\\":{\\\"errorRatePercent\\\":${rate}}}"
    patch+="\"consumer-${color}-$i\":\"${escaped}\""
  done
  patch+='}}'
  kubectl patch configmap kafka-consumer-state -n kafka-bg-test --type merge -p "$patch"
}

# === 장애 해제 ===
clear_fault_cm() {
  local color=$1
  # 현재 lifecycle 상태를 유지하면서 fault만 제거
  local current_lifecycle
  current_lifecycle=$(kubectl get configmap kafka-consumer-state -n kafka-bg-test \
    -o jsonpath="{.data.consumer-${color}-0}" | \
    python3 -c "import sys,json; print(json.load(sys.stdin)['lifecycle'])")
  local patch='{"data":{'
  local first=true
  for i in 0 1 2; do
    if [ "$first" = true ]; then first=false; else patch+=','; fi
    local escaped="{\\\"lifecycle\\\":\\\"${current_lifecycle}\\\"}"
    patch+="\"consumer-${color}-$i\":\"${escaped}\""
  done
  patch+='}}'
  kubectl patch configmap kafka-consumer-state -n kafka-bg-test --type merge -p "$patch"
}

# === State ConfigMap 전체 확인 ===
check_state() {
  kubectl get configmap kafka-consumer-state -n kafka-bg-test -o json | \
    python3 -c "
import sys, json
cm = json.load(sys.stdin)
for k, v in sorted(cm['data'].items()):
    state = json.loads(v)
    fault = state.get('fault', {})
    fault_str = ', '.join(f'{fk}={fv}' for fk, fv in fault.items() if fv) if fault else 'none'
    print(f'  {k}: lifecycle={state[\"lifecycle\"]}, fault={fault_str}')
"
}
```

### 사용 예시 (변경 후)

```bash
# 장애 주입 (Blue에 200ms 지연)
inject_delay_cm blue 200

# State ConfigMap 확인
check_state

# 장애 해제
clear_fault_cm blue

# Green에 에러율 80% 주입
inject_error_rate_cm green 80

# 전체 Consumer 상태 확인 (기존 check_all과 병행)
check_all
check_state
```
