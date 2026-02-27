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

### 3.1 StatefulSet vs Argo Rollouts

| 항목 | Phase 1 (StatefulSet) | Phase 2 (Argo Rollouts) |
|------|----------------------|------------------------|
| 전환 시간 | 1.03~1.19초 | (측정) |
| Static Membership | 활용 (리밸런싱 최소화) | 미활용 (리밸런싱 빈도 증가) |
| 배포 자동화 | 수동 (ConfigMap 변경) | AnalysisTemplate 기반 자동화 |
| 자동 롤백 | 미구현 | Argo Rollouts 내장 |
| 메트릭 기반 분석 | Prometheus (수동 확인) | AnalysisTemplate (자동 판단) |
| 운영 복잡도 | Controller/Sidecar 관리 필요 | Rollout CR 관리 |

### 3.2 리밸런싱 영향 분석

Phase 1에서는 Static Membership 덕분에 Pod 재시작 시 리밸런싱이 방지되었다. Phase 2에서는:
- 매 전환 시 리밸런싱 발생 (Blue stop/Green start)
- CooperativeStickyAssignor의 2-라운드 리밸런싱으로 영향 최소화
- 리밸런싱 소요 시간이 전환 시간에 추가
- **예상:** 전환 시간 Phase 1 대비 2~5초 증가

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
| Static Membership 제거 영향 | Phase 1 vs Phase 2 리밸런싱 빈도 비교 |
| CooperativeStickyAssignor 효과 | 리밸런싱 시 파티션 이동 수 측정 |
| 처리 공백 원인 분석 | stop→start 간 시간 vs 리밸런싱 시간 분리 측정 |

---

## 완료 조건

- [ ] 6가지 조합 테스트 데이터 수집 완료
- [ ] 정량 지표 비교 매트릭스 완성
- [ ] Phase 1 대비 분석 완성
- [ ] 사용 사례별 권장 전략 도출
- [ ] report/phase2-test-report.md 작성 완료
- [ ] Prometheus 스크린샷 및 Validator 보고서 첨부
