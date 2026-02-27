# Phase 2 핵심 설계 결정 근거

> **작성일:** 2026-02-27
> **Phase 1 참조:** plan/test-phase01/decisions.md (컴포넌트 버전 선택 근거 — 모두 동일)

---

## 컴포넌트 버전

Phase 1과 동일. 모든 버전 선택 근거는 [test-phase01/decisions.md](test-phase01/decisions.md) 참조.

---

## Phase 2 고유 설계 결정

### D1. StatefulSet → Argo Rollouts (Deployment 기반) 전환

**결정:** Consumer 워크로드를 StatefulSet에서 Argo Rollouts Rollout CR(Deployment 기반)로 전환한다.

**근거:**
- Phase 1에서 StatefulSet의 안정적 Pod 이름은 Static Membership에만 필요
- Static Membership은 Deployment/Argo Rollouts와 호환 불가 (research-summary.md §4: "Deployment/Argo Rollouts로 전환 시 Pod 이름이 랜덤이므로 Static Membership은 호환 불가")
- Argo Rollouts는 AnalysisTemplate, 자동 롤백, prePromotionAnalysis 등 배포 자동화 기능을 제공하여 Switch Controller의 일부 역할을 대체 가능
- 프로덕션 환경에서는 Deployment가 StatefulSet보다 일반적이며, Argo Rollouts와의 조합이 더 실용적

**영향:**
- `group.instance.id: ${HOSTNAME}` 제거 → 모든 리밸런싱이 동적 멤버 방식으로 처리
- Pod 종료 시 `LeaveGroup` 전송 → 즉시 리밸런싱 발생 (Static Membership이면 생략 가능했음)
- Sidecar의 L4 (Volume Mount) ConfigMap 키를 hostname 기반에서 label 기반으로 재설계 필요

### D2. Static Membership 제거

**결정:** Consumer의 `group.instance.id` 설정을 제거한다.

**근거:**
- Deployment 기반 Pod는 이름이 랜덤 → `group.instance.id: ${HOSTNAME}` 설정이 재시작 간 동일 ID를 보장하지 못함
- 동일 ID 보장 없이 Static Membership을 사용하면 FencedInstanceIdException 등의 부작용 발생 가능
- CooperativeStickyAssignor + PauseAwareRebalanceListener 조합으로 리밸런싱 영향 최소화 가능

**완화 방안:**
- `session.timeout.ms: 45000` (KIP-735 기본값) 유지
- `heartbeat.interval.ms: 3000` 유지
- `max.poll.interval.ms: 300000` 유지
- CooperativeStickyAssignor: 리밸런싱 시 2-라운드 점진적 할당으로 처리 공백 ~3.5초 (Confluent 측정)

### D3. Consumer STOPPED 상태 추가

**결정:** 기존 ACTIVE/PAUSED/DRAINING 외에 **STOPPED** 상태를 추가한다.

**근거:**
- Phase 1 단일 그룹 전략의 구조적 제한: PAUSED Consumer도 그룹에 가입하여 파티션을 할당받으므로, 해당 파티션의 메시지 처리가 중단됨
- STOPPED 상태에서는 `MessageListenerContainer.stop()` 호출 → Consumer가 그룹에서 완전히 탈퇴
- Green Consumer를 STOPPED 상태로 시작하면 그룹에 가입하지 않으므로 불필요한 리밸런싱 방지
- 전환 시: Blue stop → Green start + resume → Green만 그룹에 존재 → 모든 파티션 Green 소유

**`pause()` vs `stop()` 차이 (research-summary.md §5 참조):**

| 동작 | pause() | stop() |
|------|---------|--------|
| poll() 호출 | 계속 (빈 결과 반환) | 중단 |
| 그룹 멤버십 | **유지** | **탈퇴** (LeaveGroup) |
| 파티션 소유 | 유지 | 반납 |
| 리밸런싱 | 방지 | **트리거** |
| 타임아웃 | 방지 (poll() 계속) | 해당 없음 |

**Consumer Lifecycle 상태 머신 (Phase 2):**
```
STOPPED (그룹 미가입, 기본 시작 상태)
  │
  ├── start + resume → ACTIVE (그룹 가입, 소비 중)
  │                      │
  │                      ├── pause → PAUSED (그룹 유지, 소비 중단)
  │                      │             │
  │                      │             └── resume → ACTIVE
  │                      │
  │                      └── stop → STOPPED
  │
  └── (Consumer HTTP: /lifecycle/start, /lifecycle/stop)
```

### D4. Argo Rollouts Blue-Green 모드 활용 방식

**결정:** Argo Rollouts의 Blue-Green 전략을 사용하되, Service 스위칭이 아닌 **prePromotionAnalysis 웹후크**를 통해 Consumer lifecycle을 제어한다.

**근거:**
- Argo Rollouts B/G는 activeService/previewService 간 selector 스위칭이 핵심
- Kafka Consumer는 Service를 통해 트래픽을 받지 않으므로 Service 스위칭만으로는 전환 불가
- prePromotionAnalysis 내 웹후크가 Consumer API를 호출하여 실제 전환 수행
- Service는 Pod 발견(DNS/selector) 및 모니터링 용도로 활용

**단일 그룹 전환 시퀀스:**
1. Rollout 업데이트 → 새 ReplicaSet(Green) 생성 → Green pods STOPPED 상태로 시작
2. prePromotionAnalysis:
   a. Webhook: Blue Consumer stop (그룹 탈퇴 → 리밸런싱)
   b. Webhook: Green Consumer start + resume (그룹 가입 → 모든 파티션 할당)
   c. Prometheus: Consumer Lag 수렴 확인
3. Promote: Blue ReplicaSet scale down
4. postPromotionAnalysis: Error Rate, Lag 안정성 최종 확인

**개별 그룹 전환 시퀀스:**
1. Green Rollout의 replicas: 0 → N (scale up)
2. prePromotionAnalysis:
   a. Webhook: Blue group → Green group 오프셋 동기화
   b. Green Consumer 소비 시작
   c. Prometheus: Green Consumer Lag 수렴 확인
3. Blue Rollout의 replicas: N → 0 (scale down)
4. postPromotionAnalysis: 메시지 유실/중복 확인

### D5. Webhook Job 서비스 설계

**결정:** Argo Rollouts의 AnalysisTemplate webhook에서 호출하는 **경량 Webhook Job 서비스**를 구현한다.

**근거:**
- Argo Rollouts의 webhook은 HTTP endpoint를 호출하는 방식
- Consumer Pod의 개별 IP를 알 수 없으므로, 중간 서비스가 K8s API로 Pod 목록을 조회하여 각 Pod에 명령 전달
- 이 서비스가 오케스트레이션 로직을 집중 관리 (pause 순서, 타임아웃, 재시도 등)

**구현 옵션:**
1. **Go HTTP 서비스**: K8s API로 Pod 조회 → Consumer API 호출 (Approach A, C)
2. **K8s Job + curl**: 단순 스크립트로 Consumer API 호출 (최소 접근)
3. **기존 Switch Controller 재활용**: ConfigMap 인터페이스로 연동 (Approach B)

### D6. Sidecar L4 ConfigMap 키 재설계 (Approach B 전용)

**결정:** Approach B에서 Sidecar의 Volume Mount ConfigMap 키를 hostname 기반에서 **deployment-label 기반**으로 변경한다.

**근거:**
- Phase 1: `kafka-consumer-state` ConfigMap의 키가 `consumer-blue-0: ACTIVE` 형태 (hostname 기반)
- Deployment에서는 Pod hostname이 랜덤이므로 키 매칭 불가
- Blue/Green 단위 키로 변경: `blue: ACTIVE`, `green: PAUSED`
- 각 Sidecar는 환경변수(`BG_UNIT=blue`)로 자신의 키를 식별

### D7. 파티션 수 유지

**결정:** 토픽 파티션 수 8을 유지한다.

**근거:**
- Phase 1과 동일한 파티션 수로 비교 가능성 확보
- Consumer 3 replicas × 2 (Blue + Green) = 최대 6 Consumer
- 8 파티션 > 6 Consumer → 모든 Consumer가 최소 1개 파티션 할당 가능
- 개별 그룹의 경우 각 그룹당 3 Consumer vs 8 파티션 → 충분

---

## 결정 요약표

| ID | 결정 | 영향 범위 | 접근법 |
|----|------|----------|--------|
| D1 | StatefulSet → Argo Rollouts | 전체 아키텍처 | 전체 |
| D2 | Static Membership 제거 | Consumer 설정 | 전체 |
| D3 | STOPPED 상태 추가 | Consumer 앱 코드 | 단일 그룹 |
| D4 | prePromotionAnalysis로 전환 제어 | Argo 매니페스트 | A, C |
| D5 | Webhook Job 서비스 구현 | 신규 컴포넌트 | A, C |
| D6 | ConfigMap 키 재설계 | Sidecar, Controller | B |
| D7 | 파티션 수 8 유지 | Kafka 토픽 | 전체 |
