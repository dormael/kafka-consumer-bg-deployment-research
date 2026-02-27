# Phase 02: Plan 문서 검토 결과 (Research Summary 기준)

> **작성일:** 2026-02-25
> **기준 문서:** `research/research-summary.md` (4개 리서치 문서의 검증된 통합 요약)
> **검토 대상:** `plan/` 디렉토리 전체 (14개 파일)
> **목적:** Research Summary에서 검증/정정된 내용이 Plan 문서에 올바르게 반영되었는지 확인하고, 불일치·오류·누락 항목을 식별

---

## 검토 요약

| 심각도 | 항목 수 | 설명 |
|--------|---------|------|
| **Critical** | 1 | CLAUDE.md의 Lifecycle State 코드값 오류 (코드와 불일치) |
| **High** | 3 | 메트릭 명명 불일치, heartbeat 설정 경계값, 전략 매핑 미명시 |
| **Medium** | 4 | 파티션 규칙 연관성 누락, Offset 동기화 무결성 미반영, pauseImmediate 미언급, Volume Mount 지연 수치 불일치 |
| **Low** | 2 | KIP-848 향후 고려 누락, Burrow 미채택 사유 미기록 |

---

## Critical

### C1. CLAUDE.md Lifecycle State 코드값 오류

**위치:** CLAUDE.md (Consumer HTTP Endpoints 섹션)

**현재 (CLAUDE.md):**
```
`/lifecycle/status` | GET | 현재 상태 (ACTIVE=0, PAUSED=1, DRAINING=2)
```

**실제 코드 (`LifecycleState.java:5-7`):**
```java
ACTIVE(0),
DRAINING(1),
PAUSED(2);
```

**task02.md (정확):**
```
bg_consumer_lifecycle_state | Gauge | 라이프사이클 상태 (0=ACTIVE, 1=DRAINING, 2=PAUSED)
```

**분석:**
CLAUDE.md가 PAUSED=1, DRAINING=2로 기술하고 있으나, 실제 코드와 task02.md는 DRAINING=1, PAUSED=2로 구현되어 있다. CLAUDE.md는 프로젝트 전체의 참조 문서이므로, 이 오류는 모니터링 대시보드 설정, Prometheus 쿼리 작성, 디버깅 시 혼란을 유발한다.

**수정 대상:** CLAUDE.md

**수정 내용:**
```
현재 상태 (ACTIVE=0, PAUSED=1, DRAINING=2)
→
현재 상태 (ACTIVE=0, DRAINING=1, PAUSED=2)
```

---

## High

### H1. Controller 메트릭명 불일치 (CLAUDE.md vs 실제 코드)

**위치:** CLAUDE.md (Metric Naming Convention 섹션) vs `apps/switch-controller/internal/metrics/metrics.go`

**CLAUDE.md:**
```
Switch Controller: bg_switch_duration_seconds, bg_switch_initiated_total, bg_switch_rollback_total
```

**실제 코드 (`metrics.go:27,31`):**
```go
Name: "bg_switch_total",          // CLAUDE.md에는 bg_switch_initiated_total
Name: "bg_switch_success_total",  // CLAUDE.md에 누락
```

**task03.md (정확):**
```
bg_switch_total, bg_switch_success_total, bg_switch_rollback_total, bg_switch_active_color, bg_switch_dual_active_detected
```

**분석:**
CLAUDE.md가 `bg_switch_initiated_total`이라고 기술하지만 실제 코드는 `bg_switch_total`이다. 또한 `bg_switch_success_total`이 CLAUDE.md에 누락되어 있다. task03.md의 기술이 코드와 일치한다.

추가로 CLAUDE.md는 "모든 커스텀 메트릭은 `bg_` 접두사를 사용한다"고 명시하지만, Sidecar 메트릭은 `sidecar_` 접두사를 사용한다 (`sidecar_lifecycle_commands_total`, `sidecar_current_state` 등). 이는 명명 규칙의 예외인데, CLAUDE.md에 이 예외가 언급되지 않았다.

**수정 대상:** CLAUDE.md

**수정 내용:**
```
Switch Controller: bg_switch_duration_seconds, bg_switch_initiated_total, bg_switch_rollback_total
→
Switch Controller: bg_switch_duration_seconds, bg_switch_total, bg_switch_success_total, bg_switch_rollback_total, bg_switch_active_color, bg_switch_dual_active_detected
```

Sidecar 메트릭 접두사에 대한 예외 설명도 추가 필요:
```
모든 커스텀 메트릭은 `bg_` 접두사를 사용한다:
→
Consumer, Producer, Switch Controller의 커스텀 메트릭은 `bg_` 접두사를 사용한다. Switch Sidecar는 `sidecar_` 접두사를 사용한다:
```

---

### H2. `heartbeat.interval.ms` 설정값 경계값 사용

**위치:** task02.md, `apps/consumer/src/main/resources/application.yaml:14`

**현재 설정:**
```yaml
session.timeout.ms: 45000
heartbeat.interval.ms: 15000
```

**Research Summary 근거 (섹션 2, 기본값 변경 이력 표):**
> `heartbeat.interval.ms` | 3,000ms (3초) | 3,000ms (3초) | **`session.timeout.ms`의 1/3 이하 권장**

**분석:**
15000ms는 `session.timeout.ms`(45000ms)의 정확히 1/3이다. "1/3 이하"의 기술적 범위 안에는 있으나, **경계값 사용**이다. 문제점:

1. **Kafka 공식 문서 권장**: `session.timeout.ms`의 1/3보다 낮게 설정하여 "세 번의 heartbeat 기회" 확보를 의도한 설계. 경계값에서는 네트워크 지연 1회로 세션 만료 위험 증가.
2. **기본값 대비 5배 높음**: Kafka 3.0+에서도 기본값은 3000ms. 15000ms로 설정하면 broker가 Consumer 장애를 감지하는 데 최대 45초(session.timeout.ms) 소요.
3. **Blue/Green 배포 시나리오 영향**: 빠른 장애 감지가 중요한 전환 시나리오에서, 느린 heartbeat는 Static Membership의 `session.timeout.ms` 기반 복구와 맞물려 장애 대응이 지연될 수 있다.

**수정 제안:**
```yaml
heartbeat.interval.ms: 15000
→
heartbeat.interval.ms: 10000   # session.timeout.ms(45s)의 ~1/4.5, 권장 범위 내
```

또는 기본값(3000ms)을 그대로 사용하여 빠른 장애 감지 이점을 유지할 수 있다. 다만, 이는 성능에 영향을 줄 수 있으므로 decisions.md에 설정 근거를 명시하는 것을 권장한다.

---

### H3. 전략 이름 매핑 미명시 (Plan vs Research)

**위치:** plan/plan.md, 전체 plan 문서

**Research Summary의 전략 구분:**
- **전략 A**: 단일 그룹 내 공존 (같은 `group.id`)
- **전략 B**: 개별 그룹 전환 (별도 `group.id`)

**Plan 문서의 전략 구분:**
- **전략 C**: Pause/Resume Atomic Switch (같은 Consumer Group + Static Membership)
- **전략 B**: 별도 Consumer Group + Offset 동기화
- **전략 E**: Kafka Connect REST API / Strimzi CRD 기반

**분석:**
Plan의 "전략 C"는 Research의 "전략 A" 패턴(단일 그룹 내 공존)에 해당하고, Plan의 "전략 B"는 Research의 "전략 B"(개별 그룹 전환)에 해당한다. 그러나 이 매핑이 어느 plan 문서에서도 명시되지 않았다. 원본 설계 문서(`kafka-consumer-bluegreen-design.md`)에서 정의된 것으로 보이나, plan.md의 "검증 대상 전략" 표에 Research와의 대응 관계를 추가하면 문서 간 일관성이 높아진다.

**수정 제안:** plan/plan.md의 전략 표에 매핑 컬럼 추가:
```
| 전략 | 설명 | Research 참조 | 우선순위 |
|------|------|---------------|----------|
| 전략 C | Pause/Resume Atomic Switch | Research 전략 A (단일 그룹 내 공존) + 제어 패턴 A,C | 1순위 |
| 전략 B | 별도 Consumer Group + Offset 동기화 | Research 전략 B (개별 그룹 전환) | 2순위 |
| 전략 E | Kafka Connect REST API | Research 오케스트레이션 도구 (Strimzi) | 3순위 |
```

---

## Medium

### M1. 전략 C 파티션 분할 문제와 Research 2배수 규칙의 연관성 미기술

**위치:** changes.md (전략 C 구조적 특성 섹션), task05.md

**Plan 문서의 기술 (changes.md):**
> 같은 Consumer Group에 6개 Consumer → 8개 파티션 분배
> PAUSED 측 파티션(3~4개)은 항상 미소비 → Lag 지속 누적

**Research Summary 근거 (섹션 4):**
> 토픽의 파티션 수 >= 활성 Consumer 수의 최소 2배 이상으로 유지해야 한다.
> Blue와 Green 환경이 동시에 구동될 때, 리밸런싱을 통해 각 환경의 Consumer들에게 최소 하나 이상의 파티션을 골고루 할당할 수 있도록 보장한다.

**분석:**
현재 환경은 8파티션 / 6 Consumer(Blue 3 + Green 3)이므로, "모든 Consumer에게 최소 1파티션 할당"이라는 Research의 최소 요건(8 >= 6)은 충족한다. 그러나 Plan 문서는 이 "파티션 분할 문제"를 단순히 "전략 C의 구조적 특성"으로만 기술하고, Research의 2배수 규칙과 연관짓지 않았다.

핵심 맥락: Research의 2배수 규칙은 "모든 Consumer가 파티션을 받도록" 보장하는 것이지, "PAUSED Consumer가 파티션을 낭비하는 문제"를 해결하지는 않는다. 전략 A(=Plan 전략 C)에서 PAUSED Consumer가 파티션 소유권을 유지하는 것은 **의도된 설계**(리밸런싱 방지)이며, 그 대가로 유휴 파티션이 발생하는 트레이드오프다.

Research 섹션 2의 핵심을 정확히 인용하면:
> **파티션 소유권 유지**: pause() 상태에서도 할당받은 파티션을 놓아주지 않는다. 같은 그룹 내에서 Blue를 pause한다고 해서 Green이 그 파티션을 자동으로 가져가지는 않는다

이 트레이드오프가 changes.md에서 "구조적 특성"으로 묘사되나, Research 근거와의 연관이 명시되지 않아 **왜** 이 설계 선택이 이루어졌는지 이해하기 어렵다.

**수정 제안:** changes.md의 "전략 C 구조적 특성" 섹션에 Research 근거 추가:
```markdown
### 전략 C 구조적 특성 (파티션 분할 문제)

Research Summary 섹션 2에 기술된 대로, pause()는 파티션 소유권을 유지하면서
Fetch만 중단한다. 이는 리밸런싱 방지를 위한 의도된 설계이며, 그 트레이드오프로
PAUSED 측 파티션(3~4개)이 유휴 상태가 된다.

- 8파티션 / 6 Consumer → Research의 2배수 최소 요건(8 >= 6)은 충족
- 그러나 ACTIVE 측만 실제 소비 → 유효 파티션은 4~5개
- 전략 B(별도 Consumer Group)에서는 이 문제 없음 (각 Group이 전체 파티션 소유)
```

---

### M2. 전략 B Offset 동기화 시 데이터 무결성 고려사항 미반영

**위치:** task06.md

**Plan 문서의 기술 (task06.md):**
> D5: `--to-current` offset reset (Blue shutdown 후 log-end-offset으로 동기화)
> 시나리오 3: Lag 발생 중 전환 → `--to-current` 한계, 미소비 메시지 유실 관찰

**Research Summary 근거 (섹션 9, Blue-Green 전환 시 3가지 실패 윈도우):**
> **윈도우 2 (핵심 문제): DB 쓰기 성공, 오프셋 커밋 전 Blue 크래시**
> → Green이 동일 메시지를 재처리하면 **DB에 중복 발생**. Kafka 트랜잭션은 이를 방지할 수 없다.
>
> **윈도우 3: DB에 일시적 오류로 롤백, Kafka 오프셋은 커밋됨**
> → Kafka는 처리 완료로 간주하나 DB에는 반영 안 됨. **데이터 유실**.

**분석:**
task06.md는 `--to-current` offset reset의 Lag 상황 한계를 간략히 언급하지만, Research가 상세히 분석한 **3가지 실패 윈도우**와 이에 대한 대안 패턴(Transactional Outbox, CDC, DB에 Offset 저장, 멱등 Consumer)을 전혀 참조하지 않는다. 전략 B는 Blue의 graceful shutdown 후 Green에 offset을 동기화하는 과정에서 윈도우 2(Blue 크래시 시 중복)와 윈도우 3(DB 롤백 시 유실)이 특히 취약하다.

task06.md의 "Lag 발생 중 전환" 시나리오에서 `--to-current` 사용 시 미소비 메시지가 건너뛰어지는 것은 사실상 **데이터 유실**이며, 이것이 전략 B의 근본적 한계임을 Research 근거와 함께 명시해야 한다.

**수정 제안:** task06.md에 다음 섹션 추가:
```markdown
### 데이터 무결성 고려사항 (Research Summary 섹션 9 참조)

`--to-current` offset reset은 Blue의 마지막 committed offset이 아닌
log-end-offset으로 동기화하므로, Blue shutdown과 Green 시작 사이에
Producer가 발행한 메시지가 있으면:

- **정상 전환**: Blue가 graceful shutdown하여 offset commit 완료 후 `--to-current`
  → log-end-offset으로 동기화 → 유실 0건
- **Lag 존재 시**: Blue의 committed offset < log-end-offset인 상태에서 `--to-current`
  → **미소비 메시지 건너뜀 = 데이터 유실**

Research가 제시한 대안 패턴(멱등 Consumer, DB에 Offset 저장)은
전략 B에서 특히 중요하며, 프로덕션 적용 시 고려 필수.
```

---

### M3. `pauseImmediate` 속성 미사용에 대한 Plan 문서 내 설명 부재

**위치:** task02.md, decisions.md

**Research Summary 근거 (섹션 2, Spring Kafka 버전별 pause/resume 기능):**
> | 2.9 | **`pauseImmediate`** 속성 추가 — pause 시 현재 배치 전체가 아닌 현재 레코드 처리 후 즉시 중단 |
>
> **본 프로젝트 참고**: Spring Boot 2.7.18 (Spring Kafka 2.8.x) 사용 중이므로 `pauseImmediate`는 미사용. Atomic Switch에서 현재 레코드 단위 즉시 pause가 필요한 경우 Spring Kafka 2.9.x로 버전 오버라이드 필요.

**분석:**
Research Summary가 명시적으로 프로젝트 참고사항으로 `pauseImmediate` 미사용을 기술하고 있으나, decisions.md(Spring Kafka 2.8.11 선택 근거)나 task02.md에서 이 제약을 언급하지 않는다. 전략 C의 Atomic Switch에서 pause 시 현재 **배치 전체**를 처리한 후에야 PAUSED로 전환되므로, 대량 배치 처리 중에는 pause 지연이 발생할 수 있다. 이는 전환 시간이나 중복 처리에 영향을 줄 수 있는 특성이다.

**수정 제안:** decisions.md의 Spring Kafka 섹션에 추가:
```markdown
**제약사항:**
- `pauseImmediate` 속성은 Spring Kafka 2.9에서 추가됨 (본 프로젝트 2.8.11에서 미사용)
- pause 요청 시 현재 배치 전체를 처리한 후에야 PAUSED로 전환됨
- Atomic Switch에서 레코드 단위 즉시 pause가 필요한 경우 Spring Kafka 2.9.x로
  오버라이드 필요 (Research Summary 섹션 2 참조)
```

---

### M4. Volume Mount 최대 전파 지연 수치 불일치

**위치:** `improvment/after-task05.md` vs `improvment/after-task05-indepth.md`

**after-task05.md (섹션 6, Volume Mount 설계):**
> | `kubelet --sync-frequency` | 1분 | kubelet이 ConfigMap Volume을 갱신하는 주기 |
> | ConfigMap TTL Cache | 1분 | kubelet의 ConfigMap 캐시 TTL |
> | **최대 전파 지연** | **~2분** | 최악의 경우 ConfigMap 변경 후 파일 반영까지 |

**after-task05-indepth.md (섹션 2):**
> | syncFrequency | 최소 지연 | 최대 지연 (jitter 포함) |
> |---------------|-----------|-------------------------|
> | 60초 (기본)   | ~60초     | ~90초                   |

**분석:**
after-task05.md는 "최대 ~2분"으로 기술하고, after-task05-indepth.md는 "60~90초"로 기술하여 약 30초의 차이가 있다. after-task05-indepth.md가 kubelet의 `syncPod()` 메커니즘을 더 상세히 분석한 결과이므로, "60~90초"가 더 정확하다. after-task05.md의 "~2분"은 syncFrequency(60초) + TTL Cache(60초)를 단순 합산한 것으로 보이나, indepth 문서에서 분석한 대로 이 두 지연은 독립적이 아니라 syncPod() 주기에 종속된다.

**수정 제안:** after-task05.md의 해당 표를 정정:
```
| **최대 전파 지연** | **~2분** |
→
| **최대 전파 지연** | **~60-90초** | syncFrequency(60초) + jitter(최대 1.5배). 상세 분석은 after-task05-indepth.md 섹션 2 참조 |
```

---

## Low

### L1. KIP-848 (차세대 Consumer Rebalance Protocol) 향후 고려 누락

**위치:** plan 문서 전체

**Research Summary 근거 (섹션 4):**
> **KIP-848: 차세대 Consumer Rebalance Protocol (Kafka 4.0)**
> Kafka 4.0에서 GA 예정인 KIP-848은 파티션 할당 로직을 **서버 사이드**로 이동시킨다. 클라이언트 측 Assignor가 불필요해지며, 기본적으로 완전한 Incremental Rebalancing이 적용된다. 현재 프로젝트의 K8s v1.23.8 환경에서는 해당 없으나, 향후 마이그레이션 시 고려할 사항이다.

**분석:**
Plan 문서에서 KIP-848에 대한 언급이 전혀 없다. 현재 프로젝트에 직접 영향은 없으나, decisions.md의 "향후 고려사항" 또는 task08.md의 "결론 및 권장사항"에서 Kafka 4.0 마이그레이션 시 CooperativeStickyAssignor 설정이 불필요해지는 변화를 언급하면 문서의 완성도가 높아진다. 특히 Static Membership과 CooperativeStickyAssignor에 의존하는 현재 설계가 KIP-848 환경에서 어떻게 변경되는지 기술할 가치가 있다.

**수정 제안 (선택):** decisions.md 하단에 추가:
```markdown
## 향후 마이그레이션 참고

### KIP-848 (Kafka 4.0, 서버 사이드 파티션 할당)
- 클라이언트 측 Assignor(CooperativeStickyAssignor) 설정 불필요
- 기본적으로 완전한 Incremental Rebalancing 적용
- Static Membership의 역할 재평가 필요
- Research Summary 섹션 4 참조
```

---

### L2. Burrow 미채택 사유 미기록

**위치:** decisions.md, task01.md

**Research Summary 근거 (섹션 10, 모니터링 도구 비교표):**
> | **Burrow** (LinkedIn) | 행동 분석 (임계치 없음) | 오탐 적음, 자동 건강 평가, 설정 최소화 | 제한된 시각화 |

**분석:**
Research Summary가 Burrow의 슬라이딩 윈도우 기반 건강 평가를 상세히 소개하고 "권장 조합"에도 Prometheus/Burrow를 포함했으나, decisions.md에서는 `kafka_exporter`(Strimzi 내장)만 선택하고 Burrow 미채택 사유를 기록하지 않았다. 단일 노드 테스트 환경에서 추가 컴포넌트를 줄이는 실용적 결정이었을 수 있으나, 명시적 근거 기록이 없다.

**수정 제안 (선택):** decisions.md에 추가:
```markdown
**Burrow 미채택 사유:**
- Strimzi Kafka Exporter가 기본 Consumer Lag 메트릭 제공 → 별도 설치 불필요
- 단일 노드 테스트 환경에서 추가 컴포넌트 최소화 원칙
- 프로덕션 적용 시 Burrow의 행동 분석 기반 건강 평가 도입 권장 (Research Summary 섹션 10 참조)
```

---

## 검토 완료 항목 (문제 없음)

다음 항목들은 Research Summary와 정합성을 확인하였으며, Plan 문서에 올바르게 반영되어 있다:

| 검토 항목 | Plan 문서 | 판정 |
|-----------|-----------|------|
| `/actuator/bindings` 정정 (Spring Cloud Stream 의존성 필요) | task02.md — 자체 `/lifecycle/*` 엔드포인트 구현 | OK |
| `max.poll.interval.ms`와 Heartbeat 분리 (KIP-62) | task02.md — 2-스레드 모델 기반 설계 | OK |
| Spring Kafka pause 시 `poll(100ms)` 지속 호출 | task02.md — Container.pause() 사용 (poll loop 유지) | OK |
| Static Membership + StatefulSet 시너지 | task02.md, decisions.md — `group.instance.id=${HOSTNAME}` | OK |
| Cooperative Sticky Assignor 사용 | task02.md — `partition.assignment.strategy` 명시 | OK |
| Deployment/Argo Rollouts에서 Static Membership 비호환 | after-task05-indepth.md 섹션 7 — 상세 분석 포함 | OK |
| Argo Rollouts의 B/G 전략이 Kafka Consumer에 부적합 | after-task05-indepth.md 섹션 7.5 — Service selector vs 파티션 할당 차이 명시 | OK |
| Consumer 기본 PAUSED 시작 (Dual-Active 방지) | task05-post-improvement.md Phase 1 — P0 해결 | OK |
| 4-레이어 안전망 구현 | task05-post-improvement.md — L1~L4 설계 및 검증 완료 | OK |
| Sidecar client-go 제거 (Volume Mount + HTTP Push) | task05-post-improvement.md Phase 2 — 빌드 확인 | OK |
| KRaft 모드 선택 (ZooKeeper 제거) | decisions.md — 단일 노드 리소스 절약 근거 | OK |
| Strimzi 0.43.0 (K8s 1.23 마지막 호환 버전) | decisions.md — 버전 선택 근거 명시 | OK |
| Kafka 3.8.0 + KIP-345/KIP-429/KIP-875/KIP-980 지원 | decisions.md — 전략별 필요 기능 매핑 | OK |
| `session.timeout.ms: 45000` (KIP-735, Kafka 3.0+) | task02.md — Research와 일치 | OK |
| KEDA 2.9.3 (K8s 1.23 마지막 호환) | decisions.md — 호환성 매트릭스 참조 | OK |
| `PauseAwareRebalanceListener` 구현 | task02.md — Rebalance 후 pause 상태 재적용 | OK |
| Kafka 트랜잭션 범위의 한계 (외부 시스템 중복 방지 불가) | 프로젝트 설계상 at-least-once 채택 (트랜잭션 미사용) | OK |
