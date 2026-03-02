# Task 02: Approach A 테스트 — Rollouts Native (AnalysisTemplate 완전 자동화)

> **의존:** Task 01 (Foundation)
> **차단:** Task 05 (비교 분석)

---

## 목표

Switch Controller/Sidecar 없이, Argo Rollouts의 AnalysisTemplate + Webhook Job만으로 Kafka Consumer Blue-Green 전환을 수행하고 검증한다.

---

## 아키텍처

```
Argo Rollouts (Rollout CR)
  ├── 새 버전 배포 → Preview ReplicaSet 생성
  ├── prePromotionAnalysis
  │   ├── Job: Webhook Job → Active Pods /lifecycle/stop
  │   ├── Job: Webhook Job → Preview Pods /lifecycle/start
  │   └── Prometheus: Consumer Lag < 100 (30초간 6회 체크)
  ├── promotion (수동: kubectl argo rollouts promote)
  └── postPromotionAnalysis
      ├── Prometheus: Error Rate < 1% (60초간 6회 체크)
      └── Prometheus: Consumer Lag < 50 (60초간 안정)

Consumer Pods (Deployment via Rollout)
  ├── REST API: /lifecycle/start, /lifecycle/stop, /lifecycle/pause, /lifecycle/resume, /lifecycle/status
  └── 기본 시작 상태: STOPPED (그룹 미가입)
```

**특징:**
- Controller 없음, Sidecar 없음
- 모든 전환 로직이 AnalysisTemplate 내 Webhook Job에 집중
- Argo Rollouts가 실패 감지 시 자동 롤백 수행
- **KIP-848 활용**: `group.protocol=consumer`로 점진적 리밸런싱 (~5초), Stop-the-World 없음

---

## A-1: 단일 그룹 (pause/resume) 테스트

### 구성

- Rollout: 1개 (`consumer`)
- group.id: `bg-test-group` (Blue/Green 공유)
- 전환 메커니즘: stop(Blue) → start(Green)

### 전환 시퀀스 상세

```
시간 ──────────────────────────────────────────────────────→

[T0] kubectl argo rollouts set image consumer consumer=bg-test-consumer:v2
  │
  ├── Argo: Preview ReplicaSet 생성 (Green Pods)
  ├── Green Pods 시작: STOPPED 상태 (그룹 미가입, 리밸런싱 없음)
  │
[T1] 모든 Green Pods Ready
  │
  ├── prePromotionAnalysis 시작
  │   ├── [Job] Active Pods(Blue)에 POST /lifecycle/stop
  │   │   → Blue Consumer 그룹 탈퇴 → LeaveGroup
  │   │   → KIP-848: Coordinator가 Blue 파티션만 재분배 대상으로 표시
  │   │   → 파티션 미할당 상태 (처리 공백 시작)
  │   │
  │   ├── [Job] Preview Pods(Green)에 POST /lifecycle/start
  │   │   → Green Consumer 그룹 가입
  │   │   → KIP-848: Coordinator가 점진적으로 파티션 할당 (~5초)
  │   │   → 소비 시작 (처리 공백 종료)
  │   │
  │   └── [Prometheus] Consumer Lag < 100 확인 (30초간 6회)
  │       → 성공 시 prePromotionAnalysis 통과
  │       → 실패 시 자동 롤백 (Green stop, Blue start)
  │
[T2] prePromotionAnalysis 성공
  │
  ├── kubectl argo rollouts promote consumer
  │   → Active Service selector → Green ReplicaSet
  │   → Blue ReplicaSet scale down 예약 (scaleDownDelaySeconds: 30)
  │
[T3] postPromotionAnalysis 시작
  │   ├── [Prometheus] Error Rate < 1% 확인 (60초)
  │   └── [Prometheus] Consumer Lag < 50 확인 (60초)
  │       → 성공 시 전환 완료
  │       → 실패 시 자동 롤백
  │
[T4] 전환 완료, Blue ReplicaSet scale down
```

### 시나리오별 테스트

#### S1: 정상 Blue→Green 전환

| 단계 | 행위 | 검증 |
|------|------|------|
| 준비 | Producer TPS 100 → Blue Consumer ACTIVE 소비 중 | Consumer Lag ≈ 0 |
| 전환 | `kubectl argo rollouts set image consumer consumer=bg-test-consumer:v2` | Preview RS 생성 확인 |
| 대기 | prePromotionAnalysis 자동 실행 | Webhook Job 성공, Lag 수렴 |
| 프로모션 | `kubectl argo rollouts promote consumer` | Service selector 전환 |
| 검증 | postPromotionAnalysis 완료 대기 | Error Rate < 1%, Lag 안정 |
| 측정 | T0~T4 전체 소요 시간, Validator로 시퀀스 검증 | 유실 0건, 중복 측정 |

#### S2: 즉시 롤백

| 단계 | 행위 | 검증 |
|------|------|------|
| 준비 | S1과 동일하게 전환 완료 | Green ACTIVE, Blue scaled down |
| 롤백 | `kubectl argo rollouts abort consumer` | Rollout 상태: Degraded |
| 검증 | Blue RS scale up, Green RS scale down | Blue ACTIVE 복구 |
| 측정 | abort → Blue 소비 재개 시간, 메시지 유실/중복 | |

#### S3: Consumer Lag 발생 중 전환

| 단계 | 행위 | 검증 |
|------|------|------|
| 준비 | Blue Consumer에 `PUT /fault/processing-delay` (500ms) | Lag 축적 확인 |
| 전환 | Lag 상태에서 image 업데이트 | prePromotionAnalysis 동작 확인 |
| 검증 | Green이 밀린 메시지 소화 | Lag 수렴 시간 측정 |
| 장애 해제 | Green에는 장애 없음 | 정상 처리 확인 |

#### S4: Pod 장애 중 전환

| 단계 | 행위 | 검증 |
|------|------|------|
| 준비 | 전환 진행 중 (prePromotionAnalysis 실행 중) | |
| 장애 주입 | Green Pod 1개 강제 종료: `kubectl delete pod <green-pod>` | |
| 검증 | Argo가 Pod 장애 감지, Rollout 상태 확인 | Degraded or 자동 롤백 |
| 추가 확인 | 재생성된 Pod의 시작 상태 (STOPPED 확인) | Dual-Active 방지 |

#### S5: AnalysisRun 실패 → 자동 롤백

| 단계 | 행위 | 검증 |
|------|------|------|
| 준비 | Green Consumer에 `PUT /fault/error-rate` (50%) 설정 | |
| 전환 | image 업데이트 → prePromotionAnalysis 시작 | |
| 검증 | Prometheus 에러율 감지 → AnalysisRun Failure | 자동 롤백 동작 |
| 롤백 확인 | Webhook Job이 Green stop, Blue start 수행 | Blue 소비 재개 |
| 측정 | 장애 감지 → 롤백 완료 시간 | |

---

## A-2: 개별 그룹 (scale 0/N) 테스트

### 구성

- Rollout: 2개 (`consumer-blue`, `consumer-green`)
- group.id: `bg-test-group-blue`, `bg-test-group-green` (별도)
- 전환 메커니즘: offset 동기화 → Green scale up → Blue scale down

### 전환 시퀀스 상세

```
[T0] 전환 시작 (수동 또는 스크립트)
  │
  ├── 오프셋 동기화:
  │   kafka-consumer-groups.sh --bootstrap-server <broker>
  │     --group bg-test-group-green --topic bg-test-topic
  │     --reset-offsets --to-current --execute
  │     (Blue 그룹의 현재 커밋 오프셋을 Green 그룹에 복사)
  │
[T1] Green Rollout scale up: replicas 0 → 3
  │   → Green Consumer Pods 시작 → ACTIVE 상태 → Green 그룹 가입
  │   → 동기화된 오프셋부터 소비 시작
  │
[T2] Green Consumer Lag 수렴 확인 (Prometheus)
  │
[T3] Blue Rollout scale down: replicas 3 → 0
  │   → Blue Consumer Pods 종료 → Blue 그룹 탈퇴
  │
[T4] 전환 완료
```

### 시나리오별 테스트

S1~S5 동일한 시나리오 구조를 적용하되, 전환 메커니즘이 scale 0/N + offset 동기화로 변경된다.

#### S1: 정상 전환 (개별 그룹)

| 단계 | 행위 | 검증 |
|------|------|------|
| 준비 | Blue ACTIVE (3 pods), Green 0 replicas | |
| 오프셋 동기화 | `kafka-consumer-groups.sh --reset-offsets --to-current` | 오프셋 값 일치 확인 |
| Green Scale Up | `kubectl argo rollouts set image consumer-green ...` + replicas 3 | Green pods ACTIVE |
| Lag 확인 | Prometheus: Green Lag 수렴 | < 100 threshold |
| Blue Scale Down | `kubectl scale rollout consumer-blue --replicas=0` | Blue pods 종료 |
| 검증 | Validator로 시퀀스 검증 | 유실 0건, 중복 측정 |

#### S2: 즉시 롤백 (개별 그룹)

| 단계 | 행위 | 검증 |
|------|------|------|
| 준비 | S1 전환 완료 상태 (Green ACTIVE, Blue 0) | |
| 롤백 | Blue replicas 0→3, Green replicas 3→0 | |
| 오프셋 | Blue 그룹 오프셋을 Green의 현재 커밋으로 동기화 | |
| 검증 | Blue 소비 재개, 메시지 유실/중복 | |

#### S3~S5: 단일 그룹 시나리오와 동일 구조

S3(Lag 중 전환), S4(Pod 장애), S5(자동 롤백)을 개별 그룹 메커니즘에 맞게 적용.

---

## 측정 항목

| 항목 | 수집 방법 | 단위 |
|------|----------|------|
| 전환 시간 (T0→T4) | `kubectl argo rollouts status` 타임스탬프 | 초 |
| 처리 공백 | Prometheus `bg_consumer_messages_received_total` 증가율 0 구간 | 초 |
| 메시지 유실 | Validator 시퀀스 분석 | 건 |
| 메시지 중복 | Validator 시퀀스 분석 | 건 (%) |
| Consumer Lag 최대치 | Prometheus max(kafka_consumergroup_lag) during switch | 건 |
| Lag 수렴 시간 | Prometheus Lag > 0 → Lag = 0 시간 | 초 |
| AnalysisRun 소요 시간 | Argo Rollouts AnalysisRun 리소스 | 초 |

---

## 완료 조건

- [ ] A-1 (단일 그룹, KIP-848): S1~S5 전체 5개 시나리오 실행 완료
- [ ] A-1 (단일 그룹, Classic Protocol 비교): S1, S2 시나리오 비교 실행
- [ ] A-2 (개별 그룹, KIP-848): S1~S5 전체 5개 시나리오 실행 완료
- [ ] 각 시나리오별 측정 데이터 수집 (Prometheus 스크린샷, Validator 보고서)
- [ ] KIP-848 vs Classic Protocol 리밸런싱 시간 비교 데이터 수집
- [ ] 발견된 이슈 기록 및 분류 (P0/P1/P2)
