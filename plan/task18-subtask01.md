# Task 18: 시나리오 5 — 전환 실패 후 자동 롤백

**Phase:** 3 - 테스트 수행
**의존성:** task14 (시나리오 1)
**전략별 수행 순서:** 전략 C (우선) → 전략 B → 전략 E
**튜토리얼:** `tutorial/18-scenario5-auto-rollback.md`

---

## 목표

Green Consumer를 의도적으로 비정상 상태(높은 에러율)로 만든 뒤 전환하여, Switch Controller 또는 Argo Rollouts의 자동 롤백 동작을 검증한다.

---

## 수행 절차 (전략 C)

```
1. [Green 에러율 주입] Green Consumer에 높은 에러율 설정
   PUT http://consumer-green-0:8080/chaos/error-rate?rate=0.5
   → 50% 에러율 설정

2. [전환 수행]
   POST http://switch-controller:8090/switch

3. [전환 완료] Green ACTIVE → 즉시 에러 발생 시작

4. [헬스체크 실패 감지]
   Switch Controller 또는 Argo Rollouts AnalysisTemplate이
   Consumer 에러율 > 1%를 감지

5. [자동 롤백 트리거]
   - Switch Controller: rollbackOnFailure=true 설정에 의해 자동 롤백
   - Argo Rollouts: postPromotionAnalysis 실패 시 자동 롤백

6. [Blue 복구 확인]
   Blue ACTIVE 상태 재확인
   Blue Consumer Lag 정상 회복 확인
   메시지 유실 없이 정상 소비 재개 확인

7. [Chaos Reset]
   POST http://consumer-green-0:8080/chaos/reset
```

---

## 전략별 자동 롤백 메커니즘

### 전략 C: Switch Controller 내장 자동 롤백

```go
// Switch Controller에서 전환 후 헬스체크 수행
func (sc *SwitchController) postSwitchHealthCheck(activeColor string) error {
    for i := 0; i < healthCheckRetries; i++ {
        // Consumer 에러율 확인
        errorRate := sc.getErrorRate(activeColor)
        if errorRate > errorRateThreshold {
            log.Warnf("Error rate %.2f exceeds threshold, triggering rollback", errorRate)
            return sc.ExecuteRollback()
        }
        // Consumer Lag 확인
        lag := sc.getLag(activeColor)
        if lag > lagThreshold {
            log.Warnf("Lag %d exceeds threshold, triggering rollback", lag)
            return sc.ExecuteRollback()
        }
        time.Sleep(healthCheckInterval)
    }
    return nil
}
```

### 전략 B: Argo Rollouts postPromotionAnalysis

```yaml
postPromotionAnalysis:
  templates:
    - templateName: kafka-consumer-health-check
  args:
    - name: consumer-group
      value: bg-test-consumer-green
```

### 전략 E: Connector 상태 모니터링

```bash
# Green Connector의 task 상태 확인
curl http://connect-green:8083/connectors/file-sink-green/status
# task.state가 FAILED이면 자동 롤백
```

---

## 측정 항목

| 항목 | 기대값 |
|------|--------|
| 자동 롤백 수행 여부 | 수행됨 |
| 에러 감지 ~ 롤백 완료 시간 | 측정 |
| Blue 복구 후 메시지 유실 | 0건 |
| Blue 복구 후 Consumer Lag | 정상 회복 (< 100) |

---

## 완료 기준

- [ ] 전략 C: Switch Controller 자동 롤백 동작 확인
- [ ] 전략 B: Argo Rollouts postPromotionAnalysis 실패 → 자동 롤백 확인
- [ ] 전략 E: Connector FAILED 상태 감지 → 롤백 확인
- [ ] 모든 전략에서 Blue 복구 후 메시지 유실 없이 정상 소비 재개
