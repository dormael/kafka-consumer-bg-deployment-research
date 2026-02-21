# 계획 변경 이력

> 계획 변경 시 왜(Why)/어떻게(How)를 포함하여 기록한다.

---

## 2026-02-21: Task 04 Validator 실행 테스트 완료

### 수정된 버그

1. **[B6] Loki 로그 JSON 파싱 실패**
   - **Why**: Spring Boot 로그 라인이 `2026-02-21T... INFO ClassName - {"key":"val"}` 형태로, `json.loads(line)` 호출 시 prefix 때문에 파싱 실패. 모든 레코드가 skip됨.
   - **How**: `_parse_json_line()`에서 전체 라인 파싱 실패 시 `line.find("{")` 이후 부분을 재파싱하는 fallback 추가

2. **[B7] Consumer groupId 필드명 불일치**
   - **Why**: Consumer 로그는 `"groupId"` (camelCase), Validator는 `parsed.get("group_id")` (snake_case). 항상 "unknown" 반환.
   - **How**: `parsed.get("groupId", parsed.get("group_id", "unknown"))` — 양쪽 모두 지원

3. **[B8] timezone-aware vs naive datetime 비교 오류**
   - **Why**: `_parse_timestamp()`가 `datetime.utcfromtimestamp()` (naive) 사용, CLI 인자는 `datetime.fromisoformat()` (aware). Phase 분석 시 `TypeError: can't compare offset-naive and offset-aware datetimes` 발생.
   - **How**: `datetime.fromtimestamp(ts, tz=timezone.utc)` 로 변경하여 timezone-aware datetime 반환

### 실행 결과

- Loki 수집: Producer 30,005건, Consumer 14,952건 (5분간, 페이지네이션 7회)
- Phase 분석: Pre-Switch / During Switch / Post-Switch 구간별 유실/중복 분석 정상
- Markdown 리포트: `report/validation-c-test-run.md` 생성 확인
- **참고**: ~50% "유실"은 전략 C 구조적 특성 (PAUSED 측 파티션 미소비)이며 실제 유실 아님

---

## 2026-02-21: Task 05 전략 C 테스트 수행 완료

### 테스트 전 수정된 버그

1. **[B1] Consumer Group ID 버그**
   - **Why**: `@KafkaListener(id = "bgTestConsumerListener")`에서 `groupId` 미지정 시 `id`가 group.id로 사용됨. 실제 Consumer Group이 `bgTestConsumerListener`로 생성되어 `bg-test-group` 대신 사용.
   - **How**: `@KafkaListener`에 `groupId = "${spring.kafka.consumer.group-id:bg-test-group}"` 추가

2. **[B2] Switch Controller StatusResponse 필드명 불일치**
   - **Why**: `StatusResponse` 구조체가 `json:"status"`를 기대하나, Consumer lifecycle API는 `"state"` 키 반환. Go의 json.Decoder는 매칭 실패 시 zero value("") 반환하여 WaitForState가 영원히 성공 불가.
   - **How**: `json:"status"` → `json:"state"`, `status.Status` → `status.State` 변경

### 테스트 결과 요약

| 시나리오 | 전환 시간 | 결과 |
|----------|-----------|------|
| 1. 정상 전환 | 1.04초 | 통과 |
| 2. 즉시 롤백 | 1.03초 | 통과 |
| 3. Lag 중 전환 | 1.04초 | 통과 |
| 4. Rebalance 장애 | 1.08초 | **미통과** (Dual-Active 발생) |
| 5. 자동 롤백 | 1.04초 | **부분 통과** (자동 롤백 미구현) |

### 신규 발견 이슈

| 우선순위 | 이슈 | 설명 |
|----------|------|------|
| **P0** | PAUSED 측 Pod 재시작 시 Dual-Active | `INITIAL_STATE=ACTIVE` 정적 env var → ConfigMap 상태 미참조 |
| **P1** | Sidecar 초기 연결 실패 후 재시도 안 함 | Consumer 기동 전 3회 재시도 후 포기, ConfigMap 변경 없으면 재시도 안 함 |
| **P2** | 에러율/Lag 기반 자동 롤백 미구현 | Switch Controller가 lifecycle 상태만 확인, post-switch 헬스 모니터링 없음 |
| **P2** | Lease holder 업데이트 실패 | 이전 Lease 만료 전 재획득 시도 시 에러 (기능 영향 없음) |

### 전략 C 구조적 특성 (파티션 분할 문제)

- 같은 Consumer Group에 6개 Consumer → 8개 파티션 분배
- PAUSED 측 파티션(3~4개)은 항상 미소비 → Lag 지속 누적
- 전환 시 역할 교체되므로 전체 파티션 소비 불가능
- **전략 B(별도 Consumer Group)에서는 이 문제 발생하지 않음**

### 다음 단계

| 태스크 | 상태 | 비고 |
|--------|------|------|
| ~~Task 05: 전략 C 테스트~~ | **완료** | 5개 시나리오 수행 |
| Task 06: 전략 B 테스트 | **다음 단계** | 별도 Consumer Group + Offset 동기화 |
| Task 07: 전략 E 테스트 | 대기 | KafkaConnect 기반 |
| Task 08: 테스트 리포트 | 대기 | Task 05~07 완료 후 |

---

## 2026-02-21: Task 02, 03 Docker 빌드 및 K8s 배포 완료

### 빌드

- **Java 앱 (Producer, Consumer)**: `mvn package -DskipTests -B` → `minikube image build`
  - JAVA_HOME을 Java 17로 지정 필요 (호스트 기본값이 Java 8)
  - Consumer 컴파일 오류 수정: `PauseAwareRebalanceListener.onPartitionsRevoked()` → `onPartitionsRevokedAfterCommit()` (spring-kafka 2.8.11에서 `ConsumerAwareRebalanceListener`는 `onPartitionsRevoked(Consumer, Collection)` 시그니처 미제공)
- **Go 앱 (Switch Controller, Sidecar)**: `minikube image build` (Dockerfile 내 멀티스테이지 빌드)
- **이미지 빌드 방식**: `minikube -p kafka-bg-test image build` 사용 (containerd 런타임이므로 `docker-env` 대신)

### 배포 (kafka-bg-test 네임스페이스)

| 리소스 | Pod 수 | READY 상태 | 동작 확인 |
|--------|--------|-----------|----------|
| bg-test-producer (Deployment) | 1 | 1/1 | TPS 100 메시지 전송 중 |
| consumer-blue (StatefulSet) | 3 | 2/2 | ACTIVE — 메시지 소비 중 |
| consumer-green (StatefulSet) | 3 | 2/2 | PAUSED — PauseAwareRebalanceListener re-pause 정상 동작 |
| bg-switch-controller (Deployment) | 1 | 1/1 | active=blue 감시 중 |

### 검증 사항

- ConfigMap `kafka-consumer-active-version`: `active=blue` 정상
- Green Consumer: Rebalance 시 `PauseAwareRebalanceListener`가 파티션 re-pause 수행 확인
- Producer: 시퀀스 번호 기반 메시지 연속 전송 확인

### 다음 단계

- **Task 05: 전략 C 테스트 수행** — 앱 배포 완료로 즉시 진행 가능

---

## 2026-02-21: Task 02, 03 코드 리뷰 및 P0/P1 이슈 수정

### Task 02 (Java Producer/Consumer) — 완성도 87% → 코드 완료

**수정 사항:**
1. **[P0] Health Probe 활성화**: `management.endpoint.health.probes.enabled: true` 추가 (Producer, Consumer 두 앱 모두). 미설정 시 K8s에서 Pod CrashLoopBackOff 발생 가능.
2. **[P1] Producer 자동 시작**: `producer.auto-start` 설정 추가 + `@PostConstruct`에서 자동 `start()`. 기존에는 수동 `POST /producer/start` 호출이 필요했음.
3. **[P1] 지표명 수정**: `bg_producer_messages_sent_rate` → `bg_producer_configured_tps`. 실제 측정된 TPS가 아닌 설정값을 노출하므로 이름과 의미를 일치시킴.
4. **[P2] Deprecated API 제거**: `ListenableFutureCallback` → `completable().whenComplete()`. Spring Boot 2.7.x 호환 유지.

**남은 P2 이슈 (메모):**
- 구조화 로그 혼합 포맷 (`logstash-logback-encoder` 미사용)
- DRAINING 단계 실제 drain 대기 없음

### Task 03 (Go Switch Controller/Sidecar) — 완성도 82% → 코드 완료, 빌드 확인

**수정 사항:**
1. **[P0] go.mod 의존성 수정**: `k8s.io/kube-openapi`와 `k8s.io/utils` 버전이 잘못된 커밋 해시를 참조하여 빌드 불가. k8s 1.29 호환 버전으로 교체 (`kube-openapi v0.0.0-20231010175941-2dd684a91f00`, `utils v0.0.0-20230726121419-3b25d923346b`).
2. **[P0] go.sum 생성**: 기존 빈 파일 → `go mod tidy` 실행으로 정상 생성. 두 모듈 모두 `go build ./...` 성공 확인.
3. **[P1] Lease 미해제 버그 수정**: `switch_controller.go`에서 `rollback()` 호출 후 `releaseLease(ctx)` 누락 (Step 5, 6, 7 실패 경로). 미수정 시 전환 실패 후 30초간 Lease가 잠겨 재시도 불가.
4. **[P2] Deployment 환경변수 정렬**: 코드에서 읽는 `LIFECYCLE_PORT` 추가, 코드에서 사용하지 않는 `BLUE_REPLICAS`, `GREEN_REPLICAS`, `CONSUMER_PORT`, `HEALTH_CHECK_TIMEOUT_SECONDS`, `METRICS_PORT`, `HEALTH_PORT` 제거.

**남은 P2 이슈 (메모):**
- Consumer Lag 기반 자동 롤백 미구현
- SwitchRollbackTotal 카운팅 경로 불일치
- 전환 5초 목표: DrainTimeout 기본값 10초, 환경변수 튜닝 필요

### 미완료 항목 업데이트

| 태스크 | 상태 | 선행 조건 |
|--------|------|-----------|
| ~~Task 02 Docker 빌드 & K8s 배포~~ | ~~완료~~ | ~~2026-02-21 배포 완료~~ |
| ~~Task 03 Docker 빌드 & K8s 배포~~ | ~~완료~~ | ~~2026-02-21 배포 완료~~ |
| Task 05: 전략 C 테스트 | **다음 단계** | 앱 배포 완료, 즉시 진행 가능 |
| Task 06: 전략 B 테스트 | 대기 | 전략 C 테스트 후 |
| Task 07: 전략 E 테스트 | 대기 | KafkaConnect 클러스터 Ready |
| Task 08: 테스트 리포트 | 대기 | Task 05, 06, 07 완료 후 |

---

## 2026-02-21: Task 01~04 코드 생성 및 인프라 설치 완료

### 완료 항목

#### Task 01: K8s 인프라 셋업
- **코드 생성 완료** (16개 파일): Helm values 5개, K8s 매니페스트 10개, Grafana 대시보드 1개
- **인프라 설치 완료**: 모든 컴포넌트 Running/Ready 상태 확인
  - monitoring: Prometheus, Grafana (NodePort 30080), Alertmanager, Loki, Promtail
  - kafka: Strimzi Operator, Kafka Cluster (KRaft, 1 broker), Kafka Exporter, KafkaConnect Blue/Green
  - argo-rollouts: Argo Rollouts Controller + Dashboard
  - keda: KEDA Operator + Metrics API Server
  - kafka-bg-test: ConfigMap(active-version), RBAC(bg-switch-sa)
  - bg-test-topic: 8파티션, RF=1, Ready

#### Task 02: Producer/Consumer Java Spring Boot 앱 구현
- **코드 생성 완료** (21개 파일): Producer 8개, Consumer 13개
- Spring Boot 2.7.18, Java 17, Micrometer, 구조화 JSON 로그
- Producer: TPS 100, 시퀀스 번호, REST API 런타임 제어
- Consumer: Lifecycle pause/resume, Static Membership, 장애 주입, Rebalance 방어

#### Task 03: Switch Controller/Sidecar Go 구현
- **코드 생성 완료** (15개 파일): Controller 8개, Sidecar 7개
- Go 1.21, client-go v0.29.0
- Controller: ConfigMap Watch, Lease 기반 상호 배제, 자동 롤백
- Sidecar: ConfigMap Watch, lifecycle HTTP 클라이언트, 재시도 로직
- **참고**: `go.sum`은 빈 플레이스홀더. 빌드 전 `go mod tidy` 필요

#### Task 04: Validator 스크립트 구현 (Python)
- **코드 생성 완료** (6개 파일): validator.py, loki_client.py, file_reader.py, analyzer.py, reporter.py, requirements.txt
- Loki/파일 기반 시퀀스 수집, O(1) 유실/중복 분석, 구간별 분석, Markdown 리포트

### 변경 사항

1. **Strimzi watchNamespaces 설정 변경**
   - **Why**: `watchNamespaces: [kafka, kafka-bg-test]`로 설정 시 release 네임스페이스(kafka)와 watchNamespaces가 중복되어 동일 RoleBinding이 2번 생성됨 (Helm chart 버그)
   - **How**: `watchNamespaces: [kafka-bg-test]`로 변경 (release 네임스페이스는 자동 포함)

2. **Grafana 대시보드 ConfigMap 수동 생성 필요**
   - **Why**: prometheus-values.yaml에서 `dashboardsConfigMaps.custom: kafka-bg-grafana-dashboards`를 참조하나, 해당 ConfigMap이 Helm 설치 시점에 존재하지 않아 Grafana Pod가 마운트 실패
   - **How**: `kubectl create configmap kafka-bg-grafana-dashboards --from-file=...` 으로 별도 생성 후 Grafana Pod 재시작

### 미완료 항목 (다음 단계)

| 태스크 | 상태 | 선행 조건 |
|--------|------|-----------|
| ~~Task 02 Docker 빌드 & K8s 배포~~ | ~~완료~~ | ~~2026-02-21 배포 완료~~ |
| ~~Task 03 Docker 빌드 & K8s 배포~~ | ~~완료~~ | ~~2026-02-21 배포 완료~~ |
| Task 05: 전략 C 테스트 | **다음 단계** | 앱 배포 완료, 즉시 진행 가능 |
| Task 06: 전략 B 테스트 | 대기 | 전략 C 테스트 후 |
| Task 07: 전략 E 테스트 | 대기 | KafkaConnect 클러스터 Ready |
| Task 08: 테스트 리포트 | 대기 | Task 05, 06, 07 완료 후 |
