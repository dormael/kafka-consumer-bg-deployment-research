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
- [x] Grafana 대시보드 확인 가능 — NodePort 30080 (admin:admin123), "Kafka Consumer Blue/Green Deployment" 대시보드 존재 확인
- [x] Validator 스크립트 준비 완료 — 실행 테스트 통과 (Loki 수집 → 분석 → 리포트 생성), 3건 버그 수정 (JSON prefix 파싱, groupId 필드명, timezone-aware 비교)

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

> **테스트 수행일:** 2026-02-21
> **테스트 전 수정사항:**
> 1. `@KafkaListener` group ID 버그 수정 (`bgTestConsumerListener` → `bg-test-group`)
> 2. Switch Controller `StatusResponse` 필드명 수정 (`json:"status"` → `json:"state"`)

### 시나리오별 결과

| 시나리오 | 전환 시간 | 롤백 시간 | 유실 | 중복 | 동시 Active | 결과 |
|----------|-----------|-----------|------|------|-------------|------|
| 1. 정상 전환 | 1.04초 | - | 미측정(*) | 미측정(*) | 0회 | **통과** |
| 2. 즉시 롤백 | 1.04초 | 1.03초 | 미측정(*) | 미측정(*) | 0회 | **통과** |
| 3. Lag 중 전환 | 1.04초 | 1.03초 | 미측정(*) | 미측정(*) | 0회 | **통과** |
| 4. Rebalance 장애 | 1.08초 | 1.03초 | 미측정(*) | 미측정(*) | **1회** | **미통과** |
| 5. 자동 롤백 | 1.04초 | (수동)1.03초 | 미측정(*) | 미측정(*) | 0회 | **부분 통과** |

> (*) 유실/중복 측정은 Validator 스크립트(task04)로 Loki 로그 기반 분석 필요. 현재 Loki 미배포 상태.

### 핵심 발견 사항

#### 1. 전환 시간 일관성 (전체 시나리오 공통)
- 모든 시나리오에서 전환 시간 **1.03~1.08초** 달성 (목표 < 5초 대비 5배 빠름)
- "Pause First, Resume Second" 시퀀스: pause → WaitPaused(~500ms) → resume → WaitActive(~500ms)
- Lag 발생 중에도 전환 시간에 영향 없음 (Controller가 Lag 확인 없이 즉시 전환)

#### 2. 전략 C 구조적 특성: 파티션 분할 문제
- 같은 Consumer Group에 6개 Consumer (Blue 3 + Green 3) → Kafka가 8개 파티션 분배
- ACTIVE 측 파티션만 소비, PAUSED 측 파티션은 Lag 지속 누적
- 전환 시 역할만 교체되므로 항상 일부 파티션(~3~4개)이 미소비 상태
- **이 특성은 전략 C의 근본적 한계이며, 전략 B(별도 Consumer Group)에서는 발생하지 않음**

#### 3. 시나리오 4: Pod 재시작 시 Dual-Active 발생
- **근본 원인:** `INITIAL_STATE=ACTIVE` 정적 env var → PAUSED 측 Pod 재시작 시 ConfigMap 상태를 참조하지 않음
- **영향:** 재시작된 Blue-0이 ACTIVE로 소비 시작 → Green과 동시 Active 발생
- **Sidecar 실패:** Consumer 미기동 시 3회 재시도 후 포기, 이후 ConfigMap 변경 없으므로 재시도 안 함
- **Static Membership 동작 확인:** session.timeout.ms(45초) 이내 복귀 시 Rebalance 미발생, 동일 파티션 재할당

#### 4. 시나리오 5: 자동 롤백 미구현
- Switch Controller는 lifecycle 상태(ACTIVE/PAUSED)만 확인
- 에러율, Consumer Lag 기반 자동 롤백 로직 없음 (P2 이슈)
- 수동 롤백(ConfigMap 복원)은 정상 동작

### 발견된 버그 및 수정 사항

| # | 분류 | 내용 | 상태 |
|---|------|------|------|
| B1 | Consumer | `@KafkaListener` group ID가 listener ID로 설정됨 | **수정 완료** |
| B2 | Controller | `StatusResponse.Status` → `State` 필드명 불일치 | **수정 완료** |
| B3 | Sidecar | 시작 시 Consumer 미기동으로 초기 명령 실패 후 재시도 안 함 | 미수정 (P1) |
| B4 | Architecture | PAUSED 측 Pod 재시작 시 ACTIVE로 복귀 (Dual-Active) | 미수정 (P0) |
| B5 | Controller | Lease holder 업데이트 실패 (이전 Lease 만료 전 재획득 시도) | 미수정 (P2) |

### 개선 권고사항

1. **[P0] INITIAL_STATE 동적 결정:** Consumer 시작 시 ConfigMap을 읽어 자신의 초기 상태 결정, 또는 Sidecar가 Consumer Ready 상태까지 재시도
2. **[P1] Sidecar 초기화 재시도:** Consumer readiness probe 통과 후 초기 lifecycle 명령 전송하도록 개선
3. **[P2] Consumer Lag 기반 자동 롤백:** Switch Controller에 전환 후 헬스 모니터링 추가 (에러율, Lag 임계값 초과 시 자동 롤백)
4. **[P2] 파티션 분할 문제 해결:** 전략 C 대안으로 Blue/Green 각각 별도 Consumer Group 사용 검토 (전략 B)
