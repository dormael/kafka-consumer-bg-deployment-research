# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Kafka Consumer Blue/Green 배포 전략을 검증하는 연구 프로젝트. 세 가지 전략(C: Pause/Resume Atomic Switch, B: Offset Sync, E: Kafka Connect)을 동일한 5개 시나리오로 테스트하여 메시지 유실/중복, 전환 시간, 롤백 시간을 측정한다.

**핵심 제약:** Kubernetes v1.23.8 (EOL) 단일 노드 환경. 모든 컴포넌트 버전이 이 제약에 맞춰 선택됨.

## Build & Run Commands

### Java Apps (Consumer, Producer) — Spring Boot 2.7.18, Java 17
```bash
# Build
cd apps/consumer && mvn package -DskipTests -B
cd apps/producer && mvn package -DskipTests -B

# Docker image (imagePullPolicy: Never, local 전용)
cd apps/consumer && docker build -t bg-test-consumer:latest .
cd apps/producer && docker build -t bg-test-producer:latest .
```

### Go Apps (Switch Controller, Sidecar) — Go 1.21, Controller: client-go v0.29.0, Sidecar: 표준 라이브러리만
```bash
# Build
cd apps/switch-controller && go build ./cmd/controller
cd apps/switch-sidecar && go build ./cmd/sidecar

# 의존성 문제 시 반드시 go mod tidy 먼저 실행
cd apps/switch-controller && go mod tidy && go build ./cmd/controller
cd apps/switch-sidecar && go mod tidy && go build ./cmd/sidecar

# Docker image
cd apps/switch-controller && docker build -t bg-switch-controller:latest .
cd apps/switch-sidecar && docker build -t bg-switch-sidecar:latest .
```

### Python Validator
```bash
pip install -r tools/validator/requirements.txt
python tools/validator/validator.py --source loki --loki-url http://localhost:3100 \
  --start <ISO8601> --end <ISO8601> \
  --switch-start <ISO8601> --switch-end <ISO8601> \
  --strategy C --output report/result.md
```

### K8s Deployment
```bash
kubectl apply -f k8s/namespaces.yaml
kubectl apply -f k8s/switch-rbac.yaml
kubectl apply -f k8s/consumer-state-configmap.yaml
kubectl apply -f k8s/consumer-configmap.yaml
kubectl apply -f k8s/producer-deployment.yaml
kubectl apply -f k8s/consumer-blue-statefulset.yaml
kubectl apply -f k8s/consumer-green-statefulset.yaml
kubectl apply -f k8s/switch-controller-deployment.yaml
```

## Architecture

4개의 애플리케이션이 협력하여 Blue/Green 전환을 수행한다:

```
Producer (Java) → Kafka topic (bg-test-topic, 8 partitions)
                    ↓
    Blue Consumer StatefulSet (3 pods, ACTIVE/PAUSED)
    Green Consumer StatefulSet (3 pods, PAUSED/ACTIVE)
                    ↑
Switch Controller (Go) ← ConfigMap (kafka-consumer-active-version)
  ├── L1: Consumer lifecycle HTTP 직접 호출
  ├── L2: Sidecar HTTP push (/desired-state)
  └── Step 0: kafka-consumer-state ConfigMap 선기록
Switch Sidecar (Go, consumer pod 내 sidecar)
  ├── L3: Reconcile Loop (5초 주기, 캐시 vs actual 비교)
  └── L4: Volume Mount File Polling (ConfigMap → /etc/consumer-state)
```

### 전환 흐름 (Strategy C) — 4-레이어 안전망
1. ConfigMap(`kafka-consumer-active-version`)의 `active` 값 변경 (blue→green)
2. Controller가 `kafka-consumer-state` ConfigMap에 desired state 선기록 (Step 0)
3. Controller가 Lease 획득 후 Blue에 pause, Green에 resume 직접 호출 (L1)
4. Controller가 Sidecar에 HTTP push로 desired state 전달 (L2, best-effort)
5. Sidecar Reconciler가 5초 주기로 desired vs actual 비교 후 불일치 시 전환 (L3)
6. Volume Mount 파일 갱신(60-90초) 후 Sidecar가 파일에서 desired state 읽기 (L4, 최후 fallback)

### 핵심 설계 결정
- **Consumer 기본 PAUSED 시작**: Pod 재시작 시 Dual-Active 원천 차단 (P0 해결)
- **4-레이어 안전망**: L1(Controller→Consumer HTTP) → L2(Controller→Sidecar push) → L3(Reconcile Loop) → L4(Volume Mount)
- **Static Membership** (`group.instance.id = ${HOSTNAME}`) + **CooperativeStickyAssignor**: Rebalance 최소화
- **StatefulSet**: Pod 이름 안정성 → Static Membership ID 유지
- **K8s Lease API**: Controller 간 상호 배제 (외부 서비스 없이)
- **PauseAwareRebalanceListener**: Rebalance 후 pause 상태 재적용 (pause 유실 방어)
- **Sidecar client-go 제거**: 표준 라이브러리만 사용, File Polling + HTTP Push 패턴으로 재작성

## Key File Locations

| 역할 | 경로 |
|------|------|
| Consumer 핵심 로직 | `apps/consumer/src/main/java/.../service/MessageConsumerService.java` |
| Rebalance 방어 리스너 | `apps/consumer/src/main/java/.../listener/PauseAwareRebalanceListener.java` |
| Consumer Lifecycle API | `apps/consumer/src/main/java/.../controller/LifecycleController.java` |
| Producer 메시지 생성 | `apps/producer/src/main/java/.../service/MessageProducerService.java` |
| 전환 오케스트레이션 | `apps/switch-controller/internal/controller/switch_controller.go` |
| Sidecar Reconciler | `apps/switch-sidecar/internal/reconciler/reconciler.go` |
| Sidecar Desired State 타입 | `apps/switch-sidecar/internal/reconciler/types.go` |
| Sidecar Lifecycle Client | `apps/switch-sidecar/internal/lifecycle/client.go` |
| 검증 분석 엔진 | `tools/validator/analyzer.py` |
| 버전 선택 근거 | `plan/decisions.md` |
| 변경 이력 (P0/P1/P2) | `plan/changes.md` |
| 아키텍처 개선 계획 | `plan/task05-post-improvement.md` |
| 테스트 튜토리얼 | `tutorial/06-strategy-c-test.md` (Strategy C, 7 시나리오) |

## Consumer HTTP Endpoints (`:8080`)

| Endpoint | Method | 용도 |
|----------|--------|------|
| `/lifecycle/pause` | POST | 소비 일시 정지 |
| `/lifecycle/resume` | POST | 소비 재개 |
| `/lifecycle/status` | GET | 현재 상태 (ACTIVE=0, PAUSED=1, DRAINING=2) |
| `/fault/*` | PUT | 장애 주입 (processing-delay, error-rate, commit-delay) |
| `/actuator/prometheus` | GET | Prometheus 메트릭 |

## Sidecar HTTP Endpoints (`:8082`)

| Endpoint | Method | 용도 |
|----------|--------|------|
| `/desired-state` | POST | Controller로부터 desired state 수신 (L2 push) |
| `/healthz` | GET | 헬스체크 |
| `/readyz` | GET | 레디니스 체크 (Consumer 상태 포함) |
| `/metrics` | GET | Prometheus 메트릭 |

## Metric Naming Convention

모든 커스텀 메트릭은 `bg_` 접두사를 사용한다:
- Consumer: `bg_consumer_messages_received_total`, `bg_consumer_lifecycle_state`, `bg_consumer_last_sequence_number`
- Producer: `bg_producer_messages_sent_total`, `bg_producer_configured_tps`, `bg_producer_last_sequence_number`
- Switch Controller: `bg_switch_duration_seconds`, `bg_switch_initiated_total`, `bg_switch_rollback_total`
- Switch Sidecar: `sidecar_lifecycle_commands_total`, `sidecar_lifecycle_command_errors_total`, `sidecar_configmap_updates_total`, `sidecar_current_state`

## Known P2 Issues (Deferred)

- Consumer DRAINING 단계에서 실제 drain 대기 미구현
- Consumer Lag 기반 자동 롤백 미구현
- 구조화 로그 포맷 불일치 (logstash-logback-encoder 미사용)
- 전환 5초 목표 달성을 위한 DrainTimeout 튜닝 필요 (기본값 10초)

## Language & Documentation Convention

- 프로젝트 문서(plan/, tutorial/)는 한국어로 작성
- 코드 내 변수명, 로그 메시지, 주석은 영어
- Go Controller는 표준 라이브러리 + client-go 패턴 (Informer, Lease)
- Go Sidecar는 표준 라이브러리만 사용 (client-go 제거, File Polling + HTTP Push 패턴)
- Java 앱은 Spring Boot 관례 (application.yaml, @Configuration, @RestController)
