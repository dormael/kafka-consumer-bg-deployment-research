# Task 05: 비교 분석 및 최종 보고서

> **의존:** Task 02 (Approach A), Task 03 (Approach B), Task 04 (Approach C)
> **차단:** 없음 (최종 산출물)

---

## 목표

6가지 조합 (3 접근법 × 2 그룹 전략)의 테스트 결과를 종합 비교 분석하고, Phase 1 결과와 대비하여 Argo Rollouts 기반 Kafka Consumer Blue-Green 배포의 최적 전략을 도출한다.

---

## 1. 결과 비교 매트릭스

### 1.1 정량 지표 비교

| 지표 | A-1 | A-2 | B-1 | B-2 | C-1 | C-2 | Phase 1 (C) |
|------|-----|-----|-----|-----|-----|-----|-------------|
| 전환 시간 (S1) | | | | | | | 1.03~1.19초 |
| 롤백 시간 (S2) | | | | | | | 1.03~1.19초 |
| 메시지 유실 (S1) | | | | | | | 0건 |
| 메시지 중복 (S1) | | | | | | | 1건 (0.007%) |
| Dual-Active (S4) | | | | | | | 0회 |
| Lag 수렴 시간 (S1) | | | | | | | |
| 처리 공백 (S1) | | | | | | | |
| 자동 롤백 시간 (S5) | | | | | | | |

### 1.2 정성 지표 비교

| 항목 | A | B | C |
|------|---|---|---|
| 아키텍처 복잡도 | 중간 (Webhook Job) | 높음 (Controller+Sidecar+Rollouts) | 낮음 (curl Job) |
| 컴포넌트 수 | 3 (Consumer, Rollout, Webhook Job) | 5 (Consumer, Sidecar, Controller, Rollout, ConfigMap) | 2 (Consumer, Rollout) |
| 안전망 수준 | 없음 (단일 실패 지점) | 4-layer (L1→L4) | 없음 (단일 실패 지점) |
| 자동화 수준 | 높음 (Argo 완전 관리) | 중간 (Argo+Controller 하이브리드) | 높음 (Argo 관리) |
| 운영 복잡도 | 중간 | 높음 | 낮음 |
| 디버깅 용이성 | 중간 (Job 로그) | 높음 (Controller/Sidecar 로그) | 낮음 (스크립트 로그) |
| 확장성 | 높음 | 중간 (Controller 병목 가능) | 높음 |

---

## 2. 그룹 전략 비교

| 항목 | 단일 그룹 (pause/resume) | 개별 그룹 (scale 0/N) |
|------|------------------------|---------------------|
| 전환 시간 | (측정) | (측정) |
| 리밸런싱 | 발생 (Blue stop → Green start) | 발생 (각 그룹 내) |
| 오프셋 관리 | 자동 (같은 그룹) | 수동 동기화 필요 |
| Dual-Active 위험 | 전환 순서에 의존 | 구조적으로 분리 |
| 파티션 소유권 | 단일 그룹 내 이전 | 그룹 간 독립 |
| 구현 복잡도 | stop/start 순서 관리 | offset 동기화 + scale 관리 |
| 롤백 복잡도 | Green stop → Blue start | Green scale 0 → Blue scale N + offset 동기화 |

---

## 3. Phase 1 vs Phase 2 대비 분석

### 3.1 StatefulSet vs Argo Rollouts + KIP-848

| 항목 | Phase 1 (StatefulSet + Classic) | Phase 2 (Argo Rollouts + KIP-848) |
|------|----------------------|------------------------|
| 전환 시간 | 1.03~1.19초 | (측정) |
| Kafka 버전 | 3.8.0 | **4.1.1** |
| Consumer Group Protocol | Classic (JoinGroup/SyncGroup) | **KIP-848 (ConsumerGroupHeartbeat)** |
| Static Membership | 활용 (리밸런싱 회피) | **불필요** (KIP-848 점진적 리밸런싱) |
| 리밸런싱 방식 | Stop-the-World (Static으로 회피) | **점진적** (~5초) |
| 배포 자동화 | 수동 (ConfigMap 변경) | AnalysisTemplate 기반 자동화 |
| 자동 롤백 | 미구현 | Argo Rollouts 내장 |
| 메트릭 기반 분석 | Prometheus (수동 확인) | AnalysisTemplate (자동 판단) |
| Spring Boot | 2.7.18 | **3.4.x** |
| 운영 복잡도 | Controller/Sidecar 관리 필요 | Rollout CR 관리 |

### 3.2 리밸런싱 영향 분석

Phase 1에서는 Static Membership 덕분에 Pod 재시작 시 리밸런싱이 방지되었다. Phase 2에서는 KIP-848이 리밸런싱의 성격을 근본적으로 변경한다:

| 측면 | Phase 1 (Classic + Static) | Phase 2 (KIP-848) |
|------|---------------------------|-------------------|
| 리밸런싱 방식 | Stop-the-World (Static으로 회피) | 점진적 (영향받는 파티션만) |
| 리밸런싱 시간 | ~0초 (Static) / ~103초 (없이) | **~5초** |
| 비영향 Consumer | 전체 멈춤 | **계속 소비** |
| 할당 로직 | Client Leader | **Server Coordinator** |

- **예상:** KIP-848 리밸런싱 ~5초 + 전환 오케스트레이션 시간 → 전환 시간 Phase 1 대비 3~7초 증가
- **비교 데이터:** A-1, C-1에서 Classic Protocol 비교 측정 수행

### 3.3 KIP-848 vs Classic Protocol 비교 (신규)

| 항목 | Classic Protocol | KIP-848 |
|------|-----------------|---------|
| 전환 시간 (S1) | (측정) | (측정) |
| 리밸런싱 시간 | (측정) | (측정) |
| Stop-the-World 발생 | 예/아니오 | 아니오 |
| 비영향 파티션 처리 연속성 | (측정) | (측정) |
| 처리 공백 | (측정) | (측정) |

---

## 4. 권장 전략 도출

### 4.1 사용 사례별 권장

| 사용 사례 | 권장 접근법 | 근거 |
|----------|------------|------|
| 소규모 팀, 빠른 시작 | C (Webhook + API) | 최소 복잡도, 추가 컴포넌트 없음 |
| 프로덕션 미션 크리티컬 | B (Controller + Rollouts) | 4-layer 안전망, 최고 안정성 |
| 중간 규모, 자동화 중심 | A (Rollouts Native) | 선언적 관리, 구조화된 오케스트레이션 |
| 마이크로서비스 + Kafka | A + 개별 그룹 | 서비스 독립성, Argo 기반 자동화 |
| 단일 팀, 커스텀 Consumer | A/C + 단일 그룹 | 오프셋 동기화 불필요, 단순 |

### 4.2 결론 (테스트 후 작성)

(테스트 결과를 반영하여 최종 결론 작성)

---

## 5. 보고서 구조

```
report/phase2-test-report.md
├── 1. 개요 (Executive Summary)
├── 2. 환경 정보
├── 3. 테스트 매트릭스 (6가지 조합)
├── 4. 시나리오별 결과
│   ├── 4.1 Approach A (A-1, A-2)
│   ├── 4.2 Approach B (B-1, B-2)
│   └── 4.3 Approach C (C-1, C-2)
├── 5. 비교 분석
│   ├── 5.1 접근법 비교
│   ├── 5.2 그룹 전략 비교
│   └── 5.3 Phase 1 대비 분석
├── 6. 발견된 이슈
├── 7. 권장 전략
├── 8. 한계 및 향후 과제
└── 부록
    ├── A. Prometheus 쿼리 목록
    ├── B. Validator 보고서 원본
    └── C. Argo Rollouts AnalysisRun 기록
```

---

## 6. 추가 분석 항목

### 6.1 Argo Rollouts 특화 분석

| 분석 항목 | 방법 |
|----------|------|
| AnalysisRun 실행 시간 분포 | 모든 AnalysisRun 리소스의 duration 수집 |
| 자동 롤백 신뢰성 | S5 시나리오의 성공/실패 비율 |
| prePromotion vs postPromotion 역할 | 각 단계에서 감지한 이슈 분류 |
| Rollout 상태 전이 시간 | Healthy → Progressing → Paused → Healthy 각 단계 시간 |

### 6.2 Kafka Consumer 특화 분석

| 분석 항목 | 방법 |
|----------|------|
| 리밸런싱 횟수 및 소요 시간 | Consumer 로그에서 rebalance 이벤트 추출 |
| **KIP-848 vs Classic Protocol 리밸런싱 비교** | **A-1, C-1의 S1/S2에서 두 프로토콜 비교 측정** |
| **KIP-848 점진적 리밸런싱 효과** | **비영향 Consumer의 소비 연속성 측정** |
| **서버 사이드 할당 동작** | **ConsumerGroupHeartbeat 로그 분석** |
| Static Membership 제거 영향 | Phase 1 vs Phase 2 리밸런싱 빈도 비교 |
| 처리 공백 원인 분석 | stop→start 간 시간 vs 리밸런싱 시간 분리 측정 |

### 6.3 KIP-848 특화 분석 (신규)

| 분석 항목 | 방법 |
|----------|------|
| ConsumerGroupHeartbeat 주기 | Consumer 로그에서 heartbeat 이벤트 추출 |
| Per-Member Reconciliation 시간 | revoke → assign 사이 시간 측정 |
| 서버 사이드 할당 Delta 크기 | 리밸런싱 시 이동한 파티션 수 |
| KIP-848 활성화 영향 | Classic Protocol 대비 전체 전환 성능 비교 |
| Consumer Epoch 전이 | Coordinator 할당 변경 횟수 및 시간 |

---

## 완료 조건

- [ ] 6가지 조합 테스트 데이터 수집 완료
- [ ] 정량 지표 비교 매트릭스 완성
- [ ] Phase 1 대비 분석 완성
- [ ] 사용 사례별 권장 전략 도출
- [ ] report/phase2-test-report.md 작성 완료
- [ ] Prometheus 스크린샷 및 Validator 보고서 첨부
