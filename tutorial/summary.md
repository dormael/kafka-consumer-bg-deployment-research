# Tutorial Summary: Kafka Consumer Blue/Green 배포 전략 검증

> **프로젝트 목적:** Kafka Consumer의 Blue/Green 배포 전략 3가지(C, B, E)를 동일한 시나리오로 테스트하여 메시지 유실/중복, 전환 시간, 롤백 시간을 측정한다.
>
> **환경 제약:** Kubernetes v1.23.8 (EOL) 단일 노드, 모든 컴포넌트 버전이 이 제약에 맞춰 선택됨.

---

## 튜토리얼 구성

| # | 제목 | 핵심 내용 | 상세 |
|---|------|-----------|------|
| 01 | [클러스터 기본 셋업](01-cluster-setup.md) | K8s 확인, Helm repo, 네임스페이스 생성 | 리소스 계획 (~3.2 CPU, ~4.9Gi) |
| 02 | [모니터링 스택 설치](02-monitoring-setup.md) | Prometheus + Grafana + Loki | Alertmanager, Promtail 포함 |
| 03 | [Kafka 클러스터 설치](03-kafka-setup.md) | Strimzi Operator + KRaft 모드 Kafka | 토픽, KafkaConnect 배포 |
| 04 | [Argo Rollouts + KEDA 설치](04-argo-rollouts-setup.md) | Argo Rollouts Controller, KEDA | B/G 테스트 및 향후 확장용 |
| 05 | [앱 빌드 및 배포](05-producer-consumer-build.md) | Producer, Consumer, Controller, Sidecar | 전체 시스템 동작 확인 |
| **06** | [**전략 C 테스트**](06-strategy-c-test.md) | **Pause/Resume Atomic Switch (7 시나리오)** | **핵심 전략, 4-레이어 안전망** |
| 07 | [전략 B 테스트](07-strategy-b-test.md) | 별도 Consumer Group + Offset 동기화 (5 시나리오) | Scale 기반 전환 |
| 08 | [전략 E 테스트](08-strategy-e-test.md) | Kafka Connect REST API / Strimzi CRD (5+ 시나리오) | CRD vs REST 비교 |
| 09 | [환경 정리](09-cleanup.md) | 전체 리소스 역순 삭제 | 원클릭 정리 스크립트 |

---

## 인프라 셋업 흐름 (Tutorial 01~05)

```
01. 클러스터 확인 & 네임스페이스 생성
 └→ 02. Prometheus + Grafana + Loki (모니터링)
     └→ 03. Strimzi Operator → Kafka 클러스터 (KRaft) → Topic + KafkaConnect
         └→ 04. Argo Rollouts + KEDA (선택)
             └→ 05. Producer/Consumer/Controller/Sidecar 빌드 & 배포
```

### 주요 컴포넌트 버전

| 컴포넌트 | 버전 | 비고 |
|----------|------|------|
| Kubernetes | v1.23.8 | EOL, 단일 노드 |
| Strimzi Operator | 0.43.0 | KRaft 모드 지원 |
| Kafka | 3.8.0 | KRaft (ZooKeeper 없음) |
| kube-prometheus-stack | 51.10.0 | Prometheus + Grafana |
| Loki Stack | 2.10.2 | 로그 수집 |
| Argo Rollouts | 2.35.3 | B/G 배포 컨트롤러 |
| KEDA | 2.9.4 | 이벤트 기반 오토스케일링 |
| Java | 17 | Spring Boot 2.7.18 |
| Go | 1.21 | Controller: client-go, Sidecar: 표준 라이브러리만 |

---

## 시스템 아키텍처

```
Producer (Java, 100 TPS) → Kafka Topic (bg-test-topic, 8 partitions)
                                ↓
                Blue Consumer StatefulSet (3 pods)
                Green Consumer StatefulSet (3 pods)
                                ↑
          Switch Controller (Go) ← ConfigMap (active-version)
            ├── L1: Consumer HTTP 직접 호출
            ├── L2: Sidecar HTTP push
            └── Step 0: kafka-consumer-state ConfigMap 선기록
          Switch Sidecar (Go, 각 Consumer Pod 내)
            ├── L3: Reconcile Loop (5초 주기)
            └── L4: Volume Mount File Polling (60-90초)
```

### 4-레이어 안전망 (전략 C)

| 레이어 | 경로 | 지연 | 실패 조건 |
|--------|------|------|-----------|
| L1 | Controller → Consumer HTTP | ~1초 | Controller 다운 |
| L2 | Controller → Sidecar HTTP push | ~1초 | Controller 다운 |
| L3 | Sidecar Reconcile Loop | 5초 주기 | Sidecar 재시작 (캐시 소실) |
| L4 | Volume Mount File Polling | 60-90초 | kubelet 장애 |

---

## 3개 전략 비교

### 구조 비교

| 항목 | 전략 C (Pause/Resume) | 전략 B (Offset 동기화) | 전략 E (Kafka Connect) |
|------|----------------------|----------------------|----------------------|
| Consumer Group | 동일 (`bg-test-group`) | 별도 (`blue`/`green`) | 별도 (Connect Worker별) |
| 전환 방식 | HTTP pause/resume | Scale down/up + offset reset | CRD state patch |
| 예상 전환 시간 | **~1초** | 30~60초 | 5~30초 |
| K8s 워크로드 | StatefulSet | Deployment | KafkaConnect CRD |
| Sidecar | 필요 (4-레이어) | 불필요 | 불필요 |
| Pod 재시작 | 불필요 | 필요 (scale) | 불필요 |
| 상태 영속성 | X (in-memory) | N/A | O (config topic) |
| Static Membership | 사용 | 미사용 | N/A |
| PAUSED 측 Lag 문제 | 있음 (~50% 파티션) | 없음 | 없음 |
| Dual-Active 위험 | 있음 (P0으로 해결) | 없음 (Scale 기반) | 없음 |
| 인프라 복잡도 | 높음 (Controller+Sidecar) | 낮음 | 중간 (Strimzi) |
| 자동화 수준 | Controller 자동 | 수동/스크립트 | CRD 선언적 |

### 테스트 시나리오 매핑

| 시나리오 | 전략 C | 전략 B | 전략 E |
|----------|--------|--------|--------|
| 정상 전환 | 시나리오 1 | 시나리오 1 | 시나리오 1 (CRD) + 1-B (REST) |
| 즉시 롤백 | 시나리오 2 | 시나리오 2 | 시나리오 2 |
| Lag 중 전환 | 시나리오 3 | 시나리오 3 | 시나리오 3 |
| Rebalance/격리 | 시나리오 4 (P0 검증) | 시나리오 4 (Group 격리) | - |
| 전환 실패/롤백 | 시나리오 5 | 시나리오 5 | 시나리오 5 |
| L2 Consumer 재시작 | 시나리오 6 | - | - |
| L4 Controller 다운 | 시나리오 7 | - | - |
| config topic 영속성 | - | - | 추가 검증 1 |
| CRD vs REST 충돌 | - | - | 추가 검증 2 |

---

## 전략별 핵심 특성 요약

### 전략 C: Pause/Resume Atomic Switch

- **가장 빠른 전환** (~1초): HTTP pause/resume만으로 전환, Pod 재시작 불필요
- **같은 Consumer Group 공유**: PAUSED 측에 할당된 파티션이 Lag 누적 (구조적 한계, ~50%)
- **4-레이어 안전망**: Controller 장애에도 Sidecar가 자동 복구
- **P0 해결**: Consumer 기본 PAUSED 시작 + Sidecar Reconciler로 Dual-Active 원천 차단
- **7개 시나리오** 포함 (가장 상세한 테스트)

### 전략 B: 별도 Consumer Group + Offset 동기화

- **완전 격리**: Blue/Green이 독립 Consumer Group → Rebalance 상호 영향 없음
- **PAUSED 측 Lag 문제 없음**: ACTIVE 측이 8개 파티션 전체 소비
- **느린 전환** (30~60초): Pod scale down/up + offset reset 필요
- **Lag 상태 전환 시 유실 가능**: `--to-current`가 log-end-offset으로 설정하므로 미소비 Lag 메시지 건너뜀
- **수동/스크립트 자동화**: `tools/strategy-b-switch.sh` 헬퍼 제공

### 전략 E: Kafka Connect REST API / Strimzi CRD

- **선언적 전환**: CRD `spec.state` 패치만으로 전환 (5~30초)
- **config topic 영속성**: Worker 재시작 후에도 pause/stop 상태 복원
- **CRD vs REST API 이중 경로**: CRD가 권위적 소스, REST API는 일시적 (Strimzi reconcile ~120초에 복원)
- **Pod 재시작 불필요**: Worker는 유지, Connector만 상태 전환
- **FileStreamSinkConnector 사용**: 커스텀 장애 주입 API 미지원 (tasksMax 조절로 대체)

---

## 발견된 버그 및 해결 이력

### P0 (Critical)

| 버그 | 근본 원인 | 해결 |
|------|-----------|------|
| PAUSED 측 Pod 재시작 시 Dual-Active | `INITIAL_STATE=ACTIVE` 정적 env var | Consumer 기본 PAUSED + Sidecar Reconciler |

### P1 (High)

| 버그 | 근본 원인 | 해결 |
|------|-----------|------|
| Sidecar 초기 연결 실패 후 재시도 안 함 | Consumer 기동 전 3회 재시도 후 포기 | Reconciler 5초 주기 polling |

### P2 (Deferred)

- Consumer DRAINING 단계에서 실제 drain 대기 미구현
- Consumer Lag 기반 자동 롤백 미구현
- Lease holder 업데이트 실패 (기능 영향 없음)
- DrainTimeout 튜닝 필요 (기본값 10초 → 5초 목표)

---

## 테스트 결과 (전략 C, 2026-02-21)

| 시나리오 | 전환 시간 | 롤백 시간 | 동시 Active | 결과 |
|----------|-----------|-----------|-------------|------|
| 1. 정상 전환 | 1.04초 | - | 0회 | **통과** |
| 2. 즉시 롤백 | 1.04초 | 1.03초 | 0회 | **통과** |
| 3. Lag 중 전환 | 1.04초 | 1.03초 | 0회 | **통과** |
| 4. Rebalance 장애 | 1.08초 | 1.03초 | **1회** | **미통과** |
| 5. 자동 롤백 | 1.04초 | (수동)1.03초 | 0회 | **부분 통과** |

> 시나리오 4의 Dual-Active는 아키텍처 개선(4-레이어 안전망) 후 재검증 예정.
> 시나리오 5의 자동 롤백은 P2 이슈(미구현)으로 수동 롤백으로 대체.

---

## HTTP 엔드포인트 요약

### Consumer (`:8080`)

| Endpoint | Method | 용도 |
|----------|--------|------|
| `/lifecycle/pause` | POST | 소비 일시 정지 |
| `/lifecycle/resume` | POST | 소비 재개 |
| `/lifecycle/status` | GET | 상태 조회 (ACTIVE/PAUSED/DRAINING) |
| `/fault/processing-delay` | PUT | 처리 지연 주입 |
| `/fault/error-rate` | PUT | 에러율 주입 |
| `/actuator/prometheus` | GET | Prometheus 메트릭 |

### Sidecar (`:8082`)

| Endpoint | Method | 용도 |
|----------|--------|------|
| `/desired-state` | POST | Controller → desired state 수신 (L2) |
| `/healthz` | GET | 헬스체크 |
| `/readyz` | GET | 레디니스 (Consumer 상태 포함) |
| `/metrics` | GET | Prometheus 메트릭 |

---

## 헬퍼 스크립트

| 스크립트 | 전략 | 주요 함수 |
|----------|------|-----------|
| (Tutorial 06 인라인) | C | `check_status`, `check_all`, `check_lag`, `sc_logs`, `check_active`, `check_desired_state`, `emergency_set_state` |
| `tools/strategy-b-switch.sh` | B | `check_offsets`, `check_b_status`, `switch_to_green`, `switch_to_blue`, `inject_fault`, `clear_fault` |
| `tools/strategy-e-switch.sh` | E | `check_e_status`, `check_e_offsets`, `switch_to_green_crd`, `switch_to_blue_crd`, `switch_to_green_rest`, `switch_to_blue_rest`, `test_config_persist`, `test_crd_rest_conflict` |

---

## 진행 순서 가이드

### 최소 실행 경로 (전략 C만 테스트)

```
01 → 02 → 03 → 05 → 06 → 09
```

### 전체 실행 경로 (3개 전략 비교)

```
01 → 02 → 03 → 04(선택) → 05 → 06 → 07 → 08 → 09
```

### 전략 전환 시 주의사항

- **전략 B 테스트 전**: 전략 C의 StatefulSet과 Switch Controller를 scale down
- **전략 E 테스트 전**: 전략 C와 B 모두 scale down
- **원복 시**: 각 튜토리얼 하단의 "정리 및 원복" 섹션 참조
