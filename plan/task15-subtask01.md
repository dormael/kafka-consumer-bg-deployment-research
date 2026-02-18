# Task 15: 시나리오 2 — 전환 직후 즉시 롤백

**Phase:** 3 - 테스트 수행
**의존성:** task14 (시나리오 1 완료 후 수행)
**전략별 수행 순서:** 전략 C (우선) → 전략 B → 전략 E
**튜토리얼:** `tutorial/15-scenario2-immediate-rollback.md`

---

## 목표

Blue → Green 전환 수행 직후 즉시 롤백을 트리거하여, 롤백 소요 시간과 Blue 재개 시 정상 소비 여부를 검증한다.

---

## 전제 조건

- [ ] 시나리오 1을 통해 전환/롤백 메커니즘이 동작함을 확인한 상태
- [ ] Producer TPS 100 지속 생성 중
- [ ] Blue Consumer Lag = 0

---

## 시나리오 2A: 전략 C

### 수행 절차

```
1. Blue → Green 전환 수행 (시나리오 1A 절차)
2. Green ACTIVE 확인 직후 (3초 이내) 롤백 트리거
   POST http://switch-controller:8090/rollback
   - 롤백 명령 시각 기록 (T_rollback_start)
3. Blue ACTIVE 재확인 시각 기록 (T_rollback_end)
   롤백 소요 시간 = T_rollback_end - T_rollback_start
4. Blue Consumer Lag 급증 여부 확인 (< 100 유지)
5. Validator 실행: 전환~롤백 구간의 유실/중복 확인
```

### 측정 항목

| 항목 | 기대값 |
|------|--------|
| 롤백 소요 시간 | < 5초 |
| Blue 재개 후 Consumer Lag | < 100 |
| 메시지 유실 (전환~롤백 구간) | 0건 |

---

## 시나리오 2B: 전략 B

### 수행 절차

```
1. Blue → Green 전환 수행 (시나리오 1B 절차)
2. Green 활성화 직후 롤백 트리거
   - Green pause/scale down
   - Blue Offset 복원 (스냅샷 사용)
   - Blue scale up / resume
3. 롤백 소요 시간 측정
4. Blue 정상 소비 재개 확인
```

### 측정 항목

| 항목 | 기대값 |
|------|--------|
| 롤백 소요 시간 | < 60초 |
| Blue Offset 복원 정확도 | 검증 |

---

## 시나리오 2C: 전략 E

### 수행 절차

```
1. Blue → Green 전환 (CRD patch)
2. 즉시 롤백 (CRD patch 방향 반대)
   kubectl patch kafkaconnector file-sink-green --type merge -p '{"spec":{"state":"stopped"}}'
   kubectl patch kafkaconnector file-sink-blue --type merge -p '{"spec":{"state":"running"}}'
3. 롤백 소요 시간 측정
4. config topic에서 상태 복원 확인
```

### 측정 항목

| 항목 | 기대값 |
|------|--------|
| 롤백 소요 시간 | 2~5초 |
| config topic 기반 상태 복원 | 정상 |

---

## 완료 기준

- [ ] 전략 C: 롤백 소요 시간 < 5초
- [ ] 전략 B: 롤백 소요 시간 < 60초
- [ ] 전략 E: 롤백 소요 시간 < 10초
- [ ] 모든 전략에서 롤백 후 Blue가 정상 소비 재개
- [ ] 전환~롤백 구간 메시지 유실 0건
