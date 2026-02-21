# Tutorial 07: 전략 B 테스트 수행 (별도 Consumer Group + Offset 동기화)

> **관련 태스크:** plan/task06.md
> **우선순위:** 2순위

---

## 사전 조건

- 전략 B용 Consumer가 배포되어 있어야 함 (Tutorial 05, Step 6)
- Blue Consumer (group.id: `bg-test-group-blue`) ACTIVE, 4 replicas
- Green Consumer (group.id: `bg-test-group-green`) 0 replicas (대기)

```bash
# Blue Consumer 상태 확인
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-blue --describe

# Green Consumer Group은 아직 미존재 (replicas=0)
```

---

## 시나리오 1: 정상 Blue → Green 전환

### 수행

```bash
SWITCH_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)

# Step 1: Blue의 현재 Offset 저장
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-blue --describe \
  > /tmp/blue-offsets.txt

cat /tmp/blue-offsets.txt

# Step 2: Blue Consumer 스케일 다운 (소비 중단)
kubectl scale deployment consumer-b-blue -n kafka-bg-test --replicas=0

# Step 3: Blue의 최종 Offset 확인 (commitSync 완료 대기)
sleep 5
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-blue --describe

# Step 4: Green Consumer Group의 Offset을 Blue 값으로 동기화
# 파티션별로 offset 설정 (아래는 예시, 실제값으로 대체)
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-green --topic bg-test-topic \
    --reset-offsets --to-current --execute \
    --dry-run

# 실제 실행 (dry-run 제거)
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-green --topic bg-test-topic \
    --reset-offsets --to-current --execute

# Step 5: Green Consumer 스케일 업
kubectl scale deployment consumer-b-green -n kafka-bg-test --replicas=4

# Step 6: Green Consumer Ready 대기
kubectl rollout status deployment consumer-b-green -n kafka-bg-test --timeout=60s

# Step 7: ConfigMap 업데이트
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"green"}}'

SWITCH_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "Switch duration: $(( $(date -d "$SWITCH_END" +%s) - $(date -d "$SWITCH_START" +%s) )) seconds"
```

### 검증

```bash
# Green Consumer Lag 확인
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-green --describe

# Validator 실행
python tools/validator/validator.py \
  --source loki --loki-url http://localhost:3100 \
  --start "$SWITCH_START" --end "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --switch-start "$SWITCH_START" --switch-end "$SWITCH_END" \
  --strategy B --output report/validation-b-scenario1.md
```

---

## 시나리오 2: 즉시 롤백

```bash
# Green → Blue 롤백 (동일 절차, 방향만 반대)
# 1. Green 스케일 다운
kubectl scale deployment consumer-b-green -n kafka-bg-test --replicas=0

# 2. Blue Group Offset 동기화 (Green의 현재 Offset으로)
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
    --group bg-test-group-blue --topic bg-test-topic \
    --reset-offsets --to-current --execute

# 3. Blue 스케일 업
kubectl scale deployment consumer-b-blue -n kafka-bg-test --replicas=4

# 4. ConfigMap 업데이트
kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
  --type merge -p '{"data":{"active":"blue"}}'
```

---

## 시나리오 3: Lag 발생 중 전환

```bash
# 처리 지연 주입 후 Lag > 500에서 전환 시도
# (Tutorial 06 시나리오 3과 동일 패턴)
```

---

## 시나리오 4: Rebalance 관찰 (전략 B 고유)

전략 B는 별도 Consumer Group을 사용하므로:

```bash
# Green 스케일 업 시 Green Group 내 Rebalance 관찰
kubectl scale deployment consumer-b-green -n kafka-bg-test --replicas=4

# Rebalance 발생 로그 확인
kubectl logs -n kafka-bg-test -l app=bg-consumer,color=green --tail=20 | grep -i rebalance

# Blue 스케일 다운 → Blue Group의 Rebalance는 Green에 영향 없음 확인
kubectl scale deployment consumer-b-blue -n kafka-bg-test --replicas=0
```

---

## 시나리오 5: 자동 롤백

```bash
# Argo Rollouts 연동으로 자동 롤백
# Green에 높은 에러율 주입 후 전환 → 헬스체크 실패 → 자동 롤백
# (AnalysisTemplate으로 Consumer 에러율 검증)
```

---

## 결과 요약 템플릿

```markdown
| 시나리오 | 전환 시간 | 롤백 시간 | 유실 | 중복 | 결과 |
|----------|-----------|-----------|------|------|------|
| 1. 정상 전환 | _초 | - | _건 | _건 | PASS/FAIL |
| 2. 즉시 롤백 | - | _초 | _건 | _건 | PASS/FAIL |
| 3. Lag 중 전환 | _초 | - | _건 | _건 | PASS/FAIL |
| 4. Rebalance 관찰 | N/A | N/A | N/A | N/A | 관찰 기록 |
| 5. 자동 롤백 | _초 | _초 | _건 | _건 | PASS/FAIL |
```

## 다음 단계

- [08-strategy-e-test.md](08-strategy-e-test.md): 전략 E 테스트 수행
