# Task 03: Approach B 테스트 — Controller + Rollouts 공존

> **의존:** Task 01 (Foundation)
> **차단:** Task 05 (비교 분석)

---

## 목표

Phase 1에서 검증된 Switch Controller의 4-layer 안전망 아키텍처를 유지하면서, Argo Rollouts를 배포/분석/롤백 자동화 도구로 추가한다. 가장 높은 안정성을 기대하는 접근법.

---

## 아키텍처

```
Argo Rollouts (Rollout CR)
  ├── 새 버전 배포 → Preview ReplicaSet 생성
  ├── prePromotionAnalysis
  │   └── Webhook → kafka-consumer-active-version ConfigMap 업데이트
  └── postPromotionAnalysis
      └── Prometheus → Consumer Lag, Error Rate 확인

Switch Controller (기존 아키텍처, Deployment로 전환)
  ├── ConfigMap Watch (kafka-consumer-active-version)
  ├── Step 0: kafka-consumer-state ConfigMap 선기록
  ├── L1: Consumer HTTP 직접 호출 (stop/start)
  ├── L2: Sidecar HTTP push (/desired-state)
  └── K8s Lease 기반 상호 배제

Switch Sidecar (Consumer Pod 내 sidecar container)
  ├── L2: HTTP endpoint (/desired-state) — Controller push 수신
  ├── L3: Reconcile Loop (5초 주기) — ConfigMap vs actual 비교
  └── L4: Volume Mount File Polling — 최후 fallback (60-90초)

Consumer Pods (Deployment via Rollout)
  ├── REST API: /lifecycle/start, /lifecycle/stop, /lifecycle/pause, /lifecycle/resume
  └── 기본 시작 상태: STOPPED
```

**특징:**
- Phase 1의 4-layer 안전망 유지 (L1→L2→L3→L4)
- Argo Rollouts는 배포 + 분석 + 롤백만 담당
- ConfigMap이 Argo ↔ Controller 사이의 인터페이스
- **가장 높은 안정성, 가장 높은 복잡도**

---

## Controller/Sidecar 수정 사항

### Controller 변경

**변경 전 (Phase 1):** StatefulSet Pod 이름으로 직접 호출
```
consumer-blue-0, consumer-blue-1, consumer-blue-2
```

**변경 후 (Phase 2):** Label selector로 Pod 발견
```go
// Pod 발견: label selector
pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
    LabelSelector: "app=consumer,rollouts-pod-template-hash=<active-hash>",
})
```

**추가 변경:**
- Controller 자체를 Deployment로 배포 (기존과 동일)
- `kafka-consumer-state` ConfigMap 키: hostname → label 기반 (decisions.md D6)
  - 변경 전: `consumer-blue-0: ACTIVE`
  - 변경 후: `blue: ACTIVE`, `green: STOPPED`
- Lifecycle API 호출: `/lifecycle/pause|resume` → `/lifecycle/stop|start` (단일 그룹)

### Sidecar 변경

- ConfigMap 키 읽기: hostname → 환경변수 `BG_UNIT` 기반
- Reconciler: `desiredState`를 ConfigMap의 `${BG_UNIT}` 키에서 읽음
- Volume Mount 경로: `/etc/consumer-state/${BG_UNIT}` 또는 `/etc/consumer-state/state`

---

## B-1: 단일 그룹 (pause/resume) 테스트

### 전환 시퀀스 상세

```
[T0] kubectl argo rollouts set image consumer consumer=bg-test-consumer:v2
  │
  ├── Argo: Preview ReplicaSet 생성 (Green Pods + Sidecar)
  ├── Green Pods: STOPPED 상태로 시작
  ├── Green Sidecar: L3 Reconcile 시작 (ConfigMap 모니터링)
  │
[T1] prePromotionAnalysis 시작
  │   └── Webhook: ConfigMap(kafka-consumer-active-version) 업데이트
  │       active: blue → green
  │
[T2] Controller가 ConfigMap 변경 감지
  │   ├── Step 0: kafka-consumer-state ConfigMap 선기록
  │   │   blue: STOPPED, green: ACTIVE
  │   ├── L1: Blue Pods에 POST /lifecycle/stop
  │   │   → Blue Consumer 그룹 탈퇴
  │   ├── L1: Green Pods에 POST /lifecycle/start
  │   │   → Green Consumer 그룹 가입 → 파티션 할당
  │   └── L2: Sidecar에 HTTP push (desired state)
  │
  │   (L1 실패 시)
  │   ├── L3: Sidecar Reconciler (5초 주기) — ConfigMap과 actual 비교 → 전환
  │   └── L4: Volume Mount 파일 갱신 (60-90초) → Sidecar 파일 읽기 → 전환
  │
[T3] postPromotionAnalysis
  │   └── Prometheus: Lag < 50, Error Rate < 1% (60초)
  │
[T4] promote → Blue RS scale down
```

### 시나리오별 테스트

#### S1: 정상 Blue→Green 전환

| 단계 | 행위 | 검증 |
|------|------|------|
| 준비 | Blue ACTIVE, Producer TPS 100 | Lag ≈ 0 |
| 전환 | `kubectl argo rollouts set image consumer consumer=bg-test-consumer:v2` | |
| 자동 실행 | prePromotion → ConfigMap 변경 → Controller 전환 | Controller 로그 확인 |
| 검증 | L1 성공, Green ACTIVE | 전환 시간 측정 |
| 프로모션 | `kubectl argo rollouts promote consumer` | Blue scale down |
| 최종 검증 | postPromotionAnalysis 통과, Validator 시퀀스 검증 | |

#### S2: 즉시 롤백

| 단계 | 행위 | 검증 |
|------|------|------|
| 준비 | S1 전환 완료 | |
| 롤백 | `kubectl argo rollouts abort consumer` | Rollout Degraded |
| 자동 실행 | ConfigMap 복원 (Controller 또는 수동) → Controller가 Blue start | |
| 검증 | Blue ACTIVE 복구 시간, 메시지 유실/중복 | |

#### S3: Consumer Lag 발생 중 전환

Phase 1과 동일 패턴. Blue에 processing-delay 주입 후 전환.

#### S4: Pod 장애 중 전환 (4-layer 안전망 검증)

| 단계 | 행위 | 검증 |
|------|------|------|
| 준비 | 전환 진행 중 (Controller가 L1 호출 시도 중) | |
| 장애 주입 1 | Green Pod 1개 강제 종료 | Sidecar 연결 실패 |
| 검증 | L1 실패 → L2 fallback → L3 Reconcile Loop 동작 | Sidecar 로그 |
| 장애 주입 2 | Controller Pod 강제 종료 | Lease 해제 |
| 검증 | L3/L4 Sidecar가 독립적으로 전환 완료 | 전환 완료 시간 |
| 추가 확인 | 재생성된 Pod의 STOPPED 기본 상태, Dual-Active 0회 | |

> **핵심:** Approach B의 4-layer 안전망이 Controller 장애에서도 전환을 완료할 수 있는지 검증.

#### S5: AnalysisRun 실패 → 자동 롤백

| 단계 | 행위 | 검증 |
|------|------|------|
| 준비 | Green에 error-rate 50% 장애 주입 | |
| 전환 | image 업데이트 → prePromotion → ConfigMap 변경 | |
| Controller | Controller가 전환 수행 → Green ACTIVE (에러 발생) | |
| 분석 | postPromotionAnalysis에서 Error Rate > 1% 감지 | AnalysisRun Failure |
| 자동 롤백 | Argo Rollouts 자동 롤백 → ConfigMap 복원 필요 | |
| Controller 롤백 | Controller가 복원된 ConfigMap 감지 → Blue start | |
| 검증 | Blue 소비 재개 시간, 자동 롤백 전체 시간 | |

---

## B-2: 개별 그룹 (scale 0/N) 테스트

### 구성

- Rollout: 2개 (`consumer-blue`, `consumer-green`) + Sidecar
- Controller: 두 Rollout의 scale 관리
- group.id: `bg-test-group-blue`, `bg-test-group-green`

### 전환 시퀀스

```
[T0] ConfigMap(kafka-consumer-active-version) 업데이트: active=green
  │
[T1] Controller가 감지
  │   ├── 오프셋 동기화 (kafka-consumer-groups.sh 실행)
  │   ├── Green Rollout scale up: 0 → 3
  │   └── Green Consumer 소비 시작
  │
[T2] Lag 수렴 확인 후
  │   └── Blue Rollout scale down: 3 → 0
  │
[T3] 전환 완료
```

### S1~S5: 단일 그룹 시나리오와 동일 구조

개별 그룹 메커니즘(offset 동기화 + scale 0/N)에 맞게 적용.

---

## 측정 항목

Phase 1 측정 항목에 추가:

| 항목 | 수집 방법 | 의미 |
|------|----------|------|
| L1 성공률 | Controller 로그 | 직접 호출 성공/실패 비율 |
| L2→L3 fallback 발생 횟수 | Sidecar 메트릭 | 안전망 동작 빈도 |
| L3→L4 fallback 발생 횟수 | Sidecar 메트릭 | 최후 fallback 동작 빈도 |
| Controller → Argo 연동 지연 | 타임스탬프 비교 | ConfigMap 변경 → Controller 감지 시간 |

---

## 완료 조건

- [ ] Controller/Sidecar Deployment 전환 완료 (label selector 기반 Pod 발견)
- [ ] ConfigMap 키 재설계 적용 (hostname → label 기반)
- [ ] B-1 (단일 그룹): S1~S5 전체 5개 시나리오 실행 완료
- [ ] B-2 (개별 그룹): S1~S5 전체 5개 시나리오 실행 완료
- [ ] 4-layer 안전망 동작 검증 (L1 실패 시 L2/L3/L4 fallback 확인)
- [ ] 각 시나리오별 측정 데이터 수집
