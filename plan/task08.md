# Task 08: 테스트 리포트 작성

> **의존:** task05, task06, task07 (모든 테스트 완료 후)
> **산출물:** `report/test-report.md`, `report/screenshots/`

---

## 목표

모든 전략(B, C, E)의 테스트 결과를 종합하여 `report/test-report.md`에 리포트를 작성한다.

## 리포트 구조

### 1. 요약 (Executive Summary)

- 테스트 목적 및 범위
- 전략별 검증 결과 요약 (통과/실패/조건부 통과)
- 최종 권장 전략

### 2. 환경 정보

| 항목 | 값 |
|------|-----|
| Kubernetes 버전 | v1.23.8 |
| 노드 수 / CPU / Memory | 1노드 / ... / ... |
| Kafka 버전 | 3.8.0 (Strimzi 0.43.0) |
| Spring Boot | 2.7.18 |
| Argo Rollouts | v1.6.6 |
| 테스트 토픽 파티션 수 | 8 |
| Consumer 레플리카 수 | 전략별 기록 |
| Producer TPS | 100 |

### 3. 시나리오별 결과

각 시나리오에 대해:
- 시나리오 목적 및 수행 조건
- 측정된 지표 (전환 시간, 롤백 시간, 유실/중복 수, Lag 변화)
- Grafana 스크린샷 첨부 (`report/screenshots/`)
- 검증 기준 통과 여부 및 이유

### 4. 전략별 비교표

| 항목 | 전략 B | 전략 C | 전략 E |
|------|--------|--------|--------|
| 실측 전환 시간 | | | |
| 실측 롤백 시간 | | | |
| 메시지 유실 건수 | | | |
| 메시지 중복 건수 | | | |
| Rebalance 발생 횟수 | | | |
| 구현 복잡도 (상/중/하) | | | |
| 운영 복잡도 (상/중/하) | | | |

### 5. 발견된 문제 및 해결 방안

- 테스트 중 발견된 예상치 못한 문제 목록
- 각 문제에 대한 원인 분석 및 해결 방법 또는 미해결 사항

### 6. 결론 및 권장사항

- 설계 문서의 예상 결과와 실제 테스트 결과 비교
- 각 전략의 적합한 도입 시나리오 (운영 상황별 가이드)
- 프로덕션 도입 시 추가 고려사항

## 스크린샷 수집 목록

| 파일명 | 내용 |
|--------|------|
| `c-scenario1-lag.png` | 전략 C 시나리오 1 - Consumer Lag 시계열 |
| `c-scenario1-lifecycle.png` | 전략 C 시나리오 1 - Lifecycle 상태 변경 |
| `c-scenario1-tps.png` | 전략 C 시나리오 1 - Messages/sec |
| `c-scenario2-rollback.png` | 전략 C 시나리오 2 - 롤백 시 Lag 변화 |
| `c-scenario4-rebalance.png` | 전략 C 시나리오 4 - Rebalance 이벤트 |
| `b-scenario1-lag.png` | 전략 B 시나리오 1 - Consumer Lag |
| `e-scenario1-lag.png` | 전략 E 시나리오 1 - Consumer Lag |
| `comparison-overview.png` | 전략별 비교 대시보드 |

## 완료 기준

- [ ] 모든 전략의 시나리오 결과가 포함됨
- [ ] 전략별 비교표 작성 완료
- [ ] Grafana 스크린샷 첨부
- [ ] 설계 문서의 예상과 실제 결과 비교 분석
- [ ] 최종 권장 전략 결론 도출
