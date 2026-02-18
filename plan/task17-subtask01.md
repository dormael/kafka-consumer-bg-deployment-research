# Task 17: 시나리오 4 — 전환 중 Rebalance 장애 주입 (전략 C 전용)

**Phase:** 3 - 테스트 수행
**의존성:** task14 (시나리오 1)
**대상 전략:** 전략 C 전용
**튜토리얼:** `tutorial/17-scenario4-rebalance-injection.md`

---

## 목표

전환 도중 Consumer Pod를 재시작하여 Rebalance를 유발하고, 다음을 검증한다:
1. Rebalance 이후 pause 상태가 유지되는지 (BgRebalanceListener 동작)
2. 양쪽 동시 Active가 발생하지 않는지 (DualActiveConsumers 알람)
3. Static Membership(KIP-345)이 실제로 Rebalance를 억제하는지

---

## Subtask 17-01: 시나리오 4A — 전환 중 Rebalance 유발

### 수행 절차

```
1. Blue → Green 전환 시작
   POST http://switch-controller:8090/switch

2. 전환 진행 중(Blue DRAINING 상태)에 Blue Consumer Pod 1개 강제 삭제
   kubectl delete pod consumer-blue-2 -n kafka --force --grace-period=0

3. Rebalance 발생 확인
   - Kafka Broker 로그에서 JoinGroup/SyncGroup 요청 확인
   - bg_consumer_rebalance_count_total 지표 증가 확인

4. Rebalance 이후 Blue의 pause 상태 유지 확인
   GET http://consumer-blue-0:8080/lifecycle/status → PAUSED
   GET http://consumer-blue-1:8080/lifecycle/status → PAUSED
   (재시작된 consumer-blue-2 Pod가 PAUSED로 시작하는지 확인)

5. 양쪽 동시 Active 발생 여부 확인
   DualActiveConsumers 알람 미발화 확인
   bg_dual_active_events_total = 0 확인

6. 전환 최종 완료 확인
   Green ACTIVE 확인

7. Validator 실행
```

### 측정 항목

| 항목 | 기대값 |
|------|--------|
| Rebalance 후 pause 유지 | 유지됨 |
| 양쪽 동시 Active | 0회 |
| 전환 완료 | 성공 (시간 증가 허용) |
| 메시지 유실 | 0건 |

---

## Subtask 17-02: 시나리오 4B — Static Membership 동작 검증

### 수행 절차 (session.timeout.ms 이내 복귀)

```
1. Static Membership 설정 확인
   group.instance.id = ${POD_NAME} (StatefulSet에서 고정)
   session.timeout.ms = 45000 (45초)

2. Consumer Pod 재시작 (graceful)
   kubectl delete pod consumer-blue-1 -n kafka
   → StatefulSet이 동일 이름으로 재생성

3. Pod가 session.timeout.ms(45초) 이내에 복귀하는지 확인

4. Rebalance 미발생 확인
   - Kafka Broker 로그에서 JoinGroup 요청 부재 확인
   - bg_consumer_rebalance_count_total 변화 없음 확인

5. Consumer가 동일 파티션을 계속 소비 중인지 확인
```

### 수행 절차 (session.timeout.ms 초과)

```
1. session.timeout.ms = 10000 (10초)으로 축소 설정하여 테스트
   (또는 Pod 재시작을 지연시켜 10초 이상 소요되도록)

2. Consumer Pod 삭제 후 복귀 시간이 session.timeout.ms를 초과하도록 설정

3. Rebalance 발생 확인
   - Kafka Broker 로그에서 JoinGroup/SyncGroup 요청 확인
   - bg_consumer_rebalance_count_total 증가 확인

4. Rebalance 후 pause 상태 유지 확인 (BgRebalanceListener 검증)
```

### 측정 항목 (Static Membership)

| 항목 | session.timeout.ms 이내 | session.timeout.ms 초과 |
|------|------------------------|------------------------|
| Rebalance 발생 | 미발생 | 발생 |
| JoinGroup 요청 | 없음 | 있음 |
| 파티션 재할당 | 없음 | 발생 |
| pause 상태 유지 | N/A (Rebalance 없으므로) | 유지 (RebalanceListener) |

---

## Subtask 17-03: 시나리오 4C — max.poll.interval.ms 초과 유발

```
1. Consumer에 max.poll.interval.ms 초과 지연 주입
   PUT http://consumer-blue-0:8080/chaos/max-poll-exceed?delayMs=310000
   (max.poll.interval.ms = 300000 기본값 초과)

2. Kafka가 Consumer를 그룹에서 제외하는지 확인
   - Rebalance 발생
   - 해당 Consumer의 파티션이 다른 Consumer에 재할당

3. Rebalance 후 나머지 Consumer의 pause 상태 유지 확인

4. Chaos reset 후 Consumer가 그룹에 재합류하는지 확인
```

---

## 완료 기준

- [ ] 시나리오 4A: Rebalance 후 pause 상태 유지 확인
- [ ] 시나리오 4A: 양쪽 동시 Active = 0회
- [ ] 시나리오 4B: session.timeout.ms 이내 복귀 시 Rebalance 미발생 확인
- [ ] 시나리오 4B: session.timeout.ms 초과 시 Rebalance 발생 + pause 유지 확인
- [ ] 시나리오 4C: max.poll.interval.ms 초과 시 동작 관찰 완료
- [ ] Grafana 스크린샷: Rebalance 전후 지표 변화 캡처
