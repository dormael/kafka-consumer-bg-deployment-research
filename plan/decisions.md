# 컴포넌트/라이브러리 버전 선택 근거

> **작성일:** 2026-02-20
> **핵심 제약:** Kubernetes v1.23.8 호환성

---

## 제약 사항

Kubernetes v1.23.8은 **2023년 2월 EOL**이 된 버전으로, 대부분의 컴포넌트에서 지원하는 마지막 버전에 해당한다. 아래 선택은 모두 K8s 1.23 호환성을 최우선으로 고려하였다.

---

## 1. Strimzi Operator → v0.43.0

| 항목 | 값 |
|------|-----|
| **선택 버전** | 0.43.0 |
| **Helm Chart** | 0.43.0 |
| **Helm Repo** | `https://strimzi.io/charts/` |
| **지원 K8s** | 1.23+ |

**선택 근거:**
- Strimzi 0.43.0은 K8s 1.23을 지원하는 **마지막 버전**. 0.44.0부터 최소 K8s 1.25 요구.
- Kafka 3.7.0, 3.7.1, 3.8.0을 지원하며, 전략 E 검증에 필요한 KafkaConnect/KafkaConnector CRD를 완전 지원.
- KIP-980(Stopped 상태로 Connector 생성) 지원을 위해 Kafka 3.5+ 필요 → 충족.

**주의사항:**
- EOL K8s에 대한 마지막 지원 버전이므로, 향후 보안 패치 없음.
- Kafka 3.6.x 지원이 이 버전에서 제거됨.

---

## 2. Apache Kafka → 3.8.0 (via Strimzi)

| 항목 | 값 |
|------|-----|
| **선택 버전** | 3.8.0 |
| **배포 방식** | Strimzi Kafka CR의 `spec.kafka.version` |
| **KRaft 지원** | 가능 (Strimzi 0.43.0에서 KRaft GA) |

**선택 근거:**
- Strimzi 0.43.0의 기본(default) Kafka 버전.
- KIP-875(First-class Offsets Support): 전략 E의 Connector offset 조작 REST API 지원.
- KIP-980(Stopped state Connector): 전략 E의 초기 STOPPED 상태 Connector 생성 지원.
- Static Membership(KIP-345)은 Kafka 2.3+부터 지원 → 충분히 충족.

**ZooKeeper vs KRaft 결정:**
- 단일 노드 테스트 환경에서는 **KRaft 모드** 사용 권장.
- ZooKeeper 제거로 리소스 절약 가능 (테스트 환경의 단일 노드에서 중요).
- Strimzi 0.43.0에서 KRaft가 GA(General Availability) 상태.

---

## 3. kube-prometheus-stack → Helm Chart v51.10.0

| 항목 | 값 |
|------|-----|
| **선택 Chart 버전** | 51.10.0 |
| **Prometheus Operator** | v0.68.0 |
| **Prometheus** | v2.47.x |
| **Grafana** | 포함 (서브차트) |
| **Helm Repo** | `https://prometheus-community.github.io/helm-charts` |
| **지원 K8s** | >=1.19.0 |

**선택 근거:**
- `kubeVersion: >=1.19.0-0` 조건으로 K8s 1.23 완전 호환.
- 기술적으로는 Chart 72.9.1까지 호환 가능하나, 51.10.0이 K8s 1.23에서 가장 안정적으로 테스트된 버전.
- Prometheus + Grafana + AlertManager 올인원 번들로 별도 Grafana 설치 불필요.
- ServiceMonitor/PodMonitor CRD 포함 → Strimzi Kafka Exporter 및 Consumer App 지표 자동 수집.

**대안 고려:**
- Victoria Metrics: kube-prometheus-stack 대비 메모리 사용량 적으나, 단일 노드 테스트 환경에서는 Prometheus가 더 표준적이고 Grafana 대시보드 호환성이 높음.

---

## 4. Grafana Loki Stack → Helm Chart v2.10.2

| 항목 | 값 |
|------|-----|
| **선택 Chart 버전** | 2.10.2 |
| **Loki App** | v2.9.3 |
| **Promtail** | 포함 |
| **Helm Repo** | `https://grafana.github.io/helm-charts` |

**선택 근거:**
- loki-stack 차트는 kubeVersion 제약 없음 → K8s 1.23 호환.
- Monolithic 모드로 단일 노드 테스트 환경에 적합 (Scalable 모드 불필요).
- Promtail 포함 → 별도 로그 수집기 설치 불필요.
- kube-prometheus-stack의 Grafana에 Loki 데이터소스를 추가하여 통합 대시보드 구성.

**대안 고려:**
- Victoria Logs: 아직 GA가 아니며 Grafana 통합이 Loki 대비 미흡. Loki가 더 적합.

---

## 5. Argo Rollouts → v1.6.6 (Helm Chart v2.35.3)

| 항목 | 값 |
|------|-----|
| **선택 App 버전** | v1.6.6 |
| **Helm Chart** | 2.35.3 |
| **Helm Repo** | `https://argoproj.github.io/argo-helm` |
| **kubeVersion** | >=1.7 |

**선택 근거:**
- Helm 차트의 kubeVersion 조건이 `>=1.7`로 K8s 1.23 호환.
- CRD가 `apiextensions/v1` (K8s 1.16+에서 안정) 사용 → 호환성 문제 없음.
- 전략 B, C의 전환/롤백 오케스트레이션에 Argo Rollouts의 Blue/Green 전략 활용.
- Argo Rollouts는 Kafka Consumer를 직접 제어하지 않지만(공식 문서 명시), 배포 단위의 Blue/Green 전환 및 자동 롤백 프레임워크로 활용.

**주의사항:**
- Argo Rollouts 공식 테스트 매트릭스에 K8s 1.23은 미포함 (N, N-1 정책).
- 실제 사용되는 API surface에는 K8s 1.23과의 비호환 변경 없음 확인.

**대안 (TODO로만 기록):**
- Flagger: Istio/Linkerd 서비스 메시와 통합 → 현재 테스트 범위 외.
- OpenKruise Rollout: CNCF 인큐베이팅 → 아직 Argo Rollouts 대비 생태계 작음.
- Keptn: 자동 관찰 가능성 + 배포 → 복잡도 높음.

---

## 6. KEDA → v2.9.3 (Helm Chart v2.9.4)

| 항목 | 값 |
|------|-----|
| **선택 App 버전** | 2.9.3 |
| **Helm Chart** | 2.9.4 |
| **Helm Repo** | `https://kedacore.github.io/charts` |
| **지원 K8s** | 1.23 ~ 1.25 |

**선택 근거:**
- KEDA 공식 호환성 매트릭스에서 K8s 1.23을 지원하는 **마지막 major 버전** (2.10부터 K8s 1.24+).
- Apache Kafka Scaler 포함 → Consumer Lag 기반 자동 스케일링 검증 가능.
- 선택 사항(Optional)으로 설치하며, 전략 C에서 PAUSED 상태의 스케일 업 정합성 확인에 사용.

---

## 7. Spring Boot → 2.7.18

| 항목 | 값 |
|------|-----|
| **선택 버전** | 2.7.18 |
| **Java 요구** | 8, 11, 또는 17 |
| **Spring Framework** | 5.3.x |

**선택 근거:**
- Spring Boot 2.7.x 최종 릴리즈(2023.11).
- Spring Boot 3.x는 Java 17 필수 + Jakarta EE 9 마이그레이션 필요 → 테스트 목적으로는 불필요한 복잡도.
- Java 17으로 빌드하되, Spring Boot 2.7.x를 사용하여 안정성 확보.
- Micrometer 1.9.x 포함 → Prometheus 지표 노출에 충분.

**대안:**
- Spring Boot 3.2.x/3.3.x: Java 17 + Jakarta EE 9. 프로덕션이라면 권장하나, 이번 테스트 목적에는 2.7.18이 더 안정적.

---

## 8. Spring Kafka → 2.8.11 (Spring Boot 2.7.18 BOM 관리)

| 항목 | 값 |
|------|-----|
| **선택 버전** | 2.8.11 (Spring Boot BOM 자동 관리) |
| **Kafka Client** | 3.1.2 |
| **브로커 호환** | Kafka 0.10.2.0+ (하위 호환) |

**선택 근거:**
- Spring Boot 2.7.18의 BOM에서 관리하는 기본 버전.
- Kafka Client 3.1.2 → Kafka Broker 3.8.0과 프로토콜 하위 호환성 보장.
- `KafkaListenerEndpointRegistry.pause()/resume()` 지원 → 전략 C 구현의 핵심.
- `ConsumerRebalanceListener` 완전 지원 → Rebalance 시 pause 상태 복구 구현 가능.
- `CooperativeStickyAssignor` 지원 → 전략 C의 점진적 파티션 이전 가능.

**Kafka Client 버전 오버라이드 고려:**
- 필요시 `kafka.version`을 3.7.1 또는 3.8.0으로 오버라이드 가능하나, 기본 3.1.2로도 브로커와의 호환성에 문제 없음.
- KIP-345(Static Membership)은 Kafka Client 2.3+부터 지원 → 3.1.2에서 충족.

---

## 9. Switch Sidecar/Controller → Go 1.21+

| 항목 | 값 |
|------|-----|
| **언어** | Go |
| **버전** | 1.21 이상 |
| **K8s Client** | client-go (k8s.io/client-go) |

**선택 근거:**
- kickoff-prompt.md에서 "Go 권장"으로 명시.
- 경량 바이너리 → Sidecar로 적합 (메모리 ~64Mi).
- `client-go`의 Informer/Watch 패턴으로 ConfigMap/CRD 변경 감지 구현.
- K8s Lease API(`coordination.k8s.io/v1`)로 양쪽 동시 Active 방지.

---

## 10. Validator 스크립트 → Python 3.x

| 항목 | 값 |
|------|-----|
| **언어** | Python 3.9+ |
| **의존성** | requests (Loki 쿼리), json, argparse |

**선택 근거:**
- 데이터 비교/분석 스크립트에 Python이 가장 적합.
- Loki HTTP API를 통해 Producer/Consumer 로그를 쿼리하여 시퀀스 번호 비교.
- 별도 빌드 불필요, 즉시 실행 가능.

---

## 버전 요약표

| 컴포넌트 | 버전 | Helm Chart 버전 | Helm Repo |
|----------|------|-----------------|-----------|
| Strimzi Operator | 0.43.0 | 0.43.0 | `strimzi https://strimzi.io/charts/` |
| Apache Kafka | 3.8.0 | (Strimzi CR) | N/A |
| kube-prometheus-stack | Operator v0.68.0 | 51.10.0 | `prometheus-community https://prometheus-community.github.io/helm-charts` |
| Grafana Loki Stack | v2.9.3 | 2.10.2 | `grafana https://grafana.github.io/helm-charts` |
| Argo Rollouts | v1.6.6 | 2.35.3 | `argo https://argoproj.github.io/argo-helm` |
| KEDA | 2.9.3 | 2.9.4 | `kedacore https://kedacore.github.io/charts` |
| Spring Boot | 2.7.18 | N/A | N/A |
| Spring Kafka | 2.8.11 | N/A | N/A |
| Go (Sidecar) | 1.21+ | N/A | N/A |
| Python (Validator) | 3.9+ | N/A | N/A |
