# Task 14: 시나리오 1 — 정상 Blue → Green 전환

**Phase:** 3 - 테스트 수행
**의존성:** Phase 2 전체 완료
**전략별 수행 순서:** 전략 C (우선) → 전략 B → 전략 E
**튜토리얼:** `tutorial/14-scenario1-normal-switch.md`

---

## 목표

각 전략별로 정상적인 Blue → Green 전환을 수행하고, 전환 소요 시간, 메시지 유실/중복, Consumer Lag 회복 시간을 측정한다.

---

## 전제 조건

- [ ] Producer가 TPS 100으로 메시지를 지속 생성 중
- [ ] Blue Consumer Lag = 0
- [ ] Grafana 대시보드 모니터링 중
- [ ] Validator 스크립트 준비 완료

---

## 시나리오 1A: 전략 C (Pause/Resume Atomic Switch)

### 수행 절차

```
1. [사전 확인] Blue Consumer 상태 확인
   GET http://consumer-blue:8080/lifecycle/status → ACTIVE
   Consumer Lag = 0 확인

2. [Green 배포] Green StatefulSet replicas=4로 스케일 업
   kubectl scale statefulset consumer-green -n kafka --replicas=4
   kubectl rollout status statefulset/consumer-green -n kafka
   Green 초기 상태: PAUSED

3. [전환 시작] 시각 기록 (T_start)
   POST http://switch-controller:8090/switch
   - 또는 ConfigMap 업데이트: active: green

4. [전환 완료 확인] Green ACTIVE 확인 시각 기록 (T_end)
   GET http://consumer-green:8080/lifecycle/status → ACTIVE
   전환 소요 시간 = T_end - T_start

5. [정상 소비 확인] Green이 모든 파티션(0~7)을 소비 중인지 확인
   Consumer Lag 모니터링 (2분 이내 < 100)

6. [유실/중복 확인] Validator 실행
   python validator.py --start T_start --end T_end+5min \
     --consumer-color green --scenario "S1-strategy-C"

7. [Blue Scale Down]
   kubectl scale statefulset consumer-blue -n kafka --replicas=0
```

### 측정 항목

| 항목 | 기대값 |
|------|--------|
| 전환 소요 시간 | < 5초 |
| 메시지 유실 | 0건 |
| 메시지 중복 | 측정 (0에 가까울수록 좋음) |
| Green Lag 회복 시간 | < 2분 |
| Rebalance 발생 횟수 | 0회 (Static Membership) |

---

## 시나리오 1B: 전략 B (별도 Consumer Group + Offset 동기화)

### 수행 절차

```
1. [사전 확인] Blue Consumer (group: bg-test-consumer-blue) Lag = 0

2. [Green 배포] Green Deployment replicas=4
   Green Consumer Group: bg-test-consumer-green
   auto.offset.reset: none

3. [Blue Pause] Blue Consumer 모든 Pod에 pause 전송
   (전략 B에서는 Consumer Group을 분리하므로 pause 대신 scale down도 가능)

4. [Offset 동기화] T_start 기록
   kafka-consumer-groups.sh --group bg-test-consumer-green \
     --reset-offsets --to-current --execute

5. [Green 활성화] ConfigMap active: green
   Green Consumer 소비 시작

6. [전환 완료 확인] T_end 기록

7. [유실/중복 확인] Validator 실행

8. [Blue Scale Down]
```

### 측정 항목

| 항목 | 기대값 |
|------|--------|
| 전환 소요 시간 | < 30초 |
| 메시지 유실 | 0건 |
| 메시지 중복 | 측정 (Offset 동기화 정확도에 의존) |
| Rebalance 발생 횟수 | Green 시작 시 1회 |

---

## 시나리오 1C: 전략 E (Kafka Connect REST API / CRD)

### 수행 절차

```
1. [사전 확인] Blue Connector (file-sink-blue) RUNNING 상태
   kubectl get kafkaconnector file-sink-blue -n kafka

2. [전환 시작] T_start 기록
   kubectl patch kafkaconnector file-sink-blue -n kafka \
     --type merge -p '{"spec":{"state":"stopped"}}'
   kubectl patch kafkaconnector file-sink-green -n kafka \
     --type merge -p '{"spec":{"state":"running"}}'

3. [전환 완료 확인] T_end 기록
   Green Connector RUNNING 확인

4. [유실/중복 확인]
   # 전략 E는 FileStreamSink이므로 파일 비교 또는 Connector 내장 offset 확인

5. [추가 검증] CRD 변경 vs REST API 직접 호출 비교
```

### 측정 항목

| 항목 | 기대값 |
|------|--------|
| 전환 소요 시간 | 2~5초 |
| 메시지 유실 | 0건 |
| CRD reconcile 충돌 | 없음 |
| config topic 기반 pause 영속성 | 유지됨 |

---

## Grafana 스크린샷 수집 항목

각 전략 수행 후 아래 시점의 스크린샷을 `report/screenshots/`에 저장한다:
1. 전환 시작 직전 (Blue ACTIVE, Lag=0)
2. 전환 중 (Blue PAUSED/DRAINING)
3. 전환 직후 (Green ACTIVE)
4. 전환 후 안정화 (5분 후)

---

## 완료 기준

- [ ] 전략 C: 전환 소요 시간 < 5초, 메시지 유실 0건
- [ ] 전략 B: 전환 소요 시간 < 30초, 메시지 유실 0건
- [ ] 전략 E: 전환 소요 시간 < 10초, CRD 기반 전환 정상 동작
- [ ] 각 전략의 Validator 결과 리포트 생성 완료
- [ ] Grafana 스크린샷 수집 완료
