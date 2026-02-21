# Task 06: 전략 B 테스트 수행 (별도 Consumer Group + Offset 동기화)

> **의존:** task01, task02, task04
> **우선순위:** 2순위
> **튜토리얼:** `tutorial/07-strategy-b-test.md`

---

## 목표

전략 B(별도 Consumer Group + Offset 동기화)의 5가지 시나리오를 수행하고, 전략 C와 비교한다.

## 전략 B의 핵심 차이점 (vs 전략 C)

| 항목 | 전략 C | 전략 B |
|------|--------|--------|
| Consumer Group | 같은 group.id 공유 | **별도 group.id** (blue/green) |
| 전환 방식 | Pause/Resume | **Offset 동기화 후 Group 전환** |
| Sidecar 필요 | 필요 | **불필요** |
| Offset 동기화 | 불필요 (같은 Group) | **필수** (kafka-consumer-groups.sh) |
| 전환 시간 예상 | 1~3초 | **30초~2분** |

## 사전 준비

### 별도 Consumer Group 구성

```yaml
# Blue Consumer: group.id = bg-test-group-blue
# Green Consumer: group.id = bg-test-group-green (초기 replicas=0)
```

### ConfigMap 기반 Active 버전 관리

디자인 문서 6.4절의 ConfigMap을 사용하여 애플리케이션이 자신의 active 여부를 판단한다.

## 전환 절차

```bash
# 1. Blue Consumer의 현재 Offset 확인
kafka-consumer-groups.sh --bootstrap-server kafka:9092 \
  --group bg-test-group-blue --describe

# 2. Green Consumer Group의 Offset을 Blue 값으로 동기화
kafka-consumer-groups.sh --bootstrap-server kafka:9092 \
  --group bg-test-group-green --topic bg-test-topic \
  --reset-offsets --to-offset <blue-current-offset> --execute

# 3. Green Consumer 스케일 업
kubectl scale deployment consumer-green -n kafka-bg-test --replicas=4

# 4. Green Consumer Ready 대기
kubectl rollout status deployment consumer-green -n kafka-bg-test

# 5. Blue Consumer 스케일 다운
kubectl scale deployment consumer-blue -n kafka-bg-test --replicas=0

# 6. ConfigMap 업데이트
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"green"}}'
```

## 시나리오 1~5 수행

전략 C의 task05.md와 동일한 5가지 시나리오를 수행하되, 전환 방식만 위의 Offset 동기화 절차로 대체한다.

### 핵심 관찰 항목 (전략 B 고유)

| 항목 | 설명 |
|------|------|
| **Offset 동기화 정확도** | Green이 Blue의 마지막 Offset부터 정확히 소비를 시작하는지 |
| **전환 중 중복 메시지** | Offset 동기화와 Blue 중단 사이의 Gap으로 인한 중복 |
| **Rebalance 발생** | Green 스케일 업 시 Rebalance 발생 및 소요 시간 |
| **전환 소요 시간** | Offset 동기화 + 스케일 변경 + Rebalance 완료까지 |

### 시나리오 4 변형 (전략 B 전용)

전략 B는 별도 Consumer Group을 사용하므로, Rebalance가 Group 내부에서만 발생한다. 시나리오 4에서는:
- Green 스케일 업 시 발생하는 Group 내 Rebalance 관찰
- Blue 스케일 다운 시 발생하는 상황 관찰 (별도 Group이므로 Green에 영향 없음)

## 검증 기준

| 항목 | 기준 |
|------|------|
| 전환 완료 시간 | < 30초 (Offset 동기화 + 스케일 변경 포함) |
| 롤백 완료 시간 | < 60초 |
| 메시지 유실 | 0건 |
| Offset 동기화 정확도 | 100% (Blue 마지막 Offset = Green 시작 Offset) |

## 결과 정리

| 시나리오 | 전환 시간 | 롤백 시간 | 유실 | 중복 | 결과 |
|----------|-----------|-----------|------|------|------|
| 1. 정상 전환 | | | | | |
| 2. 즉시 롤백 | | | | | |
| 3. Lag 중 전환 | | | | | |
| 4. Rebalance 관찰 | | | | | |
| 5. 자동 롤백 | | | | | |
