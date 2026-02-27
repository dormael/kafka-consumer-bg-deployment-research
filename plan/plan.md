# Kafka Consumer Blue-Green 배포 Phase 2: Argo Rollouts 기반 테스트 계획

> **작성일:** 2026-02-27
> **기반 문서:** research/research-summary.md, plan/test-phase01/plan.md
> **검증 환경:** Kubernetes v1.23.8 (단일 노드, Minikube)
> **Phase 1 참조:** plan/test-phase01/ (StatefulSet 기반 3전략 검증, Strategy C 완료)

---

## 1. 검증 목적

Phase 1에서 StatefulSet 기반으로 검증한 Kafka Consumer Blue-Green 배포를 **Argo Rollouts만을 이용**하여 재검증한다. StatefulSet의 안정적인 Pod 이름에 의존하던 Static Membership을 제거하고, Argo Rollouts의 Blue-Green 전략과 AnalysisTemplate을 활용하여 더 실용적인 프로덕션 배포 패턴을 검증한다.

### Phase 1 대비 핵심 변경점

| 항목 | Phase 1 (StatefulSet) | Phase 2 (Argo Rollouts) |
|------|----------------------|------------------------|
| 워크로드 타입 | StatefulSet | Argo Rollouts (Deployment 기반) |
| Pod 이름 | 안정적 (`consumer-blue-0`) | 랜덤 (`consumer-7f8d9c-xk2pj`) |
| Static Membership | `group.instance.id: ${HOSTNAME}` | **사용 불가** (Pod 이름 불안정) |
| Rebalance 최소화 | Static Membership + CooperativeStickyAssignor | CooperativeStickyAssignor + PauseAwareRebalanceListener만 |
| 배포/롤백 자동화 | Switch Controller 수동 오케스트레이션 | Argo Rollouts AnalysisTemplate 기반 자동화 |
| 전환 제어 | ConfigMap + Controller 4-layer 안전망 | 접근법별 상이 (아래 참조) |

---

## 2. 검증 대상

### 2.1 제어 접근법 (3가지)

#### Approach A: Rollouts Native — AnalysisTemplate 완전 자동화

```
Argo Rollouts (Rollout CR)
  ├── prePromotionAnalysis
  │   ├── Webhook Job → Consumer /lifecycle/pause (Blue)
  │   ├── Webhook Job → Consumer /lifecycle/resume (Green)
  │   └── Prometheus Query → Consumer Lag 확인
  ├── promotion (자동/수동)
  └── postPromotionAnalysis
      └── Prometheus Query → Error Rate, Lag 안정성 확인
```

- **Switch Controller 없음, Sidecar 없음**
- 모든 전환 로직이 Argo 리소스(Rollout, AnalysisTemplate) 내에 선언적으로 정의
- AnalysisRun 실패 시 Argo가 자동 롤백
- **장점:** 아키텍처 최소, 선언적 관리
- **단점:** Argo에 강한 의존, 세밀한 오케스트레이션 어려움

#### Approach B: Controller + Rollouts 공존

```
Argo Rollouts (Rollout CR)
  ├── prePromotionAnalysis
  │   └── Webhook → ConfigMap 업데이트 (active-version 변경)
  └── postPromotionAnalysis
      └── Prometheus Query → 전환 검증

Switch Controller (기존 아키텍처 유지)
  ├── ConfigMap Watch → 전환 감지
  ├── L1: Consumer HTTP 직접 호출
  ├── L2: Sidecar HTTP push
  └── Lease 기반 상호 배제

Switch Sidecar (Consumer Pod 내)
  ├── L3: Reconcile Loop (5초 주기)
  └── L4: Volume Mount File Polling (fallback)
```

- **Switch Controller/Sidecar 유지** (Phase 1의 4-layer 안전망)
- Argo Rollouts는 배포 lifecycle + 분석 + 롤백만 담당
- ConfigMap이 Argo ↔ Controller 사이의 인터페이스
- **장점:** 최고 수준 안정성 (4-layer 안전망), Phase 1 검증 완료된 패턴
- **단점:** 아키텍처 복잡도 최대, 컴포넌트 간 결합

#### Approach C: Rollouts Webhook + Consumer API 직접 호출 (경량)

```
Argo Rollouts (Rollout CR)
  ├── prePromotionAnalysis
  │   ├── Webhook → Consumer /lifecycle/pause (Blue Pod들)
  │   └── Webhook → Consumer /lifecycle/resume (Green Pod들)
  └── postPromotionAnalysis
      └── Prometheus Query → Consumer Lag, Error Rate 확인
```

- **Switch Controller 없음, Sidecar 없음**
- Consumer 앱 내 기존 REST API (`/lifecycle/pause`, `/lifecycle/resume`) 활용
- Argo의 prePromotionAnalysis 웹후크가 Consumer API를 직접 호출
- Approach A와 유사하나, 별도의 Webhook Job 서비스를 구현하여 오케스트레이션 로직을 집중
- **장점:** 단순한 구조, Consumer API 재활용
- **단점:** 안전망 없음 (단일 실패 지점), Pod IP 발견 로직 필요

### 2.2 Consumer Group 전략 (2가지)

#### 전략 1: 단일 그룹 (pause/resume)

Blue와 Green이 **같은 `group.id`**를 사용한다.

- 전환 메커니즘: Blue Consumer pause → Green Consumer resume
- Green Consumer가 그룹에 가입 시 **리밸런싱 발생** (불가피)
- CooperativeStickyAssignor로 리밸런싱 영향 최소화
- **파티션 소유권 이슈:** Blue와 Green이 동시에 그룹에 존재하면, 일부 파티션이 PAUSED 상태의 Consumer에 할당될 수 있음
- **해결책:** Green Consumer를 **STOPPED 상태**(그룹 미가입)로 시작 → 전환 시 start + resume

#### 전략 2: 개별 그룹 (scale 0/N + offset 동기화)

Blue와 Green이 **다른 `group.id`**를 사용한다.

- Blue: `group.id=bg-test-group-blue`
- Green: `group.id=bg-test-group-green`
- 전환 메커니즘: Green offset 동기화 → Green scale up → Blue scale down
- 오프셋 동기화: `kafka-consumer-groups.sh --reset-offsets --to-current`
- **장점:** 파티션 소유권 이슈 없음, 리밸런싱 영향 없음 (별도 그룹)
- **단점:** 오프셋 동기화 시점의 데이터 무결성, 전환 시간 증가 (scale up/down)

### 2.3 테스트 매트릭스

| | 단일 그룹 (pause/resume) | 개별 그룹 (scale 0/N) |
|---|---|---|
| **Approach A: Rollouts Native** | A-1 | A-2 |
| **Approach B: Controller + Rollouts** | B-1 | B-2 |
| **Approach C: Webhook + API** | C-1 | C-2 |

> 총 6가지 조합, 각 조합당 5개 시나리오 = **최대 30개 테스트 케이스**
> 우선순위: A-1 → C-1 → A-2 → C-2 → B-1 → B-2

---

## 3. 검증 목표 지표

| 목표 항목 | 기준값 |
|-----------|--------|
| 전환 소요 시간 | < 30초 (단일 그룹 목표: < 10초) |
| 롤백 소요 시간 | < 30초 (단일 그룹 목표: < 10초) |
| 전환 중 메시지 유실 | 0건 |
| 전환 중 메시지 중복 | 측정 (0.1% 이하) |
| 전환 중 Dual-Active | 0회 |

> Phase 1에서 Strategy C는 1.03~1.19초를 달성했으나, Static Membership 없이는 리밸런싱 추가 시간이 발생할 수 있으므로 목표를 10초로 조정.

---

## 4. 테스트 시나리오 (공통 5개)

| # | 시나리오 | 핵심 검증 항목 |
|---|----------|----------------|
| S1 | 정상 Blue→Green 전환 | 전환 시간, 메시지 유실/중복, Lag 회복 |
| S2 | 전환 직후 즉시 롤백 | 롤백 시간, Blue 재개 후 Lag 안정성 |
| S3 | Consumer Lag 발생 중 전환 | Lag 상황에서의 전환 안정성, 데이터 무결성 |
| S4 | Pod 장애 중 전환 | Pod 재시작 시 동작 (Dual-Active 방지), Argo Rollouts의 장애 감지 |
| S5 | AnalysisRun 실패 → 자동 롤백 | Argo Rollouts의 자동 롤백 동작, Blue 복구 시간 |

### 시나리오별 상세 검증 포인트

**S1 — 정상 전환:**
- Green 배포 → prePromotionAnalysis 성공 → promote → Blue scale down
- Prometheus: Consumer Lag → 0 수렴 시간 측정
- Validator: 시퀀스 연속성 검증

**S2 — 즉시 롤백:**
- Green promote 직후 `kubectl argo rollouts abort` 또는 postPromotionAnalysis 실패 트리거
- Blue 복구 시간 측정, 오프셋 정합성 확인

**S3 — Lag 중 전환:**
- Consumer에 processing-delay 장애 주입 → Lag 축적
- Lag 상황에서 전환 실행 → Green이 밀린 메시지를 소화하는 시간 측정

**S4 — Pod 장애:**
- 전환 진행 중 Green Pod 1개 강제 종료 (`kubectl delete pod`)
- Argo Rollouts의 Pod readiness 감지 동작 확인
- PAUSED 기본 시작으로 Dual-Active 방지 확인

**S5 — 자동 롤백:**
- Green Consumer에 error-rate 장애 주입
- AnalysisRun이 Prometheus에서 에러율 감지 → 실패 판정 → 자동 롤백
- Argo Rollouts의 `rollbackWindow`, `scaleDownDelaySeconds` 동작 확인

---

## 5. Task 의존 관계

```
task01: 앱 수정 및 인프라 준비
  │     (Consumer Static Membership 제거, STOPPED 상태 추가,
  │      Argo Rollouts 매니페스트 설계, Webhook Job 구현)
  │
  ├── task02: Approach A 테스트 — Rollouts Native
  │     (단일 그룹 A-1 + 개별 그룹 A-2, 각 5 시나리오)
  │
  ├── task03: Approach B 테스트 — Controller + Rollouts
  │     (단일 그룹 B-1 + 개별 그룹 B-2, 각 5 시나리오)
  │     (Controller/Sidecar Deployment 전환 포함)
  │
  ├── task04: Approach C 테스트 — Webhook + API
  │     (단일 그룹 C-1 + 개별 그룹 C-2, 각 5 시나리오)
  │
  └── task05: 비교 분석 및 최종 보고서
        (6가지 조합 비교, Phase 1 결과 대비 분석, 권장 전략 도출)
```

### Task 상세 파일

| 파일 | 내용 | 상태 |
|------|------|------|
| [task01.md](task01.md) | 앱 수정 및 인프라 준비 (Foundation) | 미착수 |
| [task02.md](task02.md) | Approach A: Rollouts Native 테스트 | 미착수 |
| [task03.md](task03.md) | Approach B: Controller + Rollouts 공존 테스트 | 미착수 |
| [task04.md](task04.md) | Approach C: Rollouts Webhook + API 테스트 | 미착수 |
| [task05.md](task05.md) | 비교 분석 및 최종 보고서 | 미착수 |

---

## 6. Argo Rollouts Blue-Green과 Kafka Consumer의 핵심 과제

### 6.1 Service 미스매치

Argo Rollouts의 Blue-Green 전략은 **Service selector 스위칭**으로 트래픽을 전환한다. 그러나 Kafka Consumer는 Service를 통해 트래픽을 받지 않는 **Pull 모델**이다.

**해결 방향:**
- `activeService`/`previewService`를 Consumer Pod 발견 및 lifecycle 제어용으로 활용
- 실제 전환은 `prePromotionAnalysis` 웹후크를 통해 Consumer API 호출로 수행
- Argo의 Service 스위칭은 모니터링/디버깅 목적으로만 활용

### 6.2 Static Membership 불가

Deployment 기반 Rollout은 Pod 이름이 랜덤이므로 `group.instance.id: ${HOSTNAME}` 전략이 무의미하다.

**영향:**
- Pod 정상 종료 시 `LeaveGroup` 전송 → 즉시 리밸런싱 (Static Membership이면 생략)
- Pod 재시작 시 새 `member.id` → 항상 리밸런싱
- 리밸런싱 빈도 증가 → CooperativeStickyAssignor의 중요성 증대

**완화 방안:**
- `session.timeout.ms: 45000` (Kafka 3.0+ 기본값) 유지하여 일시적 네트워크 장애에 의한 리밸런싱 방지
- `CooperativeStickyAssignor`로 리밸런싱 시 파티션 이동 최소화
- `PauseAwareRebalanceListener`로 리밸런싱 후 pause 상태 재적용

### 6.3 단일 그룹 전략의 구조적 제한

Phase 1에서 확인된 문제: 같은 그룹에 Blue(3) + Green(3) = 6 Consumer vs 8 파티션이면, 리밸런싱 후 PAUSED Consumer에도 파티션이 할당되어 해당 파티션의 메시지 처리가 중단된다.

**Phase 2 해결 방안:**
- Green Consumer를 **STOPPED 상태**(그룹 미가입)로 시작
- 전환 시: Blue stop → Green start + resume (순차적)
- 이로써 Green만 그룹에 존재 → 모든 파티션을 Green이 소유
- 단, Blue stop ↔ Green start 사이에 **처리 공백** 발생 (수 초)

---

## 7. 환경 정보

| 항목 | 값 |
|------|-----|
| K8s 버전 | v1.23.8 (Minikube) |
| Container Runtime | containerd |
| Argo Rollouts | v1.6.6 (Helm Chart 2.35.3) |
| Strimzi Operator | 0.43.0 |
| Apache Kafka | 3.8.0 (KRaft) |
| kube-prometheus-stack | 51.10.0 |
| Grafana Loki | 2.10.2 |
| KEDA | 2.9.3 |
| Spring Boot | 2.7.18 |
| Spring Kafka | 2.8.11 |
| Go | 1.21+ |
| Python | 3.9+ |

버전 선택 근거: [decisions.md](decisions.md) 참조
Phase 1 버전과 동일 (K8s v1.23.8 제약 유지)

---

## 8. 산출물

| 산출물 | 경로 |
|--------|------|
| Phase 2 계획 문서 | `plan/` |
| Phase 1 계획 문서 (아카이브) | `plan/test-phase01/` |
| Argo Rollouts 매니페스트 | `k8s/rollouts/` |
| AnalysisTemplate 매니페스트 | `k8s/rollouts/analysis/` |
| Webhook Job 소스 | `apps/webhook-job/` (신규) |
| 수정된 Consumer 앱 | `apps/consumer/` |
| Validator 스크립트 (재사용) | `tools/validator/` |
| 테스트 보고서 | `report/phase2-test-report.md` |
| 테스트 튜토리얼 | `tutorial/phase2/` |

---

## 9. 관련 문서

| 문서 | 설명 |
|------|------|
| [decisions.md](decisions.md) | Phase 2 핵심 설계 결정 근거 |
| [test-phase01/plan.md](test-phase01/plan.md) | Phase 1 마스터 플랜 |
| [test-phase01/decisions.md](test-phase01/decisions.md) | Phase 1 컴포넌트 버전 선택 근거 |
| [test-phase01/task05-post-improvement.md](test-phase01/task05-post-improvement.md) | Phase 1 4-layer 안전망 설계 |
| [../research/research-summary.md](../research/research-summary.md) | 리서치 통합 요약 |
