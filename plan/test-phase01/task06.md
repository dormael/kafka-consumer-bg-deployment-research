# Task 06: 전략 B 테스트 수행 (별도 Consumer Group + Offset 동기화)

> **의존:** task01, task02, task04
> **우선순위:** 2순위
> **튜토리얼:** `tutorial/07-strategy-b-test.md`
> **상태:** 코드 생성 완료 (2026-02-22)

---

## 목표

전략 B(별도 Consumer Group + Offset 동기화)의 5가지 시나리오를 수행하고, 전략 C와 비교한다.

## 전략 B의 핵심 차이점 (vs 전략 C)

| 항목 | 전략 C | 전략 B |
|------|--------|--------|
| Consumer Group | 같은 group.id 공유 | **별도 group.id** (blue/green) |
| 전환 방식 | Pause/Resume | **Offset 동기화 후 Scale up/down** |
| Sidecar 필요 | 필요 (4-레이어 안전망) | **불필요** |
| K8s 워크로드 | StatefulSet (Static Membership) | **Deployment** |
| Static Membership | 사용 (`group.instance.id=${HOSTNAME}`) | **미사용** |
| Offset 동기화 | 불필요 (같은 Group) | **필수** (kafka-consumer-groups.sh) |
| 전환 시간 예상 | ~1초 | **30~60초** |
| 초기 상태 | PAUSED (Sidecar 제어) | **ACTIVE** (즉시 소비) |

## 설계 결정

| # | 결정 | 근거 |
|---|------|------|
| D1 | Deployment 사용 (StatefulSet 아님) | Static Membership 불필요, Pod 이름 안정성 불필요 |
| D2 | Sidecar 제거 | Pause/Resume이 아닌 Scale 기반 전환 → lifecycle 관리 불필요 |
| D3 | `app: bg-consumer` 레이블 유지 | Loki 쿼리 `{app="bg-consumer"}`와 호환 |
| D4 | `strategy: b` 레이블 추가 | 전략 C의 StatefulSet Pod와 selector 충돌 방지 |
| D5 | `--to-current` offset reset | Blue shutdown 후 log-end-offset으로 동기화 |
| D6 | Switch Controller 미사용 | 전략 B는 수동 kubectl + 헬퍼 스크립트로 충분 |
| D7 | `kubectl port-forward` 기반 fault injection | Consumer 이미지에 curl 미포함, sidecar 없음 |
| D8 | 전략 C scale down 후 전략 B 배포 | 리소스 절약, Consumer Group 충돌 방지 |

## 생성/수정 파일

| 구분 | 파일 | 설명 |
|------|------|------|
| 생성 | `k8s/strategy-b/consumer-b-blue-deployment.yaml` | Blue Consumer Deployment + ClusterIP Service |
| 생성 | `k8s/strategy-b/consumer-b-green-deployment.yaml` | Green Consumer Deployment + Service (replicas: 0) |
| 생성 | `tools/strategy-b-switch.sh` | 전환/롤백 헬퍼 스크립트 (6개 함수) |
| 수정 | `tutorial/07-strategy-b-test.md` | 스켈레톤 → 상세 튜토리얼 (5개 시나리오) |
| 수정 | `plan/task06.md` | 구현 상태 및 설계 결정 추가 |
| 수정 | `plan/plan.md` | task06 상태 갱신 |

## 전환 절차 (헬퍼 스크립트 자동화)

```bash
source tools/strategy-b-switch.sh

# Blue→Green 전환 (8단계 자동 실행)
switch_to_green

# Green→Blue 롤백 (7단계 자동 실행)
switch_to_blue
```

수동 절차:
1. Blue의 현재 Offset 확인
2. Blue scale down (replicas=0)
3. Blue graceful shutdown 대기 (offset commit 완료)
4. Green group offset reset (`--to-current`)
5. Green scale up (replicas=3)
6. Green rollout 대기
7. ConfigMap active 업데이트

## 시나리오 1~5 수행

### 시나리오 목록

| # | 시나리오 | 핵심 검증 항목 |
|---|----------|----------------|
| 1 | 정상 Blue→Green 전환 | 전환 시간 <60초, Offset 동기화 정확도 |
| 2 | 전환 직후 즉시 롤백 | 롤백 시간 <60초, 역방향 Offset 동기화 |
| 3 | Lag 발생 중 전환 | `--to-current` 한계, 미소비 메시지 유실 관찰 |
| 4 | Rebalance 관찰 (전략 B 고유) | 별도 Group 격리 확인 |
| 5 | 전환 실패 후 수동 롤백 | 에러율 주입 후 수동 롤백 |

### 핵심 관찰 항목 (전략 B 고유)

| 항목 | 설명 |
|------|------|
| **Offset 동기화 정확도** | Green이 log-end-offset부터 정확히 소비를 시작하는지 |
| **전환 중 소비 공백** | Blue down ~ Green ready 사이의 미소비 구간 |
| **Lag 상태 전환 시 유실** | `--to-current` 사용 시 미소비 메시지 건너뜀 |
| **Rebalance 격리** | Green Rebalance 시 Blue Group 무영향 |

## 검증 기준

| 항목 | 기준 |
|------|------|
| 전환 완료 시간 | < 60초 (Offset 동기화 + Scale 변경 포함) |
| 롤백 완료 시간 | < 60초 |
| 메시지 유실 (정상 전환) | 0건 |
| 메시지 유실 (Lag 전환) | 측정 및 기록 (전략 B 한계) |
| Offset 동기화 정확도 | 100% (log-end-offset = Green 시작 offset) |
| Rebalance 격리 | 별도 Group 격리 확인 |

## 결과 정리

| 시나리오 | 전환 시간 | 롤백 시간 | 유실 | 중복 | 결과 |
|----------|-----------|-----------|------|------|------|
| 1. 정상 전환 | | | | | |
| 2. 즉시 롤백 | | | | | |
| 3. Lag 중 전환 | | | | | |
| 4. Rebalance 관찰 | | | | | |
| 5. 수동 롤백 | | | | | |
