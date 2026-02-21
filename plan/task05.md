# Task 05: 전략 C 테스트 수행 (Pause/Resume Atomic Switch)

> **의존:** task01, task02, task03, task04
> **우선순위:** **1순위** (주 권장 전략)
> **튜토리얼:** `tutorial/06-strategy-c-test.md`

---

## 목표

전략 C(Pause/Resume Atomic Switch)의 5가지 시나리오를 순서대로 수행하고, 지표/로그를 수집하여 검증 기준 통과 여부를 확인한다.

## 사전 조건

- [x] Kafka 클러스터 정상 동작 — KRaft 1 broker, bg-test-topic 8 partitions Ready
- [x] Producer가 TPS 100으로 메시지 생성 중 — 배포 완료, 시퀀스 연속 전송 확인
- [x] Blue Consumer가 정상 소비 중 (Lag = 0) — 3 pods ACTIVE, 메시지 소비 확인
- [x] Green Consumer가 PAUSED 상태로 대기 중 — 3 pods PAUSED, re-pause 동작 확인
- [x] Switch Controller 정상 동작 — active=blue 감시 중, Lease 획득 가능
- [ ] Grafana 대시보드 확인 가능 — 미확인
- [ ] Validator 스크립트 준비 완료 — 코드 생성 완료, 실행 테스트 미수행

## 시나리오 1: 정상 Blue → Green 전환

### 수행 절차

1. **사전 확인**
   ```bash
   # Blue Consumer Lag = 0 확인
   kubectl exec -n kafka kafka-cluster-kafka-0 -- bin/kafka-consumer-groups.sh \
     --bootstrap-server localhost:9092 --group bg-test-group --describe

   # Green Pod Ready 확인
   kubectl get pods -n kafka-bg-test -l color=green
   ```

2. **전환 수행**
   ```bash
   # ConfigMap 업데이트로 전환 트리거
   kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
     --type merge -p '{"data":{"active":"green"}}'
   ```

3. **측정 항목**
   - 전환 명령 시각 기록
   - Green ACTIVE 확인 시각 기록
   - 전환 소요 시간 = Green ACTIVE 확인 시각 - 전환 명령 시각

4. **검증**
   ```bash
   # Validator로 유실/중복 확인
   python tools/validator/validator.py \
     --source loki --loki-url http://localhost:3100 \
     --start <test_start> --end <test_end> \
     --switch-start <switch_start> --switch-end <switch_end> \
     --strategy C --output report/validation-c-scenario1.md
   ```

### 검증 기준

| 항목 | 기준 | 통과 조건 |
|------|------|-----------|
| 전환 완료 시간 | < 5초 | 필수 |
| 메시지 유실 | 0건 | 필수 |
| Green Consumer Lag | < 100 (전환 후 2분 이내) | 필수 |
| 양쪽 동시 Active | 0회 | 필수 |

### 수집 대상 스크린샷 (Grafana)

- Consumer Lag 시계열 (전환 전후)
- Lifecycle 상태 변경 이벤트
- Messages/sec (Blue vs Green)

---

## 시나리오 2: 전환 직후 즉시 롤백

### 수행 절차

1. Blue → Green 전환 수행 (시나리오 1과 동일)
2. Green 활성화 확인 즉시 (5초 이내) 롤백 트리거
   ```bash
   kubectl patch configmap kafka-consumer-active-version -n kafka-bg-test \
     --type merge -p '{"data":{"active":"blue"}}'
   ```
3. Blue ACTIVE 재확인

### 검증 기준

| 항목 | 기준 |
|------|------|
| 롤백 완료 시간 | < 5초 |
| Blue 재개 후 Consumer Lag | < 100 유지 |
| 메시지 유실 | 0건 |

---

## 시나리오 3: Consumer Lag 발생 중 전환

### 수행 절차

1. Consumer 처리 지연 주입
   ```bash
   # 메시지당 200ms 처리 지연 → Consumer Lag > 500 유발
   curl -X PUT http://<consumer-svc>:8080/fault/processing-delay \
     -H "Content-Type: application/json" -d '{"delayMs": 200}'
   ```
2. Lag > 500 확인 후 전환 시도
3. 전환 정책 관찰:
   - **Lag 소진 대기 모드**: Switch Controller가 Lag = 0 까지 대기 후 전환
   - **즉시 전환 모드**: Lag 무관하게 즉시 전환
4. 장애 주입 해제
   ```bash
   curl -X PUT http://<consumer-svc>:8080/fault/processing-delay \
     -H "Content-Type: application/json" -d '{"delayMs": 0}'
   ```

### 검증 기준

| 항목 | 기준 |
|------|------|
| Lag 소진 대기 중 메시지 유실 | 0건 |
| 전략별 Lag 처리 방식 차이 | 측정 및 기록 |

---

## 시나리오 4: 전환 중 Rebalance 장애 주입 (전략 C 전용)

### 수행 절차

1. 전환 도중 Consumer Pod 임의 재시작
   ```bash
   # 전환 시작 직후 Blue Pod 1개 강제 삭제
   kubectl delete pod consumer-blue-0 -n kafka-bg-test
   ```
2. Rebalance 이후 pause 상태 유지 확인
3. 양쪽 동시 Active 발생 여부 모니터링
   - `bg_switch_dual_active_detected` 지표 확인

### Static Membership 동작 검증 (sub-scenario)

1. **`group.instance.id` 적용 상태에서 Pod 재시작**
   ```bash
   kubectl delete pod consumer-blue-0 -n kafka-bg-test
   # session.timeout.ms(45초) 이내에 Pod 복귀 확인
   ```

2. **Broker 로그 확인**: JoinGroup/SyncGroup 요청 발생 여부
   ```bash
   kubectl logs -n kafka kafka-cluster-kafka-0 | grep -E "JoinGroup|SyncGroup|Rebalance"
   ```

3. **Rebalance 지표 비교**
   ```
   # Prometheus 쿼리
   bg_consumer_rebalance_count_total{group="bg-test-group"}
   ```

4. **session.timeout.ms 초과 테스트**
   ```bash
   # Pod를 삭제하고 60초간 복귀하지 않도록 스케일 조정
   kubectl scale statefulset consumer-blue --replicas=2 -n kafka-bg-test
   # 45초(session.timeout.ms) 경과 후 Rebalance 발생 확인
   kubectl scale statefulset consumer-blue --replicas=3 -n kafka-bg-test
   ```

### 검증 기준

| 항목 | 기준 |
|------|------|
| Rebalance 이후 pause 상태 유지 | 필수 |
| 양쪽 동시 Active 발생 | 0회 |
| Static Membership: session.timeout.ms 이내 복귀 시 Rebalance 미발생 | 필수 |
| Static Membership: session.timeout.ms 초과 시 Rebalance 발생 | 확인 |

---

## 시나리오 5: 전환 실패 후 자동 롤백

### 수행 절차

1. Green Consumer에 높은 에러율 주입
   ```bash
   curl -X PUT http://<green-consumer-svc>:8080/fault/error-rate \
     -H "Content-Type: application/json" -d '{"errorRatePercent": 80}'
   ```
2. 전환 수행
3. Switch Controller 또는 Argo Rollouts의 자동 롤백 동작 확인
4. Blue 복구 후 정상 소비 재개 확인

### 검증 기준

| 항목 | 기준 |
|------|------|
| 자동 롤백 수행 여부 | 확인 |
| Blue 복구 후 정상 소비 재개 | 필수 |
| 메시지 유실 | 0건 |

---

## 결과 정리

각 시나리오 수행 후 아래 형식으로 결과를 기록한다.

| 시나리오 | 전환 시간 | 롤백 시간 | 유실 | 중복 | 동시 Active | 결과 |
|----------|-----------|-----------|------|------|-------------|------|
| 1. 정상 전환 | | | | | | |
| 2. 즉시 롤백 | | | | | | |
| 3. Lag 중 전환 | | | | | | |
| 4. Rebalance 장애 | | | | | | |
| 5. 자동 롤백 | | | | | | |
