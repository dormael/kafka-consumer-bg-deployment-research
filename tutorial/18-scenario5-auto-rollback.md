# Tutorial 18: 시나리오 5 — 전환 실패 후 자동 롤백

> Green Consumer에 높은 에러율을 주입한 뒤 전환하여, 자동 롤백이 동작하는지 검증합니다.

---

## Step 1: Green Consumer에 에러율 주입

```bash
# Green Consumer가 배포된 상태에서 에러율 50% 설정
for i in 0 1 2 3; do
  kubectl exec -n kafka consumer-green-$i -c consumer -- \
    curl -s -X PUT "http://localhost:8080/chaos/error-rate?rate=0.5"
done
```

## Step 2: 전환 수행

```bash
echo "전환 시작: $(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)"
curl -X POST http://localhost:8090/switch
```

## Step 3: 자동 롤백 관찰

Switch Controller가 전환 후 헬스체크를 수행하고, 에러율 > 1%를 감지하면 자동 롤백을 트리거합니다.

```bash
# Switch Controller 로그 모니터링
kubectl logs -f -n kafka deployment/bg-switch-controller

# 예상 로그:
# INFO: Switch blue -> green completed
# WARN: Error rate 0.50 exceeds threshold 0.01, triggering rollback
# INFO: Rollback green -> blue started
# INFO: Rollback completed
```

## Step 4: Blue 복구 확인

```bash
# Blue ACTIVE 확인
for i in 0 1 2 3; do
  kubectl exec -n kafka consumer-blue-$i -c consumer -- \
    curl -s http://localhost:8080/lifecycle/status | jq .state
done
# "ACTIVE"

# Consumer Lag 정상 회복 확인
```

## Step 5: Chaos 리셋

```bash
for i in 0 1 2 3; do
  kubectl exec -n kafka consumer-green-$i -c consumer -- \
    curl -s -X POST http://localhost:8080/chaos/reset
done
```

## Step 6: Validator 실행

```bash
python tools/validator/validator.py \
  --loki-url http://localhost:3100 \
  --start "<전환시각>" \
  --end "<롤백완료+5분>" \
  --consumer-color blue \
  --scenario "S5-strategy-C-auto-rollback" \
  --output report/scenario-5-strategy-C.md
```

---

## 결과 기록

| 항목 | 결과 |
|------|------|
| 자동 롤백 수행 여부 | |
| 에러 감지 ~ 롤백 완료 시간 | ___초 |
| Blue 복구 후 메시지 유실 | ___건 |
| Blue 복구 후 Consumer Lag | ___ |
