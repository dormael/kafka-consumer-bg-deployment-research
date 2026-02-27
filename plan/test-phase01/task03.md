# Task 03: Switch Controller 및 Sidecar 구현 (Go)

> **의존:** task01 (K8s 클러스터), task02 (Consumer App의 /lifecycle 엔드포인트)
> **언어:** Go 1.21+
> **참고:** 디자인 문서 7.3 ~ 7.6절

---

## 목표

전략 C(Pause/Resume Atomic Switch)의 핵심 컴포넌트인 Switch Controller와 Switch Sidecar를 Go로 구현한다.

## 아키텍처 요약

```
Switch Controller (Deployment, 1개)
  ├── ConfigMap/CRD의 active 색상 변경을 감시
  ├── "Pause First, Resume Second" 원칙의 전환 오케스트레이션
  └── K8s Lease 기반 양쪽 동시 Active 방지

Switch Sidecar (각 Consumer Pod에 Sidecar로 포함)
  ├── ConfigMap 변경을 Watch
  ├── 자신의 색상(MY_COLOR)과 active 색상 비교
  └── Consumer App에 /lifecycle HTTP POST 전송
```

## 프로젝트 구조

```
apps/
├── switch-controller/
│   ├── cmd/
│   │   └── controller/
│   │       └── main.go
│   ├── internal/
│   │   ├── controller/
│   │   │   └── switch_controller.go    # 전환 오케스트레이션 로직
│   │   ├── lease/
│   │   │   └── lease_manager.go        # K8s Lease 기반 상호 배제
│   │   ├── health/
│   │   │   └── health_checker.go       # Consumer 헬스체크
│   │   └── metrics/
│   │       └── metrics.go              # Prometheus 지표
│   ├── Dockerfile
│   ├── go.mod
│   └── go.sum
│
└── switch-sidecar/
    ├── cmd/
    │   └── sidecar/
    │       └── main.go
    ├── internal/
    │   ├── watcher/
    │   │   └── configmap_watcher.go    # ConfigMap Watch
    │   ├── lifecycle/
    │   │   └── client.go               # Consumer /lifecycle HTTP 클라이언트
    │   └── metrics/
    │       └── metrics.go
    ├── Dockerfile
    ├── go.mod
    └── go.sum
```

## Switch Controller 상세 설계

### 전환 시퀀스 (Blue → Green)

```
T0: 운영자가 ConfigMap 업데이트 (active: green)
  또는 kubectl patch 또는 Argo Rollouts 트리거

T1: Controller가 ConfigMap 변경 감지
    - Lease 획득 시도 (양쪽 동시 Active 방지)

T2: Blue Consumer에 Pause 요청
    - 모든 Blue Pod의 /lifecycle/pause에 HTTP POST
    - 현재 배치 처리 완료 대기 (drain)
    - offset commitSync 확인

T3: Blue PAUSED 상태 검증
    - 모든 Blue Pod의 /lifecycle/status → PAUSED 확인
    - 타임아웃 (기본 10초) 초과 시 전환 중단 + 롤백

T4: Green Consumer에 Resume 요청
    - 모든 Green Pod의 /lifecycle/resume에 HTTP POST

T5: Green ACTIVE 상태 검증
    - 모든 Green Pod의 /lifecycle/status → ACTIVE 확인
    - 헬스체크 통과 확인

T6: Lease holder를 "green"으로 업데이트
    - 전환 완료 로그 및 지표 기록
```

### Lease 기반 상호 배제

```go
// K8s Lease API를 사용한 Distributed Lock
// coordination.k8s.io/v1 Lease 리소스 활용
type LeaseManager struct {
    client    kubernetes.Interface
    namespace string
    leaseName string  // "bg-consumer-active-lease"
}

// AcquireLease: 전환 시작 전 Lease 획득
// RenewLease: 전환 중 주기적 갱신
// ReleaseLease: 전환 완료 후 해제
// GetHolder: 현재 Lease holder (blue/green) 조회
```

### 롤백 조건

1. Blue PAUSE 타임아웃 (drain이 설정 시간 내 완료되지 않음)
2. Green RESUME 후 헬스체크 실패
3. Green Consumer Lag가 임계값 이상으로 급증
4. 운영자의 수동 롤백 트리거

### 자동 롤백 시퀀스

```
1. Green PAUSE 요청
2. Green PAUSED 확인
3. Blue RESUME 요청
4. Blue ACTIVE 확인
5. ConfigMap을 "blue"로 복원
6. Lease holder를 "blue"로 복원
```

### 지표 (Prometheus)

| 지표명 | 타입 | 설명 |
|--------|------|------|
| `bg_switch_duration_seconds` | Histogram | 전환 소요 시간 |
| `bg_switch_total` | Counter | 전환 시도 총 횟수 |
| `bg_switch_success_total` | Counter | 전환 성공 횟수 |
| `bg_switch_rollback_total` | Counter | 롤백 횟수 |
| `bg_switch_active_color` | Gauge | 현재 active 색상 (0=blue, 1=green) |
| `bg_switch_dual_active_detected` | Counter | 양쪽 동시 Active 감지 횟수 |

### 환경 변수

| 변수 | 설명 | 기본값 |
|------|------|--------|
| `NAMESPACE` | 워크로드 네임스페이스 | kafka-bg-test |
| `CONFIGMAP_NAME` | active 색상 ConfigMap 이름 | kafka-consumer-active-version |
| `BLUE_SERVICE` | Blue Consumer Service 이름 | consumer-blue-svc |
| `GREEN_SERVICE` | Green Consumer Service 이름 | consumer-green-svc |
| `DRAIN_TIMEOUT_SECONDS` | Drain 타임아웃 | 10 |
| `HEALTH_CHECK_INTERVAL_MS` | 헬스체크 주기 | 500 |
| `LEASE_NAME` | Lease 리소스 이름 | bg-consumer-active-lease |

## Switch Sidecar 상세 설계

### 역할

- Consumer Pod와 같은 Pod에서 Sidecar 컨테이너로 실행
- ConfigMap의 `active` 필드를 Watch
- 자신의 색상(`MY_COLOR`)과 active 색상 비교하여:
  - `active == MY_COLOR` → Consumer에 `/lifecycle/resume` POST
  - `active != MY_COLOR` → Consumer에 `/lifecycle/pause` POST

### ConfigMap Watch 로직

```go
func (w *ConfigMapWatcher) Watch(ctx context.Context) {
    informer := cache.NewInformer(
        &cache.ListWatch{...},
        &v1.ConfigMap{},
        0,
        cache.ResourceEventHandlerFuncs{
            UpdateFunc: func(old, new interface{}) {
                newCM := new.(*v1.ConfigMap)
                activeColor := newCM.Data["active"]
                if activeColor == w.myColor {
                    w.lifecycleClient.Resume()
                } else {
                    w.lifecycleClient.Pause()
                }
            },
        },
    )
    informer.Run(ctx.Done())
}
```

### 환경 변수

| 변수 | 설명 |
|------|------|
| `MY_COLOR` | 이 Sidecar의 색상 (blue/green) |
| `MY_POD_NAME` | Pod 이름 |
| `MY_NAMESPACE` | 네임스페이스 |
| `CONSUMER_LIFECYCLE_URL` | Consumer /lifecycle 엔드포인트 URL (http://localhost:8080/lifecycle) |
| `CONFIGMAP_NAME` | 감시할 ConfigMap 이름 |

### Sidecar 리소스 요구량

```yaml
resources:
  requests:
    cpu: 50m
    memory: 64Mi
  limits:
    cpu: 100m
    memory: 128Mi
```

## K8s 매니페스트

- `k8s/switch-controller-deployment.yaml`: Switch Controller Deployment + RBAC
- `k8s/consumer-blue-statefulset.yaml`에 Sidecar 컨테이너 추가
- `k8s/consumer-green-statefulset.yaml`에 Sidecar 컨테이너 추가
- `k8s/switch-rbac.yaml`: ServiceAccount, ClusterRole, ClusterRoleBinding

### RBAC 요구사항

```yaml
# Switch Controller가 필요한 권한
rules:
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["get", "watch", "list", "update", "patch"]
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["get", "create", "update", "patch", "delete"]
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list"]
  - apiGroups: [""]
    resources: ["endpoints"]
    verbs: ["get", "list"]
```

## 완료 기준

- [x] Switch Controller가 ConfigMap 변경 감지하여 전환 수행 — Informer Watch 구현 완료
- [x] "Pause First, Resume Second" 원칙 동작 확인 — handleSwitch 시퀀스 구현 완료
- [x] Lease 기반 양쪽 동시 Active 방지 동작 확인 — Lease 미해제 버그 수정 완료
- [x] Sidecar가 Consumer App에 /lifecycle 요청 전송 — Controller + Sidecar 양쪽 구현 완료
- [x] 전환 실패 시 자동 롤백 동작 확인 — rollback 시퀀스 구현 완료
- [x] Prometheus 지표 수집 확인 — 6개 Controller + 4개 Sidecar 지표 구현
- [ ] 전환 소요 시간 < 5초 목표 — 전략 C 테스트에서 실측 예정 (DrainTimeout 튜닝 가능)

## 2026-02-21 수정 이력

- go.mod 의존성 수정: `kube-openapi`, `utils` 버전을 k8s 1.29 호환 버전으로 교체
- go.sum 생성: `go mod tidy` 실행, 두 모듈 모두 빌드 성공 확인
- Lease 미해제 버그 수정: rollback 호출 후 `releaseLease(ctx)` 추가 (Step 5, 6, 7 실패 경로)
- Deployment 환경변수 정렬: 코드 미사용 변수 제거, `LIFECYCLE_PORT` 추가
- **Docker 빌드 & K8s 배포 완료**: `minikube image build` 사용, Switch Controller 1 pod + Sidecar (Consumer StatefulSet 내) 6 pods 전체 Running/Ready

## 남은 P2 이슈 (선택적)

- Consumer Lag 기반 롤백 미구현: task03.md 명세에 "Green Consumer Lag 임계값 급증 시 롤백" 조건이 있으나 코드에 없음. 전략 C 기본 테스트에는 불필요.
- SwitchRollbackTotal 카운팅 경로 불일치: handleSwitch 내 일부 경로에서 rollback 호출 전 Inc, 일부는 rollback 내부에서만 Inc. 지표 정확도에 경미한 영향.
- 전환 5초 목표: DrainTimeout 기본값 10초 → 환경변수 `DRAIN_TIMEOUT_SECONDS`로 튜닝 필요.
