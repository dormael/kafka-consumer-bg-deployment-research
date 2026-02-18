# Kafka Consumer Blue/Green 배포 전략 검증 계획

> **버전:** v1.0
> **작성일:** 2026-02-18
> **근거 문서:** `kafka-consumer-bluegreen-design.md`, `kickoff-prompt.md`

---

## 1. 검증 범위 및 목표

### 1.1 검증 대상 전략

| 전략 | 설명 | 우선순위 |
|------|------|----------|
| **전략 C** | Pause/Resume Atomic Switch | 1순위 (주 권장 전략) |
| **전략 B** | 별도 Consumer Group + Offset 동기화 | 2순위 |
| **전략 E** | Kafka Connect REST API 기반 | 3순위 |

> 전략 A(단일 CG + Cooperative Rebalance), 전략 D(Shadow Consumer)는 설계 문서에서 비교 기준으로만 사용하며 별도 구현/검증 대상이 아님.

### 1.2 검증 목표 지표

| 목표 항목 | 기준값 |
|-----------|--------|
| 전환 소요 시간 | < 30초 전체 / 전략 C는 < 5초 |
| 롤백 소요 시간 | < 60초 전체 / 전략 C는 < 5초 |
| 전환 중 메시지 유실 | 0건 |
| 전환 중 메시지 중복 | 전략별 측정 후 비교 |
| 전환 중 양쪽 동시 Active | 0회 |

---

## 2. 작업 구조 (태스크 목록)

전체 작업은 4개의 Phase로 구분하며, 각 Phase는 하위 태스크로 세분화한다.

### Phase 1: 인프라 셋업 (Kubernetes Cluster)

| 태스크 ID | 제목 | 세부 파일 |
|-----------|------|-----------|
| task01 | Prometheus / Grafana 설치 | `plan/task01-subtask01.md` ~ |
| task02 | Loki (로그 수집) 설치 | `plan/task02-subtask01.md` ~ |
| task03 | Kafka Cluster 설치 (Strimzi Operator) | `plan/task03-subtask01.md` ~ |
| task04 | Argo Rollouts Controller 설치 | `plan/task04-subtask01.md` ~ |
| task05 | Strimzi KafkaConnect CRD 구성 (전략 E용) | `plan/task05-subtask01.md` ~ |
| task06 | KEDA 설치 (선택) | `plan/task06-subtask01.md` ~ |

### Phase 2: 애플리케이션 구현

| 태스크 ID | 제목 | 세부 파일 |
|-----------|------|-----------|
| task07 | Producer 앱 구현 (Spring Boot) | `plan/task07-subtask01.md` ~ |
| task08 | Consumer 앱 구현 (Spring Boot, Lifecycle 엔드포인트 포함) | `plan/task08-subtask01.md` ~ |
| task09 | Switch Sidecar 구현 (Go) | `plan/task09-subtask01.md` ~ |
| task10 | Switch Controller 구현 (Go) | `plan/task10-subtask01.md` ~ |
| task11 | Validator 스크립트 구현 | `plan/task11-subtask01.md` ~ |
| task12 | Grafana 대시보드 구성 | `plan/task12-subtask01.md` ~ |
| task13 | Prometheus Alert Rules 구성 | `plan/task13-subtask01.md` ~ |

### Phase 3: 테스트 수행

| 태스크 ID | 제목 | 세부 파일 |
|-----------|------|-----------|
| task14 | 시나리오 1: 정상 Blue→Green 전환 | `plan/task14-subtask01.md` ~ |
| task15 | 시나리오 2: 전환 직후 즉시 롤백 | `plan/task15-subtask01.md` ~ |
| task16 | 시나리오 3: Consumer Lag 발생 중 전환 | `plan/task16-subtask01.md` ~ |
| task17 | 시나리오 4: 전환 중 Rebalance 장애 주입 (전략 C 전용) | `plan/task17-subtask01.md` ~ |
| task18 | 시나리오 5: 전환 실패 후 자동 롤백 | `plan/task18-subtask01.md` ~ |

### Phase 4: 리포트

| 태스크 ID | 제목 | 세부 파일 |
|-----------|------|-----------|
| task19 | 테스트 리포트 작성 | `plan/task19-subtask01.md` |

---

## 3. Phase별 의존성

```
Phase 1 (인프라 셋업)
  ├── task01: Prometheus/Grafana ─┐
  ├── task02: Loki ───────────────┤
  ├── task03: Kafka Cluster ──────┼── 모두 완료 시 Phase 2 시작 가능
  ├── task04: Argo Rollouts ──────┤
  ├── task05: Strimzi Connect ────┤
  └── task06: KEDA (선택) ────────┘

Phase 2 (애플리케이션 구현)
  ├── task07: Producer ──────────────┐
  ├── task08: Consumer ──────────────┤
  ├── task09: Switch Sidecar ────────┼── 모두 완료 시 Phase 3 시작 가능
  ├── task10: Switch Controller ─────┤
  ├── task11: Validator ─────────────┤
  ├── task12: Grafana Dashboard ─────┤
  └── task13: Alert Rules ───────────┘

Phase 3 (테스트 수행) - 전략 C 우선
  ├── task14: 시나리오 1 (전략 C → B → E)
  ├── task15: 시나리오 2 (전략 C → B → E)
  ├── task16: 시나리오 3 (전략 C → B → E)
  ├── task17: 시나리오 4 (전략 C 전용)
  └── task18: 시나리오 5 (전략 C → B → E)

Phase 4 (리포트)
  └── task19: 테스트 리포트 작성
```

---

## 4. 전략별 구현 범위 요약

### 전략 C (Pause/Resume Atomic Switch) - 최우선

- **Consumer App**: `/lifecycle/pause`, `/lifecycle/resume`, `/lifecycle/status` 엔드포인트
- **Switch Sidecar (Go)**: ConfigMap/CRD Watch → Consumer HTTP POST
- **Switch Controller**: "Pause First, Resume Second" 오케스트레이션, K8s Lease 기반 동시 Active 방지
- **K8s 리소스**: StatefulSet (Blue/Green), ConfigMap (active-version), Lease (동시 Active 방지)
- **Argo Rollouts**: Pre/Post Promotion Analysis 연동

### 전략 B (별도 Consumer Group + Offset 동기화)

- **Consumer App**: 전략 C와 동일 앱 사용 (Lifecycle 엔드포인트는 전략 B에서 불필요, ConfigMap 기반 active 판단)
- **K8s 리소스**: Deployment (Blue/Green), ConfigMap, Offset 동기화 Job
- **Argo Rollouts**: Blue/Green 전략 연동

### 전략 E (Kafka Connect REST API)

- **SUT**: Strimzi `KafkaConnector` CRD + FileStreamSinkConnector
- **K8s 리소스**: KafkaConnect CRD (Blue/Green 별도 Cluster), KafkaConnector CRD
- **전환 방식**: `kubectl patch kafkaconnector` 또는 전환 스크립트
- **커스텀 Consumer 앱 불필요** (프레임워크 자체 검증)

---

## 5. TODO (향후 작업)

아래 항목은 현재 검증 범위에 포함하지 않으며, 추후 필요시 진행한다.

- [ ] 다른 배포 도구 검증: Flagger, OpenKruise Rollout, Keptn
- [ ] 다른 언어/프레임워크 구현체: Go (franz-go), Node.js (KafkaJS), Python (confluent-kafka-python)
- [ ] KEDA 기반 Consumer Lag 자동 스케일링 시나리오 심화 테스트
- [ ] KIP-848 (Next-Generation Consumer Rebalance Protocol) 적용 검증
- [ ] 프로덕션 환경 수준의 부하 테스트 (TPS 1000+)
