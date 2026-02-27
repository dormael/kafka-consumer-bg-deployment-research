# Kafka Consumer Blue-Green 배포 전략 리서치 통합 요약

> 4개 리서치 문서의 내용을 검증된 내용 중심으로 통합 정리한 문서.
> review.md에서 지적된 기술적 오류/불확실 내용은 제외하거나 정정하여 반영하였다.

---

## 1. 배경: 왜 Kafka Consumer에 Blue-Green 배포가 필요한가

Kafka Consumer는 데이터를 Pull 방식으로 가져오므로 단순한 네트워크 스위칭으로 배포를 제어할 수 없다. Consumer의 Blue-Green 스위칭은 파티션 소유권 이전과 리밸런싱이라는 복잡한 메커니즘을 제어해야 하는 과정이다.

### 롤링 업데이트 vs Blue-Green 배포

| 배포 지표 | 롤링 업데이트 (Rolling Update) | 블루-그린 배포 (Blue-Green) |
|---------|--------------------------|----------------------|
| 가용성 영향 | 리밸런싱 중 수 분간 저하 가능성 | pause/resume 기반 Atomic Switch 시 수 초 이내 전환 가능 (단, 같은 그룹 + Static Membership + Cooperative Sticky Assignor 전제) |
| 롤백 시간 | 이전 버전 재배포 및 재리밸런싱 (수 시간) | 즉각적인 트래픽 환경 전환 (수 분 이내) |
| 인프라 비용 | 상대적으로 낮음 | 일시적으로 2배의 자원 필요 |
| 운영 복잡도 | 표준 K8s 기능으로 가능 | 오케스트레이션 도구 및 패턴 필요 |

---

## 2. 핵심 메커니즘: pause()/resume()

Kafka Consumer의 `pause()`는 그룹 내 멤버 자격(Heartbeat)은 유지하면서 브로커로부터 새로운 데이터를 가져오는(Fetch) 행위만 일시 중단하는 기능이다.

### 동작 원리

- **리밸런싱 방지**: Consumer가 완전히 종료(close)되는 것이 아니므로, pause 상태에서도 브로커와 연결을 유지하여 불필요한 리밸런싱을 유발하지 않는다
- **즉각적인 롤백**: Green을 다시 pause하고 Blue를 resume하는 것만으로 즉시 롤백 가능
- **파티션 소유권 유지**: pause() 상태에서도 할당받은 파티션을 놓아주지 않는다. 같은 그룹 내에서 Blue를 pause한다고 해서 Green이 그 파티션을 자동으로 가져가지는 않는다

### 타임아웃 메커니즘 (정정 및 보강)

Kafka에는 두 가지 독립적인 타임아웃이 존재한다. 이 둘은 **KIP-62** (Kafka 0.10.1.0)에서 분리되었으며, 이전에는 단일 스레드에서 Heartbeat와 `poll()`을 모두 처리하여 처리 시간이 긴 경우 세션이 만료되는 문제가 있었다.

#### 2-스레드 모델 (KIP-62 이후)

```
Consumer Instance
├── Main Thread (Application Thread)
│   ├── poll() 호출, 레코드 처리, 오프셋 커밋
│   └── 감시 대상: max.poll.interval.ms
│
└── Heartbeat Thread (Background Thread)
    ├── heartbeat.interval.ms 간격으로 Heartbeat 전송
    ├── max.poll.interval.ms 초과 시 LeaveGroup 전송
    └── 감시 대상: session.timeout.ms
```

- **`session.timeout.ms`**: Heartbeat 스레드 기반. Heartbeat가 이 시간 내에 도착하지 않으면 브로커가 Consumer를 죽은 것으로 판단
- **`max.poll.interval.ms`**: `poll()` 호출 간격 기반. 이 시간 내에 다음 `poll()`이 호출되지 않으면 Heartbeat 스레드가 `LeaveGroup`을 전송하여 자발적으로 그룹을 탈퇴 (Livelock 방지)

#### 기본값 변경 이력

| 설정 | Kafka 0.10.1~2.8 | Kafka 3.0+ | 변경 근거 |
|------|-----------------|-----------|---------|
| `session.timeout.ms` | 10,000ms (10초) | **45,000ms (45초)** | KIP-735: 클라우드 환경에서 일시적 네트워크 장애에 의한 잦은 오탐 방지. `request.timeout.ms`(30초)보다 짧아 재연결 전에 세션이 만료되는 문제 해결 |
| `max.poll.interval.ms` | 300,000ms (5분) | 300,000ms (5분) | 변경 없음 |
| `heartbeat.interval.ms` | 3,000ms (3초) | 3,000ms (3초) | `session.timeout.ms`의 1/3 이하 권장 |

#### Spring Kafka의 pause 시 내부 동작 (상세)

Spring Kafka의 pause 메커니즘에서는 내부적으로 `poll()`을 계속 호출하되 빈 결과를 반환하므로, **두 타임아웃 모두 문제가 되지 않는다**. 구체적인 동작:

1. `container.pause()` 호출 시 내부 플래그만 설정 (즉시 반영 아님)
2. 다음 루프 반복에서 `consumer.pause(allAssignedPartitions)` 호출 — 모든 할당 파티션에 대해 Fetch 중단
3. **`consumer.poll(100ms)`를 100ms 간격으로 계속 호출** (정상 시 5,000ms). 짧은 타임아웃으로 CPU 과부하 없이 빠른 응답성 확보
4. 각 `poll()` 호출마다 `max.poll.interval.ms` 타이머 리셋 — 리밸런싱 트리거 방지
5. Heartbeat 스레드는 독립적으로 계속 Heartbeat 전송 — `session.timeout.ms` 만료 방지
6. 리밸런싱 발생 시 `consumer.pause()` 상태가 유실될 수 있으므로, Spring Kafka가 리밸런스 리스너에서 pause 상태를 재적용

| 타임아웃 | pause 시 유지 방법 | 담당 스레드 |
|---------|-----------------|----------|
| `session.timeout.ms` | Background Heartbeat 스레드가 독립적으로 계속 전송 | Heartbeat 스레드 |
| `max.poll.interval.ms` | `consumer.poll(100ms)`를 ~100ms 간격으로 계속 호출하여 타이머 리셋 | Main/Consumer 스레드 |

타임아웃 우려는 직접 KafkaConsumer API를 사용하면서 `poll()` 루프를 멈추는 경우에만 해당된다.

#### Spring Kafka 버전별 pause/resume 기능

| Spring Kafka 버전 | 기능 |
|------------------|------|
| 2.1.3 | `pause()` / `resume()` 메소드 도입 |
| 2.1.5 | `isPauseRequested()`, `isConsumerPaused()`, `ConsumerPausedEvent` 추가 |
| 2.7 | 파티션 단위 `pausePartition()` / `resumePartition()` 추가 |
| 2.9 | **`pauseImmediate`** 속성 추가 — pause 시 현재 배치 전체가 아닌 현재 레코드 처리 후 즉시 중단 |

> **본 프로젝트 참고**: Spring Boot 2.7.18 (Spring Kafka 2.8.x) 사용 중이므로 `pauseImmediate`는 미사용. Atomic Switch에서 현재 레코드 단위 즉시 pause가 필요한 경우 Spring Kafka 2.9.x로 버전 오버라이드 필요.

---

## 3. 컨슈머 그룹 전략

### 전략 A: 단일 그룹 내 공존

Blue와 Green이 같은 `group.id`를 사용한다. Green은 시작 시 pause 상태로 대기한다.

**핵심 주의사항**: Green Consumer가 같은 그룹에 Join하는 순간 **반드시 리밸런싱이 발생**한다 (pause 상태와 무관). 리밸런싱 결과 일부 파티션이 paused된 Green Consumer에 할당될 수 있으며, 이 경우 해당 파티션의 메시지 처리가 일시 중단된다. Cooperative Sticky Assignor를 사용해도 새 멤버 가입에 의한 리밸런싱 자체는 피할 수 없다.

이를 해결하기 위해:
- Consumer 기본 PAUSED 시작 설계로 Pod 재시작 시 Dual-Active 원천 차단
- PauseAwareRebalanceListener로 리밸런싱 후 pause 상태 재적용
- Static Membership으로 리밸런싱 빈도 최소화

### 전략 B: 개별 그룹 전환

Blue와 Green이 서로 다른 `group.id`를 사용한다.

- 한쪽을 resume할 때 다른 쪽을 반드시 pause해야 중복 처리 방지
- 오프셋 관리를 별도로 동기화해야 할 수 있음

---

## 4. 구조적 파티션 설계

### 파티션 오버프로비저닝과 2배수 규칙

Blue/Green 동시 구동 시나리오에서 Green 환경의 Consumer가 활성화되었을 때, 모든 파티션이 이미 Blue 환경에 할당되어 있다면 Green Consumer는 대기 상태에 머물게 된다. 이를 해결하기 위해:

> **토픽의 파티션 수 >= 활성 Consumer 수의 최소 2배 이상**으로 유지해야 한다.

이 설계를 통해 Blue와 Green 환경이 동시에 구동될 때, 리밸런싱을 통해 각 환경의 Consumer들에게 최소 하나 이상의 파티션을 골고루 할당할 수 있도록 보장한다.

### 리밸런싱 프로토콜 비교: Cooperative Sticky Assignor

Kafka 2.4에서 KIP-429로 도입된 Cooperative Sticky Assignor는 Blue-Green 배포의 효율성을 극대화한다.

| 할당 전략 | 동작 방식 | Blue-Green 환경에서의 이점 |
|--------|--------|---------------------|
| Range / Round-robin | 전체 중단 후 재할당 (Eager) | 구현이 단순하나 스위칭 시 지연 발생 |
| Sticky Assignor | 기존 할당 유지 시도 (Eager) | 파티션 이동 최소화, 여전히 전체 중단 필요 |
| Cooperative Sticky | 필요한 파티션만 중단 (Cooperative) | 중단 없는 파티션 소유권 이전 가능 |

Cooperative 방식은 소유권이 이전되지 않는 파티션의 처리를 유지하면서 점진적으로 할당을 조정한다. 이는 Blue-Green 스위칭 시 발생하는 처리 공백을 최소화하여 시스템 전체의 처리량(Throughput)을 안정적으로 유지하는 데 기여한다.

#### Cooperative Sticky Assignor의 2-라운드 리밸런싱 동작 (KIP-429 상세)

Eager 방식은 모든 Consumer가 모든 파티션을 반납한 뒤 재할당하는 "stop-the-world" 방식이다. Cooperative 방식은 파티션 이동이 필요한 것만 2라운드에 걸쳐 점진적으로 처리한다:

**Round 1 — 탐색 및 반납:**
1. 리밸런싱 트리거 (새 멤버 가입, 멤버 탈퇴 등)
2. 모든 Consumer가 **현재 소유 파티션을 유지한 채** `JoinGroup` 요청 전송 (Eager와의 핵심 차이)
3. Leader가 현재 소유자와 새 할당 대상을 비교:
   - 소유자 변경 불필요 → 즉시 재할당
   - 소유자가 없는 파티션 → 즉시 새 소유자에게 할당
   - **소유자 변경 필요** → 이번 라운드에서는 할당에서 **제외** (아직 새 소유자에게 할당하지 않음)
4. 기존 소유자가 해당 파티션이 할당에서 빠진 것을 확인하고 `onPartitionsRevoked()` 호출 → 반납

**Round 2 — 재할당:**
5. 반납된 파티션이 이제 소유자 없음 상태
6. Leader가 해당 파티션을 의도된 새 소유자에게 할당
7. 새 소유자가 `onPartitionsAssigned()` 콜백 수신 → 소비 시작

**핵심 불변성**: 어떤 시점에서도 하나의 파티션에 두 명의 동시 소유자가 존재하지 않는다.

**성능 차이** (Confluent 측정 기준):
- Eager 방식 총 일시 정지 시간: **~37,138ms**
- Cooperative 방식 총 일시 정지 시간: **~3,522ms** (약 10배 개선)

#### Static Membership (KIP-345, Kafka 2.3)

`group.instance.id`를 설정하면 Consumer가 "정적 멤버"가 되어 리밸런싱 동작이 근본적으로 달라진다:

| 이벤트 | 동적 멤버 (기본) | 정적 멤버 (`group.instance.id` 설정) |
|-------|-------------|-------------------------------|
| 정상 종료 | `LeaveGroup` 전송 → 즉시 리밸런싱 | `LeaveGroup` 미전송 → **리밸런싱 없음** |
| 비정상 종료 (크래시) | `session.timeout.ms` 후 리밸런싱 | 동일: `session.timeout.ms` 후 리밸런싱 |
| 재시작 (`session.timeout.ms` 이내) | 새 `member.id` → 리밸런싱 | `group.instance.id`로 인식 → **리밸런싱 없음**, 캐시된 할당 반환 |
| 재시작 (`session.timeout.ms` 초과) | 새 `member.id` → 리밸런싱 | 이미 세션 만료 → 리밸런싱 |

**Fencing 메커니즘**: 같은 `group.instance.id`를 가진 두 프로세스가 동시 접속 시, 기존 멤버가 `FencedInstanceIdException`을 수신하여 종료된다. 이는 Dual-Active를 방지하는 안전장치이다.

**StatefulSet과의 시너지**: 본 프로젝트에서 `group.instance.id: ${HOSTNAME}`을 사용하며, StatefulSet의 안정적인 Pod 이름(`consumer-blue-0`, `consumer-green-0` 등) 덕분에 재시작 시 동일 ID가 보장된다. Deployment/Argo Rollouts로 전환 시 Pod 이름이 랜덤이므로 Static Membership은 **호환 불가**하며, CooperativeStickyAssignor + PauseAwareRebalanceListener에 의존해야 한다.

#### KIP-848: 차세대 Consumer Rebalance Protocol (Kafka 4.0)

Kafka 4.0에서 GA 예정인 KIP-848은 파티션 할당 로직을 **서버 사이드**로 이동시킨다. 클라이언트 측 Assignor가 불필요해지며, 기본적으로 완전한 Incremental Rebalancing이 적용된다. 현재 프로젝트의 K8s v1.23.8 환경에서는 해당 없으나, 향후 마이그레이션 시 고려할 사항이다.

---

## 5. 제어 패턴

### 패턴 A: 애플리케이션 내 REST 엔드포인트

Spring Kafka의 `MessageListenerContainer` 인터페이스를 통해 Consumer 스레드를 안전하게 일시 정지하거나 재개할 수 있다. `KafkaListenerEndpointRegistry`를 통해 `MessageListenerContainer`를 직접 pause/resume한다.

> **주의 (정정)**: `/actuator/bindings` 엔드포인트는 **Spring Cloud Stream** (`spring-cloud-stream` + `spring-cloud-stream-binder-kafka`) 의존성이 있을 때만 제공된다. 순수 Spring Kafka(`spring-kafka`) 환경에서는 이 엔드포인트가 존재하지 않으므로, 자체 lifecycle 엔드포인트를 구현해야 한다.

#### `/actuator/bindings` vs 자체 엔드포인트 비교 (보강)

| 항목 | `/actuator/bindings` (Spring Cloud Stream) | 자체 `/lifecycle/*` 엔드포인트 (Spring Kafka) |
|------|-------------------------------------------|---------------------------------------------|
| 의존성 | `spring-cloud-stream` + binder + actuator | `spring-kafka` + `spring-boot-starter-web` |
| 제어 대상 | Binding 이름 기반 (`<function>-in-0`) | Listener ID 기반 (`bgTestConsumerListener`) |
| 지원 상태 | STARTED, STOPPED, PAUSED, RESUMED | pause(), resume(), stop(), start() |
| PAUSED 지원 | Kafka 및 Solace binder만 지원 (Kafka Streams binder는 미지원) | 직접 구현이므로 제약 없음 |
| 프로그래밍 API | `BindingsLifecycleController` (Spring Cloud Stream 3.1+) | `KafkaListenerEndpointRegistry` |

**`pause()` vs `stop()`의 핵심 차이:**
- **`pause()`**: Container가 `poll()`을 계속 호출하되 빈 결과 반환 → **리밸런싱 방지** (Blue-Green 배포에 적합)
- **`stop()`**: Container와 Consumer를 완전히 종료 → **리밸런싱 트리거** (Blue-Green에 부적합)

### 패턴 B: Redis Pub/Sub을 이용한 다수 인스턴스 동시 제어

Kubernetes 환경에서 여러 Consumer 파드에 동시에 신호를 전달하기 위해 Redis를 메시지 브로커로 활용하는 패턴이다.

- 제어 로직이 Redis의 특정 채널에 '전환' 메시지를 발행
- 모든 Consumer 파드가 이를 구독(Subscribe)하고 있다가 자신의 내부 Consumer 객체에서 `pause()` 또는 `resume()`을 호출
- 단일 REST 호출이 특정 파드 하나만 제어하는 한계를 극복

### 패턴 C: Sidecar 또는 ConfigMap 감시

사이드카 컨테이너가 ConfigMap의 상태 값을 감시하다가, 특정 플래그가 변경되면 메인 컨테이너(Consumer)에 시그널을 보내거나 로컬 통신을 통해 상태를 변경하도록 유도한다.

---

## 6. 오케스트레이션 도구

### Argo Rollouts — 지표 기반 전환

Argo Rollouts는 Rollout이라는 커스텀 리소스를 통해 Blue-Green 및 Canary 배포를 관리한다.

- Prometheus와 같은 모니터링 시스템과 연동하여 Consumer의 실제 건강 상태를 분석
- 핵심 제어 메커니즘은 **AnalysisTemplate**: Green 환경이 배포된 후 `AnalysisRun`을 통해 특정 Consumer Group의 `records-lag-max` 지표가 임계치 이하로 유지되는지를 일정 시간 동안 관찰
- 분석 결과가 실패로 판단되면 즉시 롤백 수행
- Service 리소스를 조작하여 트래픽을 제어하는 것이 아니라, ReplicaSet의 규모를 조정함으로써 Consumer의 활성화 여부를 관리

**Consumer Lag 수식:**
```
Lag = LogEndOffset - CommittedOffset
```

### Flagger — 웹후크를 이용한 정밀 제어

Flagger는 Argo Rollouts와 유사하게 점진적 배포를 지원하지만, 특히 웹후크(Webhook)를 통한 확장성에서 강점을 보인다.

- **confirm-rollout**: 배포 시작 전 외부 승인 시스템이나 환경 준비 상태를 확인
- **pre-rollout**: Green Consumer가 시작되기 전, 기존의 Blue Consumer를 일시 정지(Pause)시키거나 사전 데이터 동기화를 수행하는 웹후크를 실행
- **post-rollout**: 배포가 성공적으로 완료된 후 Blue 리소스를 정리하거나 배포 결과를 Slack 등에 통지

Flagger는 서비스 메시(Istio, Linkerd 등)와 통합되어 동작할 때 가장 강력하지만, Kafka Consumer와 같이 L4/L7 트래픽 제어가 필요 없는 작업자 형태의 앱에 대해서는 Kubernetes 표준 기능을 활용한 Blue-Green 배포 모델을 적용할 수 있다.

### KEDA — 이벤트 기반 자동 스위칭 및 스케일링

Kubernetes Event-Driven Autoscaling(KEDA)은 Kafka의 Lag 지표를 기반으로 Consumer의 복제본(Replica) 수를 0에서 N까지 동적으로 조절할 수 있는 도구이다.

| KEDA 설정 파라미터 | 역할 및 의미 |
|-------------|---------|
| lagThreshold | 스케일 아웃을 트리거하기 위한 파티션당 평균 Lag 임계치 (기본값: 10) |
| minReplicaCount | Blue-Green 전환 시 초기 자원 할당량 조절 (0 설정 시 대기 상태) |
| maxReplicaCount | 파티션 수를 초과하지 않도록 설정하여 유휴 Consumer 발생 방지 |
| offsetResetPolicy | 신규 Consumer의 시작 위치 결정 (earliest/latest) |

#### 스케일링을 통한 'Drain' 패턴

Blue 환경에서 Green 환경으로의 전환 시, KEDA를 활용하여 Blue 환경의 처리를 점진적으로 줄이는 패턴:

1. Green 환경이 활성화되어 처리를 분담하기 시작
2. Blue 환경의 Lag이 줄어듦에 따라 KEDA는 설정된 임계치에 따라 Blue Consumer의 복제본 수를 줄여나감
3. 최종적으로 Blue 환경의 처리가 완료되어 Lag이 0이 되면 Blue Consumer는 0으로 스케일 인(Scale-in)되어 사실상 배포가 완료

이 방식은 강제적인 종료보다 훨씬 부드러운 전환을 보장한다.

---

## 7. Pause/Resume과 Graceful Shutdown

### Pause/Resume 메소드와 외부 상태 제어

대부분의 Kafka 클라이언트 라이브러리(Java, Node.js, Spring Kafka 등)는 Consumer의 `pause()`와 `resume()` 메소드를 제공한다. 이 메소드를 호출하면 Consumer는 Kafka 브로커와의 세션을 유지하며 Heartbeat를 계속 전송하지만, 새로운 데이터를 가져오지(Fetch) 않는다.

Blue-Green 배포 시, Blue 환경의 모든 Consumer 인스턴스에 외부 신호(예: Redis의 특정 키 변경, ConfigMap 업데이트, 전용 REST 엔드포인트 호출)를 보내 `pause()` 상태로 전환함으로써 파티션 소유권을 유지하되 데이터 처리는 중단할 수 있다.

### Kubernetes Lifecycle Hook과 Graceful Shutdown

Kubernetes의 preStop 후크와 SIGTERM 시그널 처리는 Kafka Consumer의 안전한 스위칭을 위한 마지막 방어선이다.

- Pod가 종료될 때 Consumer가 명시적으로 `close()`를 호출하지 않으면, 브로커는 `session.timeout.ms`가 지날 때까지 해당 Consumer가 살아있다고 판단하여 리밸런싱을 유도하지 않는다. 이는 Green 환경으로의 전환 지연과 Lag 발생의 원인이 된다
- 애플리케이션은 SIGTERM을 수신하면 현재 처리 중인 메시지 배치를 완료하고, 오프셋(Offset)을 커밋한 뒤, Kafka 그룹에서 명시적으로 탈퇴해야 한다
- preStop 후크에 일정 시간의 유예(Sleep)를 두어 인그레스 등 네트워크 계층의 정리가 완료된 후 Kafka 종료 절차를 밟도록 하는 것도 권장되는 패턴이다

---

## 8. 상태 기반 애플리케이션(Kafka Streams)의 Blue-Green 배포 고려사항

Kafka Streams와 같이 로컬 상태 스토어(RocksDB 등)를 사용하는 애플리케이션은 Blue-Green 배포 시 상태의 일관성을 어떻게 유지할 것인가가 핵심이다.

### Application ID와 토폴로지 변경 관리

Kafka Streams의 `application.id`는 내부적으로 Consumer Group ID로 사용된다. Blue와 Green 환경이 동일한 `application.id`를 공유하면 두 환경은 하나의 거대한 클러스터처럼 동작하며 파티션과 상태를 공유하려 한다.

- **토폴로지 호환 배포**: 변경 사항이 경미하고 상태 스토어 구조가 동일한 경우, 동일한 `application.id`를 사용하여 Blue-Green 배포를 진행. 이때 Cooperative Rebalancing이 불필요한 재빌드를 방지
- **신규 Application ID 배포**: 토폴로지가 크게 변경된 경우, 새로운 `application.id`를 사용하여 Green 환경을 구축. Green 환경은 입력 토픽의 처음(earliest)부터 데이터를 재처리하여 상태를 재구축(Re-hydration)해야 하며, 이 과정에 필요한 시간과 자원을 배포 계획에 반영해야 한다

### 상태 스토어 명시적 명명

자동으로 생성되는 상태 스토어 이름은 토폴로지 구조에 따라 변할 수 있다. Blue-Green 배포 시 환경 간의 상태 전이를 원활하게 하기 위해서는 모든 상태 스토어와 중간 토픽에 대해 명시적 이름을 부여하는 것이 필수적이다. 이를 통해 새로운 버전의 애플리케이션이 기존의 체인지로그(Changelog) 토픽을 정확히 찾아 상태를 복구할 수 있게 된다.

---

## 9. 데이터 무결성 보장을 위한 멱등성 및 트랜잭션 관리 전략

Blue-Green 스위칭 과정에서 두 환경이 잠시 동안 동시에 동일한 파티션을 처리하거나, 전환 시점에 메시지가 중복 처리될 가능성이 상존한다.

### Consumer 멱등성 보장 패턴

가장 권장되는 방식은 소비자 측에서 멱등성(Idempotency)을 구현하는 것이다.

| 구현 방식 | 설명 | 장단점 |
|--------|-----|------|
| DB Unique Key | 처리 결과를 저장할 때 고유 키 제약 조건 활용 | 가장 확실하나 DB 부하 증가 가능성 |
| Redis SETNX | Redis를 이용해 메시지 처리 여부를 원자적으로 체크 | 속도가 빠르나 Redis 가용성에 의존 |
| Kafka 트랜잭션 | 'Consume-Transform-Produce' 흐름을 단일 트랜잭션으로 묶음 | Kafka 생태계 내에서 완결되나 구현 복잡도 높음 |

### Kafka 트랜잭션 API의 범위 (정정 및 보강)

Kafka 트랜잭션(KIP-98, Kafka 0.11.0)은 **Consume-Transform-Produce** 패턴 내에서의 exactly-once를 보장한다. 구체적으로 단일 트랜잭션이 원자적으로 커밋하는 대상은:

1. 하나 이상의 Kafka 토픽-파티션에 대한 **출력 메시지**
2. `sendOffsetsToTransaction()`을 통한 **Consumer 오프셋 커밋** (`__consumer_offsets` 토픽)
3. Kafka Streams의 경우 **상태 스토어 changelog 업데이트**

이 세 가지가 ALL-or-NOTHING으로 반영된다. 그러나 이 범위를 벗어나는 외부 시스템에 대해서는 보장하지 않는다.

#### `read_committed` 동작과 Last Stable Offset (LSO)

`isolation.level=read_committed` Consumer는 커밋된 트랜잭션의 메시지만 읽는다. 이때 사용되는 오프셋 경계는 High Watermark가 아닌 **Last Stable Offset (LSO)**이다:

```
LSO = min(High Watermark, 진행 중인 모든 트랜잭션의 최저 오프셋)
```

이는 **Head-of-Line Blocking** 효과를 유발한다: 하나의 느린/멈춘 트랜잭션이 해당 파티션의 모든 `read_committed` Consumer의 진행을 차단할 수 있다. 장시간 실행 트랜잭션은 출력 가용성 지연과 end-to-end 레이턴시 증가의 원인이 된다.

#### Blue-Green 전환 시 3가지 실패 윈도우

Blue→Green 전환에서 Kafka 트랜잭션이 외부 시스템 중복을 방지하지 못하는 구체적 시나리오:

**윈도우 1: DB 쓰기 실패, Kafka 오프셋 미커밋**
→ 메시지 재전달. 문제 없음 (at-least-once).

**윈도우 2 (핵심 문제): DB 쓰기 성공, 오프셋 커밋 전 Blue 크래시**
→ Kafka는 메시지가 처리되지 않은 것으로 간주. Green이 동일 메시지를 재처리하면 **DB에 중복 발생**. Kafka 트랜잭션은 이를 방지할 수 없다 — DB 쓰기가 트랜잭션 경계 밖이기 때문.

**윈도우 3: DB에 일시적 오류로 롤백, Kafka 오프셋은 커밋됨**
→ Kafka는 처리 완료로 간주하나 DB에는 반영 안 됨. **데이터 유실**.

#### `transactional.id`와 Zombie Fencing

`transactional.id`는 Producer 재시작 시 동일 ID로 `initTransactions()`를 호출하면 epoch가 증가하고, 이전 epoch의 Producer는 `ProducerFencedException`을 수신하여 종료된다. 이는 zombie Producer를 방지한다.

**Blue-Green에서의 주의점**: Blue와 Green이 **다른** `transactional.id`를 사용하면 fencing이 **동작하지 않는다**. 각각 독립적 Producer로 취급되어 동일 입력에 대해 양쪽 모두 출력을 커밋할 수 있다. Kafka 2.5+ (KIP-447) 이후에는 `sendOffsetsToTransaction()`에 Consumer Group 메타데이터를 포함하여 그룹 레벨 fencing이 가능하다.

#### 외부 시스템과의 Exactly-Once를 위한 대안 패턴

| 패턴 | 동작 원리 | 보장 수준 |
|------|---------|---------|
| **Transactional Outbox** | 비즈니스 데이터와 이벤트를 동일 DB 트랜잭션으로 기록. 별도 릴레이가 outbox 테이블에서 Kafka로 발행 | DB→Kafka: at-least-once. 소비자 측 멱등성 필요 |
| **CDC (Debezium)** | DB 트랜잭션 로그(WAL/binlog)를 직접 캡처하여 Kafka로 전달. Outbox 테이블의 INSERT를 로그에서 캡처 | Outbox와 동일. 폴링 지연 없이 준실시간 |
| **DB에 Kafka Offset 저장** | Kafka의 `__consumer_offsets` 대신 비즈니스 DB에 오프셋을 함께 저장. 재시작 시 DB에서 오프셋 읽어 seek | 단일 DB 트랜잭션으로 effectively-once 달성 |
| **멱등 Consumer** | 처리 여부를 별도 테이블(`processed_events`)에 기록. 재처리 시 중복 체크 | effectively-once (가장 실용적) |

---

## 10. 실시간 지표 분석 기반의 자동화된 롤백 체계

### 핵심 모니터링 지표

| 지표 | 설명 |
|----|-----|
| Consumer Lag (by Group/Partition) | Green 환경의 Consumer가 데이터를 충분히 빠르게 처리하고 있는지 측정 |
| Processing Latency | 개별 메시지 처리 시간이 이전 버전 대비 악화되었는지 확인 |
| Error/Exception Rate | Green 환경에서 발생하는 예외 빈도를 모니터링 |
| Metadata Refresh Latency | 클라이언트가 브로커로부터 메타데이터를 가져오는 데 걸리는 시간을 측정하여 네트워크 이슈를 감지 |

이러한 지표를 수집하기 위해 Kafka Exporter, JMX Exporter, 또는 LinkedIn에서 개발한 Burrow와 같은 도구가 널리 사용된다. 특히 Burrow는 고정된 임계치가 아닌 **슬라이딩 윈도우 방식**으로 Consumer의 건강 상태를 평가하여 보다 정확한 배포 분석을 지원한다.

#### Burrow 슬라이딩 윈도우 알고리즘 (보강)

Burrow(GitHub Stars ~3,900, v1.9.5 기준 활발히 유지보수 중)는 파티션별로 최근 N개의 오프셋 커밋을 저장하는 슬라이딩 윈도우(기본 10개, 60초 커밋 간격 기준 ~10분)를 유지하며, 다음 5가지 규칙을 순차 적용한다:

| 규칙 | 상태 | 조건 | 의미 |
|------|------|------|------|
| 1 | **OK** | 현재 Lag ≤ 허용치 (기본 0) | 건강한 상태 |
| 2 | **Rewind** | 윈도우 내 오프셋이 감소하고 미복구 | 오프셋 리셋 버그, 잘못된 재처리 감지 |
| 3 | **Stop** | 마지막 커밋 이후 경과 시간 > 윈도우 전체 시간 | Consumer가 오프셋 커밋을 중단 |
| 4 | **Stall** | 윈도우 내 모든 연속 오프셋이 동일 | 살아있으나 진행하지 못함 (stuck) |
| 5 | **Warning** | 연속 오프셋 쌍에서 Lag 증가 또는 유지 | 처리 중이나 뒤처지는 중 |

그룹 레벨에서는 "최악 상태 우선" 원칙으로 집계되며, Stop/Stall/Rewind는 모두 **Error**로 롤업된다.

#### 모니터링 도구 비교표 (보강)

| 도구 | 방식 | 장점 | 한계 | 시간 기반 Lag |
|------|------|------|------|-------------|
| **Burrow** (LinkedIn) | 행동 분석 (임계치 없음) | 오탐 적음, 자동 건강 평가, 설정 최소화 | 제한된 시각화, 단기 이력만 보관 | 미지원 (오프셋 기반) |
| **kafka_exporter** (danielqsj) | Prometheus 메트릭 노출 | 경량 Go 바이너리, JVM 불필요, SASL 지원 | 오프셋 기반 Lag만 제공 | 미지원 |
| **kafka-lag-exporter** (seglo) | Prometheus + 시간 추정 | `lag_seconds` 제공, Strimzi 연동 | **2024.3 Archived** (softwaremill/klag-exporter로 대체 권장) | **지원** |
| **JMX Exporter** | JVM MBean 노출 | `records-lag-max` 등 상세 클라이언트 지표 | 동일 JVM 내 Consumer만 모니터링 가능 | 미지원 |
| **KEDA Kafka Scaler** | 스케일링 신호 | Consumer 자동 스케일링 | 모니터링 전용 아님, 건강 평가 로직 없음 | 미지원 |

#### 시간 기반 Lag: 새로운 모니터링 패러다임

오프셋 기반 Lag의 근본적 한계: 1,000만 오프셋의 Lag이 30초 지연인지 3시간 지연인지 알 수 없다 (처리량에 따라 다름). **시간 기반 Lag**은 "현재 Consumer가 처리 중인 오프셋이 최신이었던 시각"과 현재 시각의 차이를 측정하여, 처리량에 무관하게 일관된 단일 임계치(예: "2분 이상 뒤처지면 경고")로 모든 Consumer 그룹을 모니터링할 수 있다.

### 자동화된 롤백 시나리오

Argo Rollouts나 Flagger와 같은 도구는 분석 결과가 실패로 판단되면 즉시 롤백을 수행한다. Kafka Consumer 관점에서의 롤백 동작:

1. Green 환경의 Deployment/ReplicaSet 규모를 즉시 0으로 축소한다
2. 종료 과정에서 Green Consumer가 명시적으로 그룹을 탈퇴하도록 유도한다
3. Blue 환경의 규모를 원상복구하거나, 일시 정지(Pause) 상태였다면 `resume()`을 호출하여 처리를 재개한다
4. 리밸런싱을 통해 파티션 소유권이 Blue로 다시 이전되며, 중단되었던 지점부터 처리가 재개된다

---

## 11. 활용 가능한 오픈소스 도구 정리

| 카테고리 | 도구 | 역할 |
|--------|-----|-----|
| 배포 오케스트레이션 | Argo Rollouts | AnalysisTemplate 기반 지표 분석 및 자동 롤백 |
| 배포 오케스트레이션 | Flagger | Webhook 기반 정밀 배포 제어 (pre/post-rollout) |
| Kafka 오퍼레이터 | Strimzi | K8s에서 Kafka 운영, KafkaExporter 내장 |
| 이벤트 기반 스케일링 | KEDA | Kafka Lag 기반 Consumer 자동 스케일링 |
| 모니터링 | Burrow | 슬라이딩 윈도우 기반 Consumer Lag 분석 |
| 모니터링 | Prometheus + Kafka Exporter | Consumer Lag, 처리 지연 등 지표 수집 |
| 애플리케이션 프레임워크 | Spring Kafka | MessageListenerContainer를 통한 pause/resume |

### 권장 조합

```
Argo Rollouts/Flagger (워크플로우 제어)
  + Spring Kafka (Consumer pause/resume 실행)
  + Prometheus/Burrow (상태 검증)
```

---

## 12. 실무 구현 사례

### 우아한형제들(배달의민족)

가장 대표적인 국내 사례로, Kafka Consumer의 pause와 resume을 활용한 무중단 배포 전략을 공유하였다.

- **핵심 로직**: Consumer를 완전히 종료하지 않고 `MessageListenerContainer`의 `pause()` 메서드를 호출하여 메시지 폴링만 일시 정지. 이를 통해 Consumer 그룹의 멤버 자격을 유지하면서 리밸런싱을 방지
- **이점**: 외부 시스템(DB 등)의 부하를 고려하여 Consumer 수를 동적으로 조절하거나, 배포 시점에 파티션 소유권을 유지한 채로 안전하게 전환

### Flagger/Argo Rollouts 연동

- **Flagger**: pre-rollout 웹후크에서 Green 환경을 검증하고, 성공 시 Blue 환경의 Consumer를 정지시키는 스크립트를 실행
- **Argo Rollouts**: AnalysisTemplate을 통해 Consumer Lag 지표를 Prometheus로 관찰하며, 배포 단계 사이사이에 AnalysisRun이 웹후크를 호출하여 Consumer 상태를 제어

---

## 부록 A: 제외 및 정정 내용

review.md의 검증 결과에 따라 다음 내용은 본 요약에서 제외하거나 정정하였다:

1. **`/actuator/bindings` 무조건 사용 가능 설명** → Spring Cloud Stream 의존성 필요 명시로 정정. `spring-cloud-stream` + binder + actuator 세 가지 모두 필요. 순수 `spring-kafka`에서는 `KafkaListenerEndpointRegistry`를 통한 자체 엔드포인트 구현이 올바른 접근
2. **`max.poll.interval.ms`와 Heartbeat 혼동** → KIP-62 (Kafka 0.10.1)의 2-스레드 모델로 정확하게 분리 기술. Spring Kafka는 pause 시에도 `poll(100ms)`를 계속 호출하여 양 타임아웃 모두 방지
3. **단일 그룹 내 공존 시 리밸런싱 문제 과소 설명** → KIP-429의 2-라운드 리밸런싱 동작을 상세 기술. Cooperative 방식이라도 새 멤버 가입 시 리밸런싱 자체는 불가피하며, 영향 범위만 최소화됨
4. **Kafka 트랜잭션 API의 Blue-Green 적용 과대 설명** → KIP-98의 정확한 보장 범위(Kafka 내부 원자성), 3가지 실패 윈도우, `transactional.id` fencing의 한계, 외부 시스템 대안 패턴(Outbox, CDC, 멱등 Consumer) 보강
5. **"수 초 이내의 전환 완료" 무조건적 주장** → pause/resume 기반 + Static Membership + Cooperative Sticky Assignor 전제 조건 명시. Eager 방식에서는 37초 이상, Cooperative에서도 3.5초 수준의 일시 정지 발생 가능
6. **kconsumer-group-operator** → 추가 리서치 결과 실존하는 프로젝트 (GitHub: `thanhnamit/kconsumer-group-operator`, 2020년 생성)이나, Star 1개의 개인 데모 프로젝트이며 커뮤니티 채택 없음. 도구 목록에서는 제외 유지
7. **출처 불명확한 레퍼런스** (빈 백틱 등) → 해당 인용 제거

---

## 부록 B: 추가 리서치 참고 자료

본 요약의 보강 내용은 다음 1차 자료에 기반한다:

### Kafka KIP 문서
- [KIP-62: Allow consumer to send heartbeats from a background thread](https://cwiki.apache.org/confluence/display/KAFKA/KIP-62) — Kafka 0.10.1, 2-스레드 모델 도입
- [KIP-345: Introduce static membership protocol](https://cwiki.apache.org/confluence/display/KAFKA/KIP-345) — Kafka 2.3, `group.instance.id`
- [KIP-429: Kafka Consumer Incremental Rebalance Protocol](https://cwiki.apache.org/confluence/display/KAFKA/KIP-429) — Kafka 2.4, CooperativeStickyAssignor
- [KIP-447: Producer scalability for exactly once semantics](https://cwiki.apache.org/confluence/display/KAFKA/KIP-447) — Kafka 2.5, Consumer Group 레벨 fencing
- [KIP-735: Increase default consumer session timeout](https://cwiki.apache.org/confluence/display/KAFKA/KIP-735) — Kafka 3.0, `session.timeout.ms` 10초→45초
- [KIP-848: A New Consumer Rebalance Protocol](https://cwiki.apache.org/confluence/display/KAFKA/KIP-848) — Kafka 4.0, 서버 사이드 할당
- [KIP-98: Exactly Once Delivery and Transactional Messaging](https://cwiki.apache.org/confluence/display/KAFKA/KIP-98) — Kafka 0.11, 트랜잭션 API

### Spring 공식 문서
- [Spring Kafka: Pausing and Resuming Listener Containers](https://docs.spring.io/spring-kafka/reference/kafka/pause-resume.html)
- [Spring Cloud Stream: Binding visualization and control](https://docs.spring.io/spring-cloud-stream/reference/spring-cloud-stream/binding_visualization_control.html)

### Confluent 기술 블로그
- [Incremental Cooperative Rebalancing in Apache Kafka](https://www.confluent.io/blog/incremental-cooperative-rebalancing-in-kafka/) — Eager vs Cooperative 성능 비교
- [Dynamic vs. Static Consumer Membership](https://www.confluent.io/blog/dynamic-vs-static-kafka-consumer-rebalancing/)
- [Transactions in Apache Kafka](https://www.confluent.io/blog/transactions-apache-kafka/) — `transactional.id` fencing 상세
- [Exactly-Once Semantics Are Possible](https://www.confluent.io/blog/exactly-once-semantics-are-possible-heres-how-apache-kafka-does-it/)

### 모니터링
- [LinkedIn Engineering: Burrow — Kafka Consumer Monitoring Reinvented](https://engineering.linkedin.com/apache-kafka/burrow-kafka-consumer-monitoring-reinvented)
- [Burrow Consumer Lag Evaluation Rules](https://github.com/linkedin/Burrow/wiki/Consumer-Lag-Evaluation-Rules)
- [WarpStream: Stop Counting Messages, Start Measuring Time](https://www.warpstream.com/blog/the-kafka-metric-youre-not-using-stop-counting-messages-start-measuring-time) — 시간 기반 Lag 패러다임

### 패턴
- [Debezium: Reliable Microservices Data Exchange With the Outbox Pattern](https://debezium.io/blog/2019/02/19/reliable-microservices-data-exchange-with-the-outbox-pattern/)
- [Strimzi: Exactly-Once Semantics with Kafka Transactions](https://strimzi.io/blog/2023/05/03/kafka-transactions/)
