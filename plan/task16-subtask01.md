# Task 16: 시나리오 3 — Consumer Lag 발생 중 전환

**Phase:** 3 - 테스트 수행
**의존성:** task14 (시나리오 1)
**전략별 수행 순서:** 전략 C (우선) → 전략 B → 전략 E
**튜토리얼:** `tutorial/16-scenario3-lag-during-switch.md`

---

## 목표

Consumer Lag가 높은 상태(> 500)에서 전환을 시도하여, 전략별 Lag 처리 방식(대기 vs 즉시 전환)의 차이와 메시지 유실 여부를 검증한다.

---

## 전제 조건

- [ ] Producer TPS 100 지속 생성 중
- [ ] Consumer에 "처리 지연" chaos injection 준비

---

## 수행 절차 (공통)

```
1. [Lag 유발] Blue Consumer에 처리 지연 주입
   PUT http://consumer-blue-0:8080/chaos/processing-delay?delayMs=500
   → 메시지당 500ms 지연 → TPS ~2로 감소 → Lag 급증

2. [Lag 확인] Lag > 500 도달 대기 (약 5~10초)
   Grafana 또는 Prometheus 쿼리로 확인

3. [전환 시도] Lag가 높은 상태에서 전환 트리거
   전략별 방식으로 전환 수행

4. [관찰] 전환 정책에 따른 동작 관찰
   - 정책 A: Lag=0 대기 후 전환 → 전환 시간 증가, 유실 0
   - 정책 B: 즉시 전환 → 전환 시간 짧음, 중복 발생 가능

5. [Lag 소진 확인] 전환 완료 후 Green이 Lag를 소진하는지 확인

6. [Chaos Reset] 처리 지연 제거
   POST http://consumer-green-0:8080/chaos/reset

7. [Validator 실행] 유실/중복 확인
```

---

## 전략별 Lag 처리 방식 비교

### 전략 C

Switch Controller의 `drainTimeoutSeconds` 설정에 따라:
- **대기 모드**: Blue의 Lag가 소진될 때까지 pause를 보류 → 전환 시간 증가
- **즉시 모드**: Lag 무관하게 즉시 pause → 전환 빠르지만 in-flight 메시지 중복 가능
- 테스트 시 두 모드 모두 수행

### 전략 B

- Offset 동기화 시점의 Blue offset이 Green에 전달되므로, Blue가 처리 완료하지 못한 메시지는 Green이 재처리
- 중복 발생 예상

### 전략 E

- Connector pause 후 내부적으로 batch 처리 완료를 기다림 (프레임워크 동작)
- config topic에 commit된 offset 기준으로 Green이 재개

---

## 측정 항목

| 항목 | 전략 C (대기) | 전략 C (즉시) | 전략 B | 전략 E |
|------|-------------|-------------|--------|--------|
| 전환 소요 시간 | 측정 (> 기본) | < 5초 | 측정 | 측정 |
| Lag 소진 시간 | 측정 | 측정 | 측정 | 측정 |
| 메시지 유실 | 0건 | 0건 | 0건 | 0건 |
| 메시지 중복 | 측정 | 측정 | 측정 | 측정 |

---

## 완료 기준

- [ ] Lag > 500 상태에서 전환 시도 완료
- [ ] 전략별 Lag 처리 방식 차이 기록
- [ ] 모든 전략에서 메시지 유실 0건
- [ ] 전략별 중복 건수 비교 데이터 수집
