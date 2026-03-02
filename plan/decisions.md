# Phase 2 핵심 설계 결정 근거

> **작성일:** 2026-02-27
> **수정일:** 2026-03-02
> **Phase 1 참조:** plan/test-phase01/decisions.md (컴포넌트 버전 선택 근거)

---

## 컴포넌트 버전

Phase 1에서는 K8s v1.23.8 제약으로 모든 컴포넌트가 구버전에 고정되었으나, Phase 2에서는 **Kafka 4.x (KIP-848 GA)** 활용을 위해 K8s 버전을 v1.30으로 올리고 전체 컴포넌트를 최신화한다.

### 버전 변경 요약

| 컴포넌트 | Phase 1 | Phase 2 | 변경 사유 |
|----------|---------|---------|----------|
| K8s (Minikube) | v1.23.8 | **v1.30.x** | Strimzi 0.50 (K8s 1.27+), KEDA 2.17 (K8s 1.30+) |
| Strimzi Operator | 0.43.0 | **0.50.1** | Kafka 4.1.x 지원, KRaft 전용 |
| Apache Kafka | 3.8.0 | **4.1.1 (KRaft)** | KIP-848 GA, KIP-1078 (rack-aware 개선) |
| kube-prometheus-stack | 51.10.0 | **69.x+** | K8s 1.30 호환, Prometheus 3.x |
| Grafana Loki | loki-stack 2.10.2 | **loki 6.x (app v3.4+)** | loki-stack 차트 deprecated → loki 차트 전환 |
| Argo Rollouts | v1.6.6 (Chart 2.35.3) | **v1.8.4** | Blue-Green 분석 버그 수정, K8s 1.30 호환 |
| KEDA | 2.9.3 | **2.17** | K8s 1.30 공식 지원 |
| Spring Boot | 2.7.18 | **3.4.x** | Java 17+, Jakarta EE 10, Micrometer 최신 |
| Spring Kafka | 2.8.11 | **3.3.x** | Spring Boot 3.4.x BOM 관리 |
| kafka-clients | 3.1.2 | **4.1.x (override)** | KIP-848 GA, 서버 사이드 할당 |
| Java | 8/11/17 | **17+** | Spring Boot 3.x, kafka-clients 4.x 최소 요구 |
| Go | 1.21+ | **1.22+** | 최신 안정 버전 |
| Python | 3.9+ | **3.11+** | 최신 안정 버전 |

### 버전 선택 상세 근거

#### Kubernetes v1.30.x
- **Strimzi 0.50.1**: K8s 1.27+ 필수
- **KEDA 2.17**: K8s 1.30~1.32 지원 (N-2 정책)
- **Argo Rollouts v1.8.4**: K8s 1.30 공식 테스트 대상
- v1.30은 Minikube에서 안정적으로 지원되는 최신 버전대

#### Strimzi 0.50.1 → Kafka 4.1.1
- 지원 Kafka: 4.0.0, 4.0.1, 4.1.0, 4.1.1
- Java 21 런타임 (Strimzi 자체)
- v1 API CRD (v1alpha1/v1beta1 deprecated, 1.0.0까지만 지원)
- **KRaft 전용**: Kafka 4.0부터 ZooKeeper 코드 완전 제거

#### Kafka 4.1.1
- **KIP-848 GA**: 새 Consumer Group Protocol 프로덕션 사용 가능
- **KIP-1078**: rack-aware 할당 개선 (multi-AZ 클러스터)
- **KIP-932 (Share Groups)**: Preview 상태 (참고용, 미적용)
- **Java 최소 요구**: Broker/Connect/Tools → Java 17+, Clients → Java 11+

#### Spring Boot 3.4.x + Spring Kafka 3.3.x
- Spring Boot 3.4.x BOM에서 Spring Kafka 3.3.x 자동 관리
- 기본 kafka-clients: 3.8.x → **4.1.x로 override** 필요
- `group.protocol=consumer`는 Spring Kafka properties로 전달 가능
- Jakarta EE 10 마이그레이션 필요 (`javax.*` → `jakarta.*`)

#### kafka-clients 4.1.x Override
```xml
<properties>
    <kafka.version>4.1.1</kafka.version>
</properties>
```
- Spring Kafka 3.3.x는 kafka-clients 3.8.x 컴파일 의존
- kafka-clients 4.x는 프로토콜 하위 호환으로 Spring Kafka 3.3.x와 동작
- KIP-848 활성화: `group.protocol=consumer` 속성만 추가

---

## Phase 2 고유 설계 결정

### D1. StatefulSet → Argo Rollouts (Deployment 기반) 전환

**결정:** Consumer 워크로드를 StatefulSet에서 Argo Rollouts Rollout CR(Deployment 기반)로 전환한다.

**근거:**
- Phase 1에서 StatefulSet의 안정적 Pod 이름은 Static Membership에만 필요
- Static Membership은 Deployment/Argo Rollouts와 호환 불가 (research-summary.md §4: "Deployment/Argo Rollouts로 전환 시 Pod 이름이 랜덤이므로 Static Membership은 호환 불가")
- Argo Rollouts는 AnalysisTemplate, 자동 롤백, prePromotionAnalysis 등 배포 자동화 기능을 제공하여 Switch Controller의 일부 역할을 대체 가능
- 프로덕션 환경에서는 Deployment가 StatefulSet보다 일반적이며, Argo Rollouts와의 조합이 더 실용적

**영향:**
- `group.instance.id: ${HOSTNAME}` 제거 → 모든 리밸런싱이 동적 멤버 방식으로 처리
- Pod 종료 시 `LeaveGroup` 전송 → 즉시 리밸런싱 발생 (Static Membership이면 생략 가능했음)
- Sidecar의 L4 (Volume Mount) ConfigMap 키를 hostname 기반에서 label 기반으로 재설계 필요

### D2. Static Membership 제거 + KIP-848로 대체

**결정:** Consumer의 `group.instance.id` 설정을 제거하고, **KIP-848 (New Consumer Group Protocol)**을 활용하여 리밸런싱 영향을 근본적으로 해소한다.

**근거:**
- Deployment 기반 Pod는 이름이 랜덤 → `group.instance.id: ${HOSTNAME}` 설정이 재시작 간 동일 ID를 보장하지 못함
- **KIP-848에서는 Static Membership의 중요도가 "필수" → "Nice-to-have"로 변경**
  - Classic Protocol: Static Membership 없으면 Stop-the-World → **필수**
  - KIP-848: 없어도 점진적 리밸런싱으로 영향 최소 → **Nice-to-have**
- `group.protocol=consumer` 한 줄 추가로 Phase 1의 CooperativeStickyAssignor + PauseAwareRebalanceListener보다 더 나은 결과

**KIP-848 적용 시 Consumer 설정 변경:**
```yaml
# Phase 1 (Classic Protocol)
spring.kafka.consumer.properties:
  group.instance.id: ${HOSTNAME}                    # 제거
  partition.assignment.strategy: CooperativeStickyAssignor  # 제거 (서버 사이드 할당)
  session.timeout.ms: 45000                         # 제거 (서버 사이드 관리)
  heartbeat.interval.ms: 3000                       # 제거 (서버 사이드 관리)

# Phase 2 (KIP-848)
spring.kafka.consumer.properties:
  group.protocol: consumer                          # KIP-848 활성화
  # session.timeout.ms → 서버: group.consumer.session.timeout.ms
  # heartbeat.interval.ms → 서버: group.consumer.heartbeat.interval.ms
```

**리밸런싱 성능 비교:**

| 측면 | Phase 1 (Classic + Static) | Phase 2 (KIP-848) |
|------|---------------------------|-------------------|
| Pod 재시작 시 | 리밸런싱 없음 (Static) | 점진적 리밸런싱 (~5초) |
| Stop-the-World | 발생 (Static 없으면) | **없음** |
| 리밸런싱 시간 | ~103초 (10 consumers, 900 partitions) | **~5초** (동일 조건, 20배 빠름) |
| 할당 로직 위치 | Client (Leader Consumer) | **Server (Group Coordinator)** |
| 영향 범위 | 전체 Consumer 멈춤 | 영향받는 파티션만 이동 |

### D3. Consumer STOPPED 상태 추가

**결정:** 기존 ACTIVE/PAUSED/DRAINING 외에 **STOPPED** 상태를 추가한다.

**근거:**
- Phase 1 단일 그룹 전략의 구조적 제한: PAUSED Consumer도 그룹에 가입하여 파티션을 할당받으므로, 해당 파티션의 메시지 처리가 중단됨
- STOPPED 상태에서는 `MessageListenerContainer.stop()` 호출 → Consumer가 그룹에서 완전히 탈퇴
- Green Consumer를 STOPPED 상태로 시작하면 그룹에 가입하지 않으므로 불필요한 리밸런싱 방지
- 전환 시: Blue stop → Green start + resume → Green만 그룹에 존재 → 모든 파티션 Green 소유

**`pause()` vs `stop()` 차이 (research-summary.md §5 참조):**

| 동작 | pause() | stop() |
|------|---------|--------|
| poll() 호출 | 계속 (빈 결과 반환) | 중단 |
| 그룹 멤버십 | **유지** | **탈퇴** (LeaveGroup) |
| 파티션 소유 | 유지 | 반납 |
| 리밸런싱 | 방지 | **트리거** |
| 타임아웃 | 방지 (poll() 계속) | 해당 없음 |

**Consumer Lifecycle 상태 머신 (Phase 2):**
```
STOPPED (그룹 미가입, 기본 시작 상태)
  │
  ├── start + resume → ACTIVE (그룹 가입, 소비 중)
  │                      │
  │                      ├── pause → PAUSED (그룹 유지, 소비 중단)
  │                      │             │
  │                      │             └── resume → ACTIVE
  │                      │
  │                      └── stop → STOPPED
  │
  └── (Consumer HTTP: /lifecycle/start, /lifecycle/stop)
```

### D4. Argo Rollouts Blue-Green 모드 활용 방식

**결정:** Argo Rollouts의 Blue-Green 전략을 사용하되, Service 스위칭이 아닌 **prePromotionAnalysis 웹후크**를 통해 Consumer lifecycle을 제어한다.

**근거:**
- Argo Rollouts B/G는 activeService/previewService 간 selector 스위칭이 핵심
- Kafka Consumer는 Service를 통해 트래픽을 받지 않으므로 Service 스위칭만으로는 전환 불가
- prePromotionAnalysis 내 웹후크가 Consumer API를 호출하여 실제 전환 수행
- Service는 Pod 발견(DNS/selector) 및 모니터링 용도로 활용

**단일 그룹 전환 시퀀스:**
1. Rollout 업데이트 → 새 ReplicaSet(Green) 생성 → Green pods STOPPED 상태로 시작
2. prePromotionAnalysis:
   a. Webhook: Blue Consumer stop (그룹 탈퇴 → 리밸런싱)
   b. Webhook: Green Consumer start + resume (그룹 가입 → 모든 파티션 할당)
   c. Prometheus: Consumer Lag 수렴 확인
3. Promote: Blue ReplicaSet scale down
4. postPromotionAnalysis: Error Rate, Lag 안정성 최종 확인

**개별 그룹 전환 시퀀스:**
1. Green Rollout의 replicas: 0 → N (scale up)
2. prePromotionAnalysis:
   a. Webhook: Blue group → Green group 오프셋 동기화
   b. Green Consumer 소비 시작
   c. Prometheus: Green Consumer Lag 수렴 확인
3. Blue Rollout의 replicas: N → 0 (scale down)
4. postPromotionAnalysis: 메시지 유실/중복 확인

### D5. Webhook Job 서비스 설계

**결정:** Argo Rollouts의 AnalysisTemplate webhook에서 호출하는 **경량 Webhook Job 서비스**를 구현한다.

**근거:**
- Argo Rollouts의 webhook은 HTTP endpoint를 호출하는 방식
- Consumer Pod의 개별 IP를 알 수 없으므로, 중간 서비스가 K8s API로 Pod 목록을 조회하여 각 Pod에 명령 전달
- 이 서비스가 오케스트레이션 로직을 집중 관리 (pause 순서, 타임아웃, 재시도 등)

**구현 옵션:**
1. **Go HTTP 서비스**: K8s API로 Pod 조회 → Consumer API 호출 (Approach A, C)
2. **K8s Job + curl**: 단순 스크립트로 Consumer API 호출 (최소 접근)
3. **기존 Switch Controller 재활용**: ConfigMap 인터페이스로 연동 (Approach B)

### D6. Sidecar L4 ConfigMap 키 재설계 (Approach B 전용)

**결정:** Approach B에서 Sidecar의 Volume Mount ConfigMap 키를 hostname 기반에서 **deployment-label 기반**으로 변경한다.

**근거:**
- Phase 1: `kafka-consumer-state` ConfigMap의 키가 `consumer-blue-0: ACTIVE` 형태 (hostname 기반)
- Deployment에서는 Pod hostname이 랜덤이므로 키 매칭 불가
- Blue/Green 단위 키로 변경: `blue: ACTIVE`, `green: PAUSED`
- 각 Sidecar는 환경변수(`BG_UNIT=blue`)로 자신의 키를 식별

### D7. 파티션 수 유지

**결정:** 토픽 파티션 수 8을 유지한다.

**근거:**
- Phase 1과 동일한 파티션 수로 비교 가능성 확보
- Consumer 3 replicas × 2 (Blue + Green) = 최대 6 Consumer
- 8 파티션 > 6 Consumer → 모든 Consumer가 최소 1개 파티션 할당 가능
- 개별 그룹의 경우 각 그룹당 3 Consumer vs 8 파티션 → 충분

### D8. KIP-848 Consumer Group Protocol 활용

**결정:** Kafka 4.1의 KIP-848 (New Consumer Group Protocol)을 Phase 2의 **주(primary) 프로토콜**로 사용하되, Classic Protocol과의 비교 테스트도 수행한다.

**근거:**
- KIP-848은 Kafka 4.0에서 GA → 프로덕션 사용 가능
- Stop-the-World 리밸런싱 제거 → Blue-Green 전환 시 처리 공백 최소화
- 서버 사이드 할당으로 Consumer Leader 부하 제거
- `group.protocol=consumer` 한 줄로 활성화 → 최소 변경

**KIP-848 동작 방식 (Blue-Green 시나리오):**

```
Green Pod 시작 시 (새 Consumer 가입):
1. Coordinator가 새 target assignment 계산 → 일부 파티션을 Green으로 이동 결정
2. 해당 파티션을 가진 Blue Pod만 revoke 요청 받음
3. 나머지 Blue Pod는 중단 없이 계속 소비 ✅
4. Green Pod가 해당 파티션 획득

Blue Pod 종료 시:
1. Coordinator가 종료된 Pod의 파티션만 재분배
2. 다른 모든 Consumer는 영향 없이 계속 소비 ✅
```

**테스트 접근:**
- **Primary**: `group.protocol=consumer` (KIP-848) — 모든 시나리오 실행
- **Comparison**: `group.protocol=classic` (Classic Protocol) — S1, S2 시나리오에서 비교 측정
- 비교 항목: 리밸런싱 시간, Stop-the-World 여부, 처리 공백, 전환 시간

**Spring Kafka에서 KIP-848 활성화 시 제거할 Classic Protocol 설정:**
- `partition.assignment.strategy` → 서버 사이드 `group.consumer.assignors`로 대체
- `session.timeout.ms` → 서버 사이드 `group.consumer.session.timeout.ms`로 대체
- `heartbeat.interval.ms` → 서버 사이드 `group.consumer.heartbeat.interval.ms`로 대체

### D9. Kubernetes 버전 업그레이드 (v1.23.8 → v1.30.x)

**결정:** Minikube K8s 버전을 v1.23.8에서 **v1.30.x**로 업그레이드한다.

**근거:**
- K8s 1.23은 2023년 2월 EOL — Phase 1에서는 기존 환경 제약이었으나 Phase 2에서는 제약 해제
- Strimzi 0.50.1: K8s 1.27+ 필수
- KEDA 2.17: K8s 1.30~1.32 공식 지원 (N-2 정책)
- Argo Rollouts v1.8.4: K8s 1.30 공식 테스트 대상
- K8s 1.30은 모든 의존 컴포넌트의 교집합 최소 버전

**영향:**
- CRD API 버전: `apiextensions/v1` (이미 사용 중, 영향 없음)
- Strimzi CRD: `v1` API (0.49부터 도입, v1alpha1/v1beta1 deprecated)
- kube-prometheus-stack: 최신 버전 사용 가능 (K8s 1.19+ 호환)

### D10. Spring Boot 2.7.x → 3.4.x 마이그레이션

**결정:** Consumer/Producer 앱을 Spring Boot 2.7.18에서 **3.4.x**로 업그레이드한다.

**근거:**
- Spring Boot 3.x는 Java 17 필수 → kafka-clients 4.x 최소 요구 (Java 11+) 충족
- Spring Kafka 3.3.x 자동 관리 → kafka-clients 4.1.x override로 KIP-848 활용
- Jakarta EE 10 (`javax.*` → `jakarta.*`) 마이그레이션 필요하나, 테스트 앱 규모에서 부담 적음
- Micrometer 1.13+ → Prometheus 3.x 호환 메트릭

**마이그레이션 핵심 변경:**
1. `javax.servlet.*` → `jakarta.servlet.*`
2. `javax.validation.*` → `jakarta.validation.*`
3. Java 17 컴파일 타겟
4. `kafka.version` property override to 4.1.x

---

## 결정 요약표

| ID | 결정 | 영향 범위 | 접근법 |
|----|------|----------|--------|
| D1 | StatefulSet → Argo Rollouts | 전체 아키텍처 | 전체 |
| D2 | Static Membership 제거 + KIP-848 | Consumer 설정 | 전체 |
| D3 | STOPPED 상태 추가 | Consumer 앱 코드 | 단일 그룹 |
| D4 | prePromotionAnalysis로 전환 제어 | Argo 매니페스트 | A, C |
| D5 | Webhook Job 서비스 구현 | 신규 컴포넌트 | A, C |
| D6 | ConfigMap 키 재설계 | Sidecar, Controller | B |
| D7 | 파티션 수 8 유지 | Kafka 토픽 | 전체 |
| D8 | KIP-848 Consumer Group Protocol | Consumer 설정, 테스트 | 전체 |
| D9 | K8s v1.23 → v1.30 업그레이드 | 전체 인프라 | 전체 |
| D10 | Spring Boot 2.7 → 3.4 마이그레이션 | Consumer/Producer 앱 | 전체 |
