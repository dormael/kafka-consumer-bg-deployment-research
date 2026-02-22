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

### 타임아웃 메커니즘 (정정)

Kafka에는 두 가지 독립적인 타임아웃이 존재한다:

- **`session.timeout.ms`**: Heartbeat 스레드 기반. Heartbeat가 이 시간 내에 도착하지 않으면 브로커가 Consumer를 죽은 것으로 판단 (KIP-62 이후 별도 스레드로 분리)
- **`max.poll.interval.ms`**: `poll()` 호출 간격 기반. 이 시간 내에 다음 `poll()`이 호출되지 않으면 리밸런싱 트리거

Spring Kafka의 pause 메커니즘에서는 내부적으로 `poll()`을 계속 호출하되 빈 결과를 반환하므로, **두 타임아웃 모두 문제가 되지 않는다**. 타임아웃 우려는 직접 KafkaConsumer API를 사용하면서 `poll()` 루프를 멈추는 경우에만 해당된다.

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

---

## 5. 제어 패턴

### 패턴 A: 애플리케이션 내 REST 엔드포인트

Spring Kafka의 `MessageListenerContainer` 인터페이스를 통해 Consumer 스레드를 안전하게 일시 정지하거나 재개할 수 있다. `KafkaListenerEndpointRegistry`를 통해 `MessageListenerContainer`를 직접 pause/resume한다.

> **주의 (정정)**: `/actuator/bindings` 엔드포인트는 **Spring Cloud Stream** 의존성이 있을 때만 제공된다. 순수 Spring Kafka(`spring-kafka`) 환경에서는 이 엔드포인트가 존재하지 않으므로, 자체 lifecycle 엔드포인트를 구현해야 한다.

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

### Kafka 트랜잭션 API의 범위 (정정)

Kafka 트랜잭션은 **Consume-Transform-Produce** 패턴 내에서의 exactly-once를 보장한다. `isolation.level=read_committed`는 미확정 오프셋을 읽지 않게 해주는 것은 맞지만:

- Blue가 메시지를 소비하고 **외부 시스템(DB 등)에 이미 반영**한 후 오프셋 커밋 전에 중단된 경우, Green이 같은 메시지를 다시 처리하면 **외부 시스템 기준으로는 중복이 발생**한다
- Kafka 트랜잭션은 **Kafka 생태계 내부**(오프셋 + 출력 토픽)의 원자성만 보장하며, 외부 시스템과의 원자성은 **별도 멱등성 처리가 필요**하다

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

## 부록: 제외된 내용

review.md의 검증 결과에 따라 다음 내용은 본 요약에서 제외하거나 정정하였다:

1. **`/actuator/bindings` 무조건 사용 가능 설명** → Spring Cloud Stream 의존성 필요 명시로 정정
2. **`max.poll.interval.ms`와 Heartbeat 혼동** → 두 메커니즘 분리하여 정확하게 기술
3. **단일 그룹 내 공존 시 리밸런싱 문제 과소 설명** → 리밸런싱 반드시 발생함을 명시
4. **Kafka 트랜잭션 API의 Blue-Green 적용 과대 설명** → Kafka 내부 원자성만 보장, 외부 시스템 별도 필요 명시
5. **"수 초 이내의 전환 완료" 무조건적 주장** → pause/resume 기반 + Static Membership + Cooperative Sticky Assignor 전제 조건 명시
6. **kconsumer-group-operator** → 존재가 불확실한 프로젝트이므로 도구 목록에서 제외
7. **출처 불명확한 레퍼런스** (빈 백틱 등) → 해당 인용 제거
