# Kafka Consumer Blue/Green 배포 전략 검증 계획

> **작성일:** 2026-02-20
> **기반 문서:** kafka-consumer-bluegreen-design.md
> **검증 환경:** Kubernetes v1.23.8 (단일 노드, Ubuntu 22.04.5 LTS, containerd 1.7.27)

---

## 1. 검증 목적

`kafka-consumer-bluegreen-design.md`에서 제시한 Kafka Consumer Blue/Green 배포 전략 중 **전략 B, C, E**를 실제 구현하고, 지표/로그를 수집하여 전환 시간, 롤백 시간, 메시지 유실/중복, 양쪽 동시 Active 발생 여부를 측정한다.

## 2. 검증 대상 전략

| 전략 | 설명 | 우선순위 |
|------|------|----------|
| **전략 C** | Pause/Resume Atomic Switch (같은 Consumer Group + Static Membership) | **1순위** (주 권장 전략) |
| **전략 B** | 별도 Consumer Group + Offset 동기화 | 2순위 |
| **전략 E** | Kafka Connect REST API / Strimzi CRD 기반 | 3순위 |

## 3. 검증 목표 지표

| 목표 항목 | 기준값 |
|-----------|--------|
| 전환 소요 시간 | < 30초 전체 / 전략 C는 < 5초 목표 |
| 롤백 소요 시간 | < 60초 전체 / 전략 C는 < 5초 목표 |
| 전환 중 메시지 유실 | 0건 |
| 전환 중 메시지 중복 | 전략별 측정 후 비교 |
| 전환 중 양쪽 동시 Active | 0회 |

## 4. 태스크 목록 및 의존 관계

```
task01: K8s 인프라 셋업 (모니터링 + Kafka + Argo Rollouts + Strimzi + KEDA)
  │
  ├── task02: Producer/Consumer Java Spring Boot 앱 구현
  │     │
  │     ├── task03: Switch Controller (Go) + Sidecar 구현
  │     │     │
  │     │     └── task05: 전략 C 테스트 수행 (1순위)
  │     │           │
  │     │           └── task05-post: 전략 C 아키텍처 개선 (4-레이어 안전망)
  │     │
  │     └── task06: 전략 B 테스트 수행
  │
  ├── task04: Validator 스크립트 구현 (메시지 유실/중복 자동 검증)
  │
  └── task07: 전략 E 테스트 수행 (Strimzi KafkaConnector 기반)
        │
        └── task08: 테스트 리포트 작성
```

### 태스크 상세 파일

| 파일 | 내용 | 예상 소요 | 상태 |
|------|------|-----------|------|
| [task01.md](task01.md) | K8s 클러스터 인프라 셋업 | 1~2일 | **완료** (코드 생성 + 설치) |
| [task02.md](task02.md) | Producer/Consumer Java 앱 구현 | 2~3일 | **완료** (코드 + 빌드 + 배포) |
| [task03.md](task03.md) | Switch Controller/Sidecar Go 구현 | 2~3일 | **완료** (코드 + 빌드 + 배포) |
| [task04.md](task04.md) | Validator 스크립트 구현 | 0.5~1일 | **코드 생성 완료** |
| [task05.md](task05.md) | 전략 C 테스트 수행 | 1~2일 | **완료** (5개 시나리오 수행, 2개 미통과) |
| [task05-post-improvement.md](task05-post-improvement.md) | 전략 C 아키텍처 개선 (P0/P1 해결 + 4-레이어 안전망) | 3~5일 | **완료** (4-레이어 안전망 구현, 7개 시나리오 통과) |
| [task06.md](task06.md) | 전략 B 테스트 수행 | 1일 | **코드 생성 완료** (매니페스트 + 헬퍼 + 튜토리얼) |
| [task07.md](task07.md) | 전략 E 테스트 수행 | 1일 | **코드 생성 완료** (매니페스트 + 헬퍼 + 튜토리얼) |
| [task08.md](task08.md) | 테스트 리포트 작성 | 1일 | **완료** (전략 C 실측 + 전략 B/E 설계 기반 예상) |

## 5. 테스트 시나리오 요약

모든 전략에 공통 적용하며, 전략 C를 우선 검증한다.

| # | 시나리오 | 핵심 검증 항목 |
|---|----------|----------------|
| 1 | 정상 Blue → Green 전환 | 전환 시간, 메시지 유실/중복, Lag 회복 |
| 2 | 전환 직후 즉시 롤백 | 롤백 시간, Blue 재개 후 Lag 안정성 |
| 3 | Consumer Lag 발생 중 전환 | Lag 소진 정책별 동작 차이 |
| 4 | 전환 중 Rebalance 장애 주입 (전략 C 전용) | Pause 상태 유지, Static Membership 동작 |
| 5 | 전환 실패 후 자동 롤백 | 자동 롤백 수행 여부, Blue 복구 |

## 6. 환경 정보

| 항목 | 값 |
|------|-----|
| K8s 버전 | v1.23.8 |
| 노드 | 1개 (kafka-bg-test) |
| Container Runtime | containerd 1.7.27 |
| OS | Ubuntu 22.04.5 LTS |
| Helm | v3.12.1 |

컴포넌트 버전 선택 근거는 [decisions.md](decisions.md) 참조.

## 7. 산출물

| 산출물 | 경로 |
|--------|------|
| 계획 문서 | `plan/` |
| 튜토리얼 | `tutorial/` |
| Producer/Consumer 앱 | `apps/consumer/`, `apps/producer/` |
| Switch Controller | `apps/switch-controller/` |
| Switch Sidecar | `apps/switch-sidecar/` |
| K8s 매니페스트 | `k8s/` |
| Helm values 파일 | `k8s/helm-values/` |
| Validator 스크립트 | `tools/validator/` |
| Grafana 대시보드 JSON | `k8s/grafana-dashboards/` |
| 테스트 리포트 | `report/test-report.md` |
| 스크린샷 | `report/screenshots/` |

## 8. 관련 문서

| 문서 | 설명 |
|------|------|
| [decisions.md](decisions.md) | 컴포넌트/라이브러리 버전 선택 근거 |
| [changes.md](changes.md) | 계획 변경 이력 |
| [improvment/after-task05.md](improvment/after-task05.md) | Task 05 이후 아키텍처 개선안 (ConfigMap state + Sidecar Reconciler) |
| [improvment/after-task05-indepth.md](improvment/after-task05-indepth.md) | ConfigMap 전파 지연 연구 + 워크로드 타입별 호환성 분석 |
| [task05-post-improvement.md](task05-post-improvement.md) | 개선 작업 실행 계획 (5-Phase) |
| `../kickoff-prompt.md` | 원본 요구사항 |
| `../kafka-consumer-bluegreen-design.md` | 설계 문서 |
