# ConfigMap 전파 지연 문제 연구: Sidecar 상태 전달 대안 분석

> **작성일:** 2026-02-22
> **선행 문서:** `after-task05.md` (아키텍처 개선안)
> **목적:** ConfigMap Volume Mount 방식의 전파 지연(~60-90초)이 롤백 시나리오에서 문제가 될 수 있는지 분석하고, 대안을 연구

---

## 목차

1. [문제 정의](#1-문제-정의)
2. [kubelet Volume Mount 전파 메커니즘](#2-kubelet-volume-mount-전파-메커니즘)
3. [대안 분석 (5가지)](#3-대안-분석-5가지)
4. [대안 비교표](#4-대안-비교표)
5. [추천안: Sidecar HTTP Endpoint + Controller Push](#5-추천안-sidecar-http-endpoint--controller-push)
6. [추천안 반영 시 after-task05.md 변경 사항](#6-추천안-반영-시-after-task05md-변경-사항)
7. [워크로드 타입별 호환성 분석 (Deployment / Argo Rollouts)](#7-워크로드-타입별-호환성-분석-deployment--argo-rollouts)
8. [결론](#8-결론)

---

## 1. 문제 정의

### 1.1 after-task05.md의 제안 아키텍처 요약

```
ConfigMap(kafka-consumer-state) 변경
    ↓
kubelet Volume Mount 갱신 (~60-90초)
    ↓
Sidecar File Polling (5초 주기) → 변경 감지 → Consumer 전환
```

- Controller의 HTTP 직접 호출이 **정상 경로**(~1초)를 담당
- Volume Mount + File Polling은 **fallback 경로**(~60-90초)를 담당

### 1.2 지연이 문제가 되는 시나리오

| 시나리오 | 주 경로 | 실제 지연 | Volume Mount 의존? |
|----------|---------|-----------|-------------------|
| 정상 전환 | Controller HTTP 직접 호출 | ~1초 | 아님 |
| Controller 발 롤백 | Controller HTTP 직접 호출 | ~1초 | 아님 |
| Consumer 재시작 (Controller 정상) | **Sidecar만** | **60-90초** | **의존** |
| Controller 크래시 중 전환 미완 | **Sidecar만** | **60-90초** | **의존** |
| Controller 다운 + 수동 롤백 | **Sidecar만** | **60-90초** | **의존** |

**핵심 질문:** Controller가 없을 때 Sidecar만으로 복구해야 하는 3가지 시나리오에서 60-90초 지연이 허용 가능한가?

### 1.3 대규모 클러스터 제약

실제 적용 환경에서 노드가 수천 개인 경우, **Sidecar가 K8s API Watch를 유지하는 패턴은 지양**해야 한다.

- 각 Sidecar가 API Watch를 유지하면 수만 개의 Watch stream 발생
- API 서버에 상당한 부하
- 따라서 Sidecar에서 K8s API 직접 호출은 선택지에서 제외

---

## 2. kubelet Volume Mount 전파 메커니즘

### 2.1 전파 경로

```
ConfigMap 변경 (API Server에 저장)
        ↓
kubelet이 변경 인지 (changeDetectionStrategy에 따라)
  - Watch 전략: 즉시 인지 (~ms)
  - TTL/Cache 전략: 캐시 만료 후 인지 (~60초)
        ↓
        ↓  ← 여기서 대기! syncPod()는 주기적으로만 호출됨
        ↓     syncFrequency(기본 60초) + jitter(최대 1.5배)
        ↓
syncPod() 실행 → Volume 파일 atomic swap (symlink 교체)
        ↓
Sidecar File Polling → 변경 감지
```

### 2.2 왜 회피 불가능한가

`configMapAndSecretChangeDetectionStrategy=Watch`를 설정해도 소용없는 이유:

| 단계 | 역할 | 제어 가능? |
|------|------|-----------|
| ConfigMap 내용 인지 | `changeDetectionStrategy`가 제어 | 가능 (Watch로 즉시 인지) |
| Volume 파일에 쓰기 | `syncPod()` 호출 시점에 종속 | **불가능** (syncFrequency 타이머) |

kubelet 내부에서 ConfigMap 내용 인지와 Volume 파일 갱신은 **별도의 비동기 경로**로 동작한다. 인지는 즉시 해도, 실제 파일 쓰기는 다음 `syncPod()` 주기를 기다린다.

### 2.3 syncFrequency 튜닝

minikube에서 syncFrequency를 조정할 수 있다:

```bash
minikube start \
  --kubernetes-version=v1.23.8 \
  --extra-config=kubelet.syncFrequency=10s \
  --extra-config=kubelet.configMapAndSecretChangeDetectionStrategy=Watch
```

| syncFrequency | 최소 지연 | 최대 지연 (jitter 포함) |
|---------------|-----------|-------------------------|
| 60초 (기본)   | ~60초     | ~90초                   |
| 30초          | ~30초     | ~45초                   |
| 10초          | ~10초     | ~15초                   |
| 5초           | ~5초      | ~7.5초                  |

**주의사항:**
- minikube 재생성 필요 (`minikube delete && minikube start`)
- 클러스터 레벨 설정으로 프로덕션 이식성 없음
- 단일 노드 테스트 환경에서는 10초 설정이 안전

### 2.4 관련 K8s 이슈

- [kubernetes/kubernetes#49650](https://github.com/kubernetes/kubernetes/issues/49650): Volume refresh 주기를 별도로 튜닝하는 기능 요청 (미구현)
- [kubernetes/kubernetes#30189](https://github.com/kubernetes/kubernetes/issues/30189): ConfigMap refresh 시간에 대한 논의

---

## 3. 대안 분석 (5가지)

### 대안 A: Raw HTTP Watch (client-go 없이 K8s Watch API 직접 사용)

Sidecar가 Go 표준 라이브러리 `net/http`만으로 K8s API Watch를 직접 수행.

```
GET /api/v1/namespaces/{ns}/configmaps?watch=true&fieldSelector=metadata.name={name}
Authorization: Bearer <ServiceAccount token>
```

**구현 개요 (~150줄):**
- ServiceAccount 토큰: `/var/run/secrets/kubernetes.io/serviceaccount/token`
- CA 인증서: `/var/run/secrets/kubernetes.io/serviceaccount/ca.crt`
- API 서버 주소: `KUBERNETES_SERVICE_HOST:KUBERNETES_SERVICE_PORT`
- Watch 응답: chunked HTTP stream, 줄 단위 JSON (ADDED, MODIFIED, DELETED, BOOKMARK 이벤트)
- 재연결: EOF/timeout 시 마지막 `resourceVersion`으로 재연결
- 410 Gone 처리: `resourceVersion` 리셋 후 재시작

| 항목 | 값 |
|------|-----|
| 전파 지연 | ~밀리초 (Watch 이벤트) |
| 바이너리 크기 | ~8-12 MB (client-go ~50-65MB 대비 5-6배 축소) |
| 추가 코드량 | ~150줄 (Watch + reconnect + 410 Gone 처리) |
| RBAC 필요 | Yes (기존 `bg-switch-sa`로 충분) |
| client-go 의존성 | **제거** |

**장점:**
- client-go 제거 목표 달성
- 전파 지연 극적 개선 (~ms)
- K8s Watch API는 stable v1로 버전 호환 문제 없음

**단점:**
- **수천 노드에서 Watch stream 수만 개** → API 서버 부하 문제 미해결
- Sidecar에 RBAC 여전히 필요
- Watch 재연결/410 Gone 처리 직접 구현 필요

**결론:** client-go 제거는 달성하지만, **대규모 클러스터 스케일 문제를 해결하지 못함**. 제외.

---

### 대안 B: kubelet syncFrequency 튜닝

코드 변경 없이 클러스터 설정으로 지연 감소.

| 항목 | 값 |
|------|-----|
| 전파 지연 | 10-15초 (syncFrequency=10s 시) |
| 코드 변경 | 없음 |
| 클러스터 변경 | 필요 (minikube 재생성) |

**장점:**
- 코드 변경 불필요
- Volume Mount + File Polling 아키텍처 유지

**단점:**
- 클러스터 레벨 설정 → 프로덕션 이식성 없음
- 여전히 10-15초 지연 (실시간 대비 느림)
- minikube 재생성 필요

**결론:** 연구/테스트 환경의 **보조 수단**으로만 활용 가능. 아키텍처 해결책은 아님.

---

### 대안 C: Controller → Sidecar HTTP Push

Controller가 전환 시 Consumer뿐 아니라 Sidecar에도 HTTP로 desired state를 push.

```
Controller
  ├── (1) ConfigMap에 desired state 기록 (source of truth)
  ├── (2) Consumer에 HTTP 직접 호출 pause/resume (즉시 전환)
  └── (3) Sidecar에 HTTP push desired state (알림)  ← 신규
```

| 항목 | 값 |
|------|-----|
| 전파 지연 | ~1초 (Controller 경유) |
| Sidecar 변경 | HTTP endpoint 1개 추가 (`POST /desired-state`) |
| Controller 변경 | Sidecar endpoint 호출 로직 추가 |
| K8s API 사용 (Sidecar) | 없음 |

**장점:**
- Sidecar에서 K8s API 의존성 완전 제거
- Controller가 정상이면 전파 즉시

**단점:**
- **Controller가 다운되면 push 불가** → Volume Mount fallback (60-90초)
- 핵심 문제("Controller 없을 때 빠른 롤백")를 단독으로 해결하지 못함

**결론:** 단독으로는 불완전하지만, **Volume Mount와 결합하면 대부분의 시나리오를 커버**.

---

### 대안 D: Pod Annotation Trick (Volume Mount 즉시 갱신)

Pod annotation이 변경되면 kubelet이 해당 Pod를 즉시 `syncPod()` 큐에 넣는 동작을 이용.

```
Controller
  ├── (1) ConfigMap 기록
  ├── (2) HTTP 즉시 전환
  └── (3) Pod annotation 패치 → kubelet 즉시 sync 트리거
                ↓
kubelet이 Pod 변경 감지 → syncPod() 즉시 실행 → Volume Mount 갱신 (~1-2초)
```

| 항목 | 값 |
|------|-----|
| 전파 지연 | ~1-2초 (annotation 트리거 시) |
| 추가 RBAC | Pod `patch` 권한 필요 |
| Controller 변경 | Pod annotation patch 추가 |

**장점:**
- Volume Mount + File Polling 아키텍처 유지
- Sidecar에 K8s API 의존성 불필요

**단점:**
- **Controller가 다운되면 annotation patch 불가** → 대안 C와 동일 문제
- Pod 객체 patch는 추가 RBAC 필요
- kubelet 내부 동작에 의존하는 비공식 기법
- 수천 Pod annotation을 동시 patch하면 API 서버 부하

**결론:** 흥미로운 기법이지만 **비공식적이고 Controller 의존 문제가 동일**. 제외.

---

### 대안 E: Downward API (Pod Annotations Volume Mount)

Pod annotation을 Downward API Volume으로 마운트.

```yaml
volumes:
- name: podinfo
  downwardAPI:
    items:
    - path: "annotations"
      fieldRef:
        fieldPath: metadata.annotations
```

**결론: 동일한 지연 문제.** Downward API Volume도 ConfigMap Volume과 **동일한 kubelet syncPod() 주기**에 종속. 전파 속도 이점 없음.

---

## 4. 대안 비교표

| 기준 | 원안 (Volume Mount) | A: Raw HTTP Watch | B: syncFrequency 튜닝 | C: Controller Push | D: Annotation Trick |
|------|---------------------|-------------------|-----------------------|-------------------|---------------------|
| **정상 전환 지연** | ~1초 | ~1초 | ~1초 | ~1초 | ~1초 |
| **Controller 다운 시 지연** | **60-90초** | **~ms** | **10-15초** | **60-90초** | **60-90초** |
| **Consumer 재시작 복구** | 60-90초 | ~5초 | 10-15초 | **~5초** (캐시) | 60-90초 |
| client-go 제거 | Yes | Yes | Yes | Yes | Yes |
| Sidecar K8s API 의존 없음 | **Yes** | No | **Yes** | **Yes** | **Yes** |
| 대규모 클러스터 호환 | **Yes** | **No** (Watch 수만 개) | 설정 이식성 없음 | **Yes** | 부분적 |
| 구현 복잡도 | 낮음 | 중간 (~150줄) | 없음 (설정만) | 낮음 | 중간 |

---

## 5. 추천안: Sidecar HTTP Endpoint + Controller Push

### 5.1 선택 근거

**대안 C(Controller Push)를 원안(Volume Mount + File Polling)에 결합**하는 것이 최선이다.

세 가지 설계 목표를 동시에 충족:

| 목표 | 충족 방법 |
|------|-----------|
| Sidecar에서 K8s API 미사용 | Volume Mount(파일 읽기) + HTTP endpoint(Controller push 수신) |
| 빠른 전파 | Controller가 Sidecar에 HTTP push (~1초) |
| 대규모 클러스터 호환 | Watch stream 없음, HTTP push는 전환 시에만 발생 |

### 5.2 전체 아키텍처 (4-레이어 안전망)

```
                          Controller
                        /     |     \
               (1) ConfigMap  (2) Consumer   (3) Sidecar HTTP    ← 신규
                   Write       HTTP 직접      push desired state
                     ↓         pause/resume       ↓
              kafka-consumer-     ↓          Sidecar 메모리 캐시
              state (영속)    Consumer가        ↓
                     ↓        즉시 전환     (4) Reconcile loop
              kubelet sync                  desired vs actual 비교
              (60-90초)                     불일치 시 Consumer 전환
                     ↓
              Volume Mount 파일 갱신
              Sidecar File Polling
              (최후의 fallback)
```

### 5.3 4-레이어 안전망

| 레이어 | 역할 | 지연 | 실패 조건 |
|--------|------|------|-----------|
| L1: Controller → Consumer HTTP | 즉시 전환 | ~1초 | Controller 다운 |
| L2: Controller → Sidecar HTTP push | Sidecar에 desired state 전달 | ~1초 | Controller 다운 |
| L3: Sidecar Reconcile Loop | desired(캐시) vs actual 비교 | 5초 주기 | Sidecar 재시작 (캐시 소실) |
| L4: Volume Mount File Polling | 파일에서 desired state 읽기 | 60-90초 | kubelet 장애 |

### 5.4 시나리오별 동작

#### 정상 전환

```
1. Controller → ConfigMap write (source of truth)
2. Controller → Consumer HTTP pause/resume (L1: 즉시 전환)
3. Controller → Sidecar HTTP push desired state (L2: 캐시 갱신)
4. Sidecar reconcile loop: desired(캐시)와 actual 일치 확인 (L3: 무동작)
→ 결과: ~1초
```

#### Consumer 재시작 (Controller 정상)

```
1. Consumer 재시작 → 기본 PAUSED로 기동
2. Sidecar는 메모리에 desired state 보유 (L2에서 Controller가 push 해둠)
3. L3 reconcile loop (5초): desired=ACTIVE, actual=PAUSED → resume 호출
→ 결과: ~5초 (Volume Mount 60-90초 대비 대폭 개선)
```

#### Consumer + Sidecar 모두 재시작 (Controller 정상)

```
1. Consumer 재시작 → 기본 PAUSED
2. Sidecar 재시작 → 메모리 캐시 소실
3. Sidecar 초기화: Volume Mount 파일에서 desired state 읽기 (L4)
   - 파일이 이미 갱신되어 있으면 → 즉시 reconcile
   - 파일이 아직 이전 상태면 → 다음 kubelet sync까지 대기
4. Controller가 살아있으면 다음 전환 시 L2로 다시 push
→ 결과: 파일 갱신 상태에 따라 즉시 ~ 60-90초
```

#### Controller 크래시 중 전환 미완

```
1. Controller가 ConfigMap write(L1 Step 1) 후 HTTP(Step 2) 전에 크래시
2. Consumer는 아직 전환되지 않음
3. Controller 재시작 (Deployment 자동 복구, 수 초)
4. Controller가 현재 ConfigMap 읽어 초기화 → L1 + L2 다시 수행
→ 결과: Controller 복구 시간(수 초) + ~1초
```

#### Controller 다운 + 수동 롤백 (가장 불리한 시나리오)

```
1. Controller 완전 다운 (복구 불가)
2. 운영자가 ConfigMap patch로 롤백 시도
3. L2(Controller push) 불가
4. L4(Volume Mount) 경로만 가능 → 60-90초 지연

대안 1: 수동 Sidecar push 스크립트로 우회 (~1초)
대안 2: syncFrequency=10s 튜닝으로 10-15초로 단축
```

### 5.5 Sidecar 변경 상세

Sidecar의 기존 HTTP 서버(`:8082`)에 endpoint 추가:

```go
// main.go의 mux에 추가
mux.HandleFunc("/desired-state", reconciler.HandleDesiredState)
```

```go
// reconciler.go에 추가

// HandleDesiredState receives desired state push from Controller.
func (r *Reconciler) HandleDesiredState(w http.ResponseWriter, req *http.Request) {
    if req.Method != http.MethodPost {
        w.WriteHeader(http.StatusMethodNotAllowed)
        return
    }

    var state DesiredState
    if err := json.NewDecoder(req.Body).Decode(&state); err != nil {
        w.WriteHeader(http.StatusBadRequest)
        return
    }

    r.mu.Lock()
    r.cachedDesired = &state  // 메모리 캐시 갱신
    r.mu.Unlock()

    r.logger.Info("received desired state push",
        "lifecycle", state.Lifecycle)

    w.WriteHeader(http.StatusOK)
}
```

reconcileOnce 수정:

```
func reconcileOnce():
  1. desired state 결정 (우선순위):
     a. cachedDesired가 있으면 사용 (Controller push로 받은 최신 상태)
     b. 없으면 Volume Mount 파일에서 읽기 (fallback)

  2. 이하 기존 로직과 동일 (actual state 조회, 비교, 전환)
```

### 5.6 Controller 변경 상세

`handleSwitch()`에서 Sidecar에도 push 추가:

```go
// handleSwitch 내 추가 (Step 2와 Step 3 사이)
// Sidecar에 desired state push (best-effort, 실패해도 전환 진행)
sc.pushDesiredStateToSidecars(ctx, oldEndpoints, DesiredState{Lifecycle: "PAUSED"})
sc.pushDesiredStateToSidecars(ctx, newEndpoints, DesiredState{Lifecycle: "ACTIVE"})
```

```go
// pushDesiredStateToSidecars sends desired state to sidecar HTTP endpoints.
// This is best-effort: failures are logged but do not block the switch.
func (sc *SwitchController) pushDesiredStateToSidecars(
    ctx context.Context,
    consumerEndpoints []string,  // "10.0.0.1:8080" 형태
    state DesiredState,
) {
    body, _ := json.Marshal(state)
    for _, ep := range consumerEndpoints {
        // Consumer port(8080)에서 Sidecar port(8082)로 변환
        sidecarEP := strings.Replace(ep, ":"+sc.config.LifecyclePort, ":8082", 1)
        url := fmt.Sprintf("http://%s/desired-state", sidecarEP)

        req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
        req.Header.Set("Content-Type", "application/json")

        resp, err := sc.httpClient.Do(req)
        if err != nil {
            sc.logger.Warn("failed to push desired state to sidecar",
                "endpoint", sidecarEP, "error", err)
            continue
        }
        resp.Body.Close()
    }
}
```

### 5.7 긴급 수동 복구 스크립트

Controller가 완전히 다운된 상태에서 즉시 롤백이 필요한 운영 상황을 위한 escape hatch:

```bash
# === 긴급 수동 복구: Controller 없이 Sidecar에 직접 push ===
# Sidecar HTTP endpoint를 통해 desired state를 직접 전달
# K8s API는 kubectl exec가 사용하며, Sidecar 자체는 K8s API를 호출하지 않음

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

# 사용 예: Blue를 PAUSED, Green을 ACTIVE로 긴급 전환
emergency_set_state blue PAUSED
emergency_set_state green ACTIVE
```

---

## 6. 추천안 반영 시 after-task05.md 변경 사항

### 6.1 결정 사항 추가

**결정 7: Controller → Sidecar HTTP Push 추가**

Controller가 전환 시 Consumer HTTP 호출과 함께 Sidecar에도 desired state를 HTTP push한다.

**근거:**
- Sidecar가 K8s API 없이도 Controller로부터 즉시 desired state를 수신
- Consumer 재시작 시 Sidecar의 메모리 캐시로 ~5초 내 복구 (Volume Mount 대기 불필요)
- Push는 best-effort로 실패해도 전환에 영향 없음 (Volume Mount가 최후의 fallback)

### 6.2 아키텍처 데이터 흐름 수정

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
        +-- (3) HTTP POST: Sidecar /desired-state push (캐시 갱신)  ← 신규
        |
        v
 kafka-consumer-state ConfigMap
        |
        | (Volume Mount -> kubelet 파일 갱신, ~60초 이내)
        v
 Switch Sidecar (각 Pod)
        |
        | Reconcile loop (5초 주기):
        |   desired 소스: (a) Controller push 캐시 > (b) Volume Mount 파일
        |   desired vs actual 비교 → 불일치 시 HTTP POST /pause 또는 /resume
        v
    Consumer lifecycle API
```

### 6.3 Reconciler 설계 수정

Reconciler 구조체에 `cachedDesired` 필드 추가:

```go
type Reconciler struct {
    statePath       string            // "/etc/consumer-state"
    myHostname      string            // "consumer-blue-0"
    lifecycleClient *lifecycle.Client
    logger          *slog.Logger
    metrics         *metrics.Metrics
    pollInterval    time.Duration     // 5s
    lastApplied     *DesiredState     // last successfully applied state
    cachedDesired   *DesiredState     // from Controller HTTP push (신규)
    mu              sync.RWMutex      // protects cachedDesired (신규)
}
```

reconcileOnce 알고리즘 수정:

```
func reconcileOnce():
  1. Determine desired state:
     a. r.mu.RLock(); cached := r.cachedDesired; r.mu.RUnlock()
     b. if cached != nil -> use cached (Controller push 경로, 최우선)
     c. else -> Read file: /etc/consumer-state/{myHostname} (Volume Mount fallback)

  2~7. 기존과 동일
```

### 6.4 Phase 2 작업 추가

Phase 2(Sidecar 재작성)에 추가:

- Sidecar HTTP 서버에 `POST /desired-state` endpoint 추가
- Reconciler에 `cachedDesired` 필드 및 `HandleDesiredState()` 메서드 추가

### 6.5 Phase 3 작업 추가

Phase 3(Controller 확장)에 추가:

- Controller에 `pushDesiredStateToSidecars()` 메서드 추가
- `handleSwitch()`에서 Consumer HTTP 호출 후 Sidecar push 수행

### 6.6 Edge Case 수정

**Case 4 (Container 재시작) 개선:**
- 변경 전: Sidecar도 재시작 시 Volume Mount 의존 (~60-90초)
- 변경 후: Consumer만 재시작 시 Sidecar 캐시로 ~5초 복구. Sidecar도 재시작 시에만 Volume Mount 의존.

**Case 5 (ConfigMap 전파 지연) 개선:**
- 변경 전: Sidecar가 이전 desired state를 보고 Controller 전환을 되돌릴 위험
- 변경 후: Controller push로 캐시가 갱신되므로 Volume Mount 갱신 전에도 올바른 desired state 보유. 되돌림 위험 제거.

**Case 7 (신규): Controller push 실패**

시나리오: Controller가 Sidecar push(Step 3)에 실패

동작:
- Consumer HTTP 직접 호출(Step 2)은 이미 성공 → 전환 완료
- Sidecar 캐시 미갱신 → Volume Mount 갱신 시점에 파일에서 읽어 캐시 갱신
- 다음 전환 시 Controller가 다시 push 시도
- **결과:** 정상 동작에 영향 없음. Sidecar의 reconcile이 다음 주기에 정상화

### 6.7 역할 분리표 수정

| 컴포넌트 | 현재 역할 | 제안 역할 (수정) |
|----------|-----------|-----------------|
| Switch Controller | ConfigMap Watch에서 HTTP 직접 전환 | State 기록 + Consumer HTTP + **Sidecar HTTP push** |
| Switch Sidecar | ConfigMap API Watch에서 HTTP 전환 | **HTTP push 수신(캐시)** + File Polling Reconcile (fallback) |

---

## 7. 워크로드 타입별 호환성 분석 (Deployment / Argo Rollouts)

> **작성일:** 2026-02-22
> **목적:** 4-레이어 안전망 아키텍처가 StatefulSet 외에 Deployment, Argo Rollouts에서도 적용 가능한지 분석

### 7.1 현재 아키텍처의 StatefulSet 의존 지점

현재 설계는 3가지 핵심 지점에서 StatefulSet에 의존한다:

| 의존 지점 | 사용 위치 | StatefulSet이 보장하는 것 |
|-----------|-----------|--------------------------|
| **Pod 이름 안정성** | `group.instance.id = ${HOSTNAME}` | `consumer-blue-0` 같은 이름이 재시작 후에도 유지 |
| **ConfigMap 키 구조** | `consumer-blue-0: '{"lifecycle":"PAUSED"}'` | 키가 Pod 이름이므로 예측 가능 |
| **Sidecar 파일 읽기** | `/etc/consumer-state/{myHostname}` | 자기 자신의 desired state를 정확히 식별 |

**Deployment와 Argo Rollouts의 근본 차이:**
- StatefulSet: Pod에 안정적 ID(ordinal index) 부여 → `consumer-blue-0`
- Deployment: ReplicaSet이 관리하는 랜덤 suffix Pod 생성 → `consumer-blue-7f8b9d6c4-x2k9p`
- Argo Rollouts: 내부적으로 ReplicaSet 사용 → Deployment와 동일한 Pod 이름 특성

### 7.2 레이어별 호환성 분석

#### L1: Controller → Consumer HTTP (즉시 전환) — 호환

```
Controller → Service Endpoints 조회 → Pod IP 목록 → HTTP POST /pause, /resume
```

| 항목 | StatefulSet | Deployment | Argo Rollouts |
|------|-------------|------------|---------------|
| Service Endpoints | Headless Service | 일반 ClusterIP Service | Argo가 관리하는 Service |
| Pod IP 발견 | `getServiceEndpoints()` | **동일하게 동작** | **동일하게 동작** |
| 결론 | 동작 | **호환** | **호환** |

L1은 Pod IP 기반이므로 워크로드 타입 무관하게 동작한다. Controller가 Service의 Endpoints 리소스에서 Pod IP를 조회하는 방식은 모두 동일하다.

#### L2: Controller → Sidecar HTTP push (캐시 갱신) — 호환

```
Controller → Sidecar HTTP push desired state → 메모리 캐시 갱신
```

| 항목 | StatefulSet | Deployment | Argo Rollouts |
|------|-------------|------------|---------------|
| Sidecar endpoint 발견 | Consumer IP:8082 | **동일** | **동일** |
| push 내용 | `{"lifecycle":"ACTIVE"}` | **동일** | **동일** |
| 결론 | 동작 | **호환** | **호환** |

L2도 Pod IP 기반이므로 완전 호환. Sidecar port(8082)로 변환하는 로직만 있으면 된다.

#### L3: Sidecar Reconcile Loop (캐시 vs actual 비교) — 호환

```
Sidecar 메모리: desired(L2 캐시) vs actual(Consumer HTTP 상태 조회) → 불일치 시 전환
```

| 항목 | StatefulSet | Deployment | Argo Rollouts |
|------|-------------|------------|---------------|
| desired 소스 | 메모리 캐시 | **동일** | **동일** |
| actual 조회 | localhost:8080/lifecycle/status | **동일** | **동일** |
| Pod 이름 의존 | 없음 (메모리 내 비교) | **동일** | **동일** |
| 결론 | 동작 | **호환** | **호환** |

L3는 Pod 내부 통신(localhost)과 메모리 캐시만 사용하므로 완전 호환.

#### L4: Volume Mount File Polling (최후의 fallback) — 비호환

```
ConfigMap Volume → /etc/consumer-state/{myHostname} 파일 읽기
```

| 항목 | StatefulSet | Deployment | Argo Rollouts |
|------|-------------|------------|---------------|
| Pod 이름 | `consumer-blue-0` (안정적) | `consumer-blue-7f8b9d-x2k9p` (랜덤) | 동일 (랜덤) |
| ConfigMap 키 | `consumer-blue-0: {...}` (사전 생성 가능) | **키를 사전에 알 수 없음** | **동일 문제** |
| 파일 경로 | `/etc/consumer-state/consumer-blue-0` | **파일이 존재하지 않음** | **동일 문제** |
| 결론 | 동작 | **비호환** | **비호환** |

### 7.3 L4 비호환 해결 방안

L1~L3은 모두 호환이고 L4만 비호환이므로, L4의 ConfigMap 키 구조만 변경하면 해결된다.

#### 방안 A: ConfigMap 키를 BlueGreen 단위로 변경 (권장)

현재 (StatefulSet 전용):
```yaml
data:
  consumer-blue-0:  '{"lifecycle":"PAUSED"}'
  consumer-blue-1:  '{"lifecycle":"PAUSED"}'
  consumer-blue-2:  '{"lifecycle":"PAUSED"}'
  consumer-green-0: '{"lifecycle":"ACTIVE"}'
```

변경 (워크로드 타입 무관):
```yaml
data:
  blue:  '{"lifecycle":"PAUSED"}'
  green: '{"lifecycle":"ACTIVE"}'
```

Sidecar 변경:
```
기존: /etc/consumer-state/{myHostname}  → consumer-blue-0 파일 읽기
변경: /etc/consumer-state/{myColor}     → blue 파일 읽기
```

| 항목 | 평가 |
|------|------|
| 장점 | 단순, 모든 워크로드 타입에서 동작, Pod 수 변경에 자동 대응 |
| 단점 | Pod 개별 제어 불가 (같은 색상의 모든 Pod이 동일 상태) |
| 단점 영향 | 현재 설계에서 같은 색상의 Pod은 항상 동일 상태이므로 실질적 단점 없음 |

**참고:** `after-task05.md`의 Controller 로직(`updateStateConfigMap`)은 같은 색상의 모든 Pod에 동일한 lifecycle 값을 일괄 적용한다. 따라서 Pod 단위 키는 오버 엔지니어링이며, BlueGreen 단위 키가 더 적절하다. 이 변경은 Deployment 호환성뿐 아니라 설계 단순화 효과도 있다.

#### 방안 B: L4를 제거하고 L2+L3에 강화된 fallback 추가

L2(Controller push) 실패 + L3(캐시) 비어있을 때:
- Sidecar가 기본 PAUSED 적용 (Consumer 기본 PAUSED 정책과 일치)
- Controller 복구 시 L2로 다시 push

| 항목 | 평가 |
|------|------|
| 장점 | Volume Mount 의존성 완전 제거, 아키텍처 단순화 |
| 단점 | Controller 완전 다운 시 수동 복구만 가능 (긴급 스크립트 의존) |

### 7.4 Kafka Static Membership 호환성

4-레이어 안전망과 별개로, Kafka Static Membership 자체가 StatefulSet에 강하게 의존한다:

| 워크로드 타입 | group.instance.id | Rebalance 동작 |
|---------------|-------------------|----------------|
| **StatefulSet** | `consumer-blue-0` (안정) | Pod 재시작 시 동일 ID → **Rebalance 없음** |
| **Deployment** | `consumer-blue-7f8b9d-x2k9p` (랜덤) | Pod 재시작 시 새 ID → **항상 Rebalance 발생** |
| **Argo Rollouts** | 랜덤 (Deployment과 동일) | **항상 Rebalance 발생** |

Static Membership의 핵심 가치는 `session.timeout.ms`(기본 45초) 동안 Rebalance를 지연시켜 짧은 재시작에서 파티션 재할당을 방지하는 것이다. Deployment에서는 이 이점이 사라진다.

#### 해결 방안

| 방법 | 설명 | 적합 환경 |
|------|------|-----------|
| **Static Membership 포기** | CooperativeStickyAssignor만으로 운영, Rebalance 허용 | Deployment, Argo Rollouts |
| **PVC에 ID 저장** | Pod 시작 시 PVC에서 고유 ID 읽기, 없으면 생성 | StatefulSet with PVC만 가능 |
| **외부 ID 할당** | Init Container에서 Redis/etcd에서 ID 할당 | 복잡, 비권장 |

**권장:** Deployment/Argo Rollouts에서는 Static Membership을 포기하고 CooperativeStickyAssignor에 의존. Blue/Green 전환의 핵심은 Pause/Resume이지 파티션 할당 안정성이 아니므로, Rebalance가 발생해도 `PauseAwareRebalanceListener`가 방어한다.

### 7.5 Argo Rollouts 특수 고려사항

Argo Rollouts는 자체 Blue/Green 전략을 제공하지만, Kafka Consumer에는 그대로 적용할 수 없다:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Rollout
spec:
  strategy:
    blueGreen:
      activeService: consumer-active-svc
      previewService: consumer-preview-svc
      autoPromotionEnabled: false
```

| 항목 | 현재 아키텍처 (자체 B/G) | Argo Rollouts B/G |
|------|--------------------------|-------------------|
| 전환 트리거 | ConfigMap patch | `kubectl argo rollouts promote` |
| 전환 메커니즘 | Pause/Resume HTTP | **Service selector 교체** |
| 롤백 | ConfigMap 원복 | `kubectl argo rollouts abort` |
| Consumer 재시작 | 불필요 (Pause/Resume) | **새 ReplicaSet 생성 (Pod 재시작)** |

**핵심 차이:** Argo의 Blue/Green은 Service selector를 교체하여 트래픽을 전환하지만, Kafka Consumer는 트래픽이 아닌 **파티션 할당**으로 동작한다. Service selector 교체는 HTTP 기반 앱에는 적합하지만, Kafka Consumer에는 의미가 없다.

**결론:** Argo Rollouts의 B/G 전략을 그대로 쓸 수 없고, 현재의 Pause/Resume 메커니즘을 유지하면서 Argo Rollouts를 **배포 관리 도구(이미지 업데이트, 롤아웃 관리)**로만 활용해야 한다.

### 7.6 종합 호환성 매트릭스

| 레이어 | StatefulSet | Deployment | Argo Rollouts | 비호환 시 해결책 |
|--------|-------------|------------|---------------|-----------------|
| **L1** Controller→Consumer HTTP | 호환 | 호환 | 호환 | - |
| **L2** Controller→Sidecar push | 호환 | 호환 | 호환 | - |
| **L3** Sidecar Reconcile (캐시) | 호환 | 호환 | 호환 | - |
| **L4** Volume Mount File Polling | 호환 | **비호환** | **비호환** | ConfigMap 키를 BlueGreen 단위로 변경 |
| **Static Membership** | 호환 | **비호환** | **비호환** | 포기, CooperativeStickyAssignor만 사용 |
| **Argo B/G 전략 활용** | N/A | N/A | **부분 호환** | Kafka Consumer는 Pause/Resume 유지 |

### 7.7 요약

4-레이어 안전망 자체는 **L4만 수정하면 Deployment/Argo Rollouts에서도 적용 가능**하다.

1. **L1~L3 (전환 지연의 99% 커버)**: Pod IP 기반이므로 워크로드 타입 무관 동작
2. **L4 (최후의 fallback)**: ConfigMap 키를 `consumer-blue-0` → `blue`로 변경하면 해결
3. **Static Membership**: Deployment/Argo에서는 포기하되, `PauseAwareRebalanceListener`가 Rebalance를 방어하므로 Pause/Resume 전환의 안전성은 유지
4. **Argo Rollouts**: B/G 전략은 Kafka Consumer에 부적합, 배포 관리 도구로만 활용 권장

Controller HTTP push(L2)가 핵심 경로인 설계 덕분에 Volume Mount(L4) 의존도가 이미 낮다. L4가 비호환이더라도 실제 영향은 "Controller 완전 다운 + Sidecar 재시작"이라는 극히 드문 시나리오에 한정된다.

---

## 8. 결론

### 8.1 핵심 판단

| 질문 | 답변 |
|------|------|
| ConfigMap Volume Mount 지연은 회피 가능한가? | **불가능** (kubelet syncPod() 구조적 제약) |
| Sidecar에서 K8s API Watch는 사용 가능한가? | **지양** (대규모 클러스터에서 수만 Watch stream 부하) |
| 어떻게 해결해야 하는가? | **Controller → Sidecar HTTP push** 추가로 대부분의 시나리오 커버, Volume Mount는 최후의 fallback으로 유지 |
| Deployment/Argo Rollouts에서도 적용 가능한가? | **가능** (L4의 ConfigMap 키를 BlueGreen 단위로 변경 + Static Membership 포기) |

### 8.2 설계 원칙

**"지연을 제거"가 아니라 "지연이 문제 안 되게" 설계:**

1. **Consumer 기본 PAUSED** → 지연 중에도 안전 (Dual-Active 불가)
2. **Controller HTTP → Consumer** → 정상 경로에서 지연 없음 (~1초)
3. **Controller HTTP → Sidecar push** → Sidecar 캐시로 재시작 복구 ~5초
4. **Volume Mount + File Polling** → 모든 것이 실패해도 결국 수렴 (eventual consistency)

이 4개 레이어가 겹쳐서 **60-90초 지연이 실제로 문제가 되는 경우는 "Controller 완전 다운 + Sidecar 재시작 + Volume Mount 미갱신"이 동시에 발생하는 극히 드문 edge case**로 한정된다.

### 8.3 잔여 지연에 대한 수용

| 시나리오 | 추천안 적용 후 지연 | 위험도 | 수용 근거 |
|----------|---------------------|--------|-----------|
| 정상 전환 | ~1초 | 없음 | - |
| Consumer 재시작 | ~5초 | 낮음 | Sidecar 캐시로 즉시 복구 |
| Controller 크래시 | Controller 복구 시간 + ~1초 | 낮음 | Deployment 자동 복구 |
| Controller 다운 + 수동 롤백 | 60-90초 또는 수동 ~1초 | 중간 | 긴급 스크립트로 우회 가능 |
| 전체 Pod 재시작 | 60-90초 | 낮음 | 기본 PAUSED로 Dual-Active 불가 |
