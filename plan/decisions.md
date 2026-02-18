# 기술 결정 사항 (Technology Decisions)

> 셋업 및 코드 구현 시 사용할 컴포넌트/라이브러리 버전 선택과 그 이유를 기록한다.

---

## D-001: Kafka Cluster 설치 방식 — Strimzi Operator

**결정:** Strimzi Kafka Operator를 사용하여 Kafka Cluster를 Kubernetes에 설치한다.

**선택 버전:** Strimzi 0.44.x (Apache Kafka 3.8.x 포함)

**이유:**
1. **전략 E 필수**: 전략 E(Kafka Connect REST API)의 검증을 위해 `KafkaConnect`, `KafkaConnector` CRD가 필요하며, Strimzi가 이를 네이티브로 지원
2. **CRD 기반 운영**: `spec.state` 필드를 통한 Connector 상태 제어를 검증해야 함 (Strimzi Proposal #054)
3. **지표 수집 내장**: Strimzi는 JMX Exporter, Kafka Exporter를 CRD 설정으로 간편하게 활성화 가능
4. **Kubernetes 네이티브**: Helm이 아닌 Operator 패턴으로 Kafka의 생명주기를 K8s API로 관리
5. **커뮤니티 활성도**: CNCF Graduated 수준의 성숙도, 활발한 릴리즈 주기

**대안 검토:**
- Confluent for Kubernetes (CFK): 상용 라이센스 필요, 검증 목적에 과도
- Bitnami Kafka Helm Chart: 단순 설치에 적합하나 KafkaConnect CRD 미지원
- Apache Kafka Helm (official): Strimzi 대비 Operator 기능 부족

---

## D-002: 모니터링 스택 — Prometheus + Grafana (kube-prometheus-stack)

**결정:** `kube-prometheus-stack` Helm chart를 사용하여 Prometheus, Grafana, AlertManager를 일괄 설치한다.

**선택 버전:** kube-prometheus-stack 67.x+ (Prometheus 3.x, Grafana 11.x)

**이유:**
1. **올인원**: Prometheus, Grafana, AlertManager, node-exporter, kube-state-metrics를 단일 chart로 설치
2. **ServiceMonitor/PodMonitor CRD**: Strimzi가 생성하는 ServiceMonitor와 자연스럽게 연동
3. **Grafana 대시보드 프로비저닝**: ConfigMap 기반으로 대시보드를 코드로 관리 가능
4. **Alert Rules 프로비저닝**: PrometheusRule CRD로 알람 규칙을 선언적으로 관리

**대안 검토:**
- Victoria Metrics: 성능 우수하나 Grafana 대시보드 호환성에서 추가 설정 필요. 검증 목적에서는 kube-prometheus-stack이 더 간편
- Datadog/New Relic: SaaS 기반으로 로컬 K8s 환경에 부적합

---

## D-003: 로그 수집 — Grafana Loki

**결정:** Grafana Loki를 사용하여 로그를 수집한다.

**선택 버전:** Loki 3.x (loki-stack 또는 loki Helm chart)

**이유:**
1. **Grafana 통합**: Prometheus와 동일한 Grafana UI에서 로그와 지표를 함께 조회 가능
2. **레이블 기반 인덱싱**: Pod 이름, namespace, container 레이블로 효율적인 로그 필터링
3. **경량성**: Elasticsearch 대비 리소스 사용량이 적어 검증 환경에 적합
4. **LogQL**: PromQL과 유사한 쿼리 언어로 학습 비용 낮음

**대안 검토:**
- Victoria Logs: 아직 GA 전단계로 안정성 검증 부족
- Elasticsearch + Kibana: 리소스 소모가 크고 검증 환경에 과도

---

## D-004: Producer/Consumer 앱 — Spring Boot 3.4.x + Spring Kafka

**결정:** Java 21 + Spring Boot 3.4.x + Spring Kafka를 사용한다.

**선택 버전:**
- Java: 21 (LTS)
- Spring Boot: 3.4.x
- Spring Kafka: 3.3.x (Spring Boot 3.4.x와 호환)
- Apache Kafka Client: 3.8.x (Strimzi Kafka 버전과 일치)

**이유:**
1. **Spring Kafka의 Pause/Resume 추상화**: `KafkaListenerEndpointRegistry`를 통한 `container.pause()`/`container.resume()` 지원으로 전략 C 구현 간소화
2. **Micrometer 내장**: Spring Boot Actuator + Micrometer로 Prometheus 지표 자동 노출
3. **ConsumerRebalanceListener 지원**: `onPartitionsAssigned`에서 pause 상태 복구 로직 구현 가능
4. **Spring Boot Actuator**: 헬스체크, 지표 엔드포인트 기본 제공
5. **Kafka Client 버전 일치**: Strimzi Kafka 3.8.x와 동일한 Kafka Client를 사용하여 프로토콜 호환성 보장

**대안 검토 (TODO로 기록):**
- Go (twmb/franz-go): goroutine-safe한 현대적 Go 클라이언트. 추후 구현
- Node.js (KafkaJS): 이벤트루프 특성상 thread-safety 자연 해결. 추후 구현
- Python (confluent-kafka-python): librdkafka 기반. 추후 구현

---

## D-005: Switch Sidecar — Go

**결정:** Switch Sidecar를 Go로 구현한다.

**선택 버전:** Go 1.23.x

**이유:**
1. **경량 바이너리**: Sidecar 컨테이너에 적합한 작은 이미지 크기 (< 20MB)
2. **K8s client-go 네이티브**: ConfigMap/CRD Watch가 Go에서 가장 자연스러움
3. **낮은 메모리 사용량**: Sidecar로서 리소스 제약이 있는 환경에 적합 (64MB 메모리 요청)
4. **동시성 모델**: goroutine 기반 비동기 처리로 Watch + HTTP 요청 병행 용이

**대안 검토:**
- Java (Spring): Sidecar로는 JVM 오버헤드가 과도 (최소 128MB+)
- Python: K8s client 라이브러리 성숙도는 충분하나 컨테이너 크기와 성능에서 Go 대비 불리
- Rust: 성능 우수하나 K8s client 생태계가 Go 대비 미성숙

---

## D-006: Switch Controller — Go

**결정:** Switch Controller도 Go로 구현한다.

**선택 버전:** Go 1.23.x + controller-runtime (kubebuilder 기반)

**이유:**
1. **Sidecar와 코드 공유**: 동일 언어로 공통 라이브러리 재사용 가능
2. **controller-runtime**: K8s Operator 구현의 표준 프레임워크
3. **K8s Lease API**: `coordination.k8s.io/v1` Lease를 통한 분산 잠금(양쪽 동시 Active 방지) 구현에 Go가 가장 적합
4. **Argo Rollouts와 유사한 패턴**: Argo Rollouts 자체가 Go로 구현되어 있어 참고 용이

---

## D-007: Argo Rollouts — Blue/Green 오케스트레이션

**결정:** Argo Rollouts를 전략 B, C의 전환/롤백 오케스트레이션 보조 도구로 사용한다.

**선택 버전:** Argo Rollouts 1.7.x

**이유:**
1. **Blue/Green 전략 내장**: `strategy.blueGreen` 스펙으로 선언적 Blue/Green 배포
2. **AnalysisTemplate**: Prometheus 지표 기반 자동 헬스체크 및 자동 롤백
3. **Pre/Post Promotion Analysis**: 전환 전후 검증 자동화
4. **kubectl 플러그인**: `kubectl argo rollouts` CLI로 직관적인 상태 확인 및 수동 제어

**한계 인지:**
- Argo Rollouts는 HTTP/gRPC 트래픽 제어만 가능하며, Kafka 파티션 할당을 직접 제어하지 못함
- 따라서 Argo Rollouts는 Pod 생명주기 관리 + 헬스체크에만 사용하고, Kafka 수준 전환은 Switch Controller가 담당

**대안 검토 (TODO로 기록):**
- Flagger: Argo Rollouts와 유사하나 Flux 생태계 의존
- OpenKruise Rollout: 알리바바 주도, 국내 사용 사례 부족
- Keptn: CNCF 프로젝트지만 Kafka 전환에 대한 지원 미비

---

## D-008: KEDA — Consumer Lag 기반 자동 스케일링 (선택)

**결정:** KEDA를 설치하되 핵심 검증 범위에는 포함하지 않고 선택적으로 검증한다.

**선택 버전:** KEDA 2.16.x

**이유:**
1. **Kafka ScaledObject**: Consumer Lag 기반 자동 스케일링 트리거 내장
2. **검증 보완**: 전환 후 Green Consumer의 Lag 급증 시 자동 스케일 아웃 동작 확인
3. **설치 간편**: Helm chart로 간단 설치, ScaledObject CRD만 추가 적용

---

## D-009: 전략 E의 SUT (테스트 대상 시스템)

**결정:** 전략 E는 커스텀 Java 앱 대신 Strimzi `KafkaConnector` CRD + `FileStreamSinkConnector`를 SUT로 사용한다.

**이유:**
1. **전략 E의 검증 포인트**: Kafka Connect 프레임워크 자체의 REST API/CRD 제어 방식을 검증하는 것이 목적
2. **커스텀 Consumer 불필요**: FileStreamSinkConnector는 Kafka Connect에 내장되어 별도 플러그인 빌드 불필요
3. **CRD reconcile 충돌 검증**: Strimzi Operator와 REST API 직접 호출 간의 충돌 여부 확인 (Issue #3277)
4. **config topic 기반 pause 영속성 검증**: Connect 프레임워크의 핵심 이점 실증

---

## D-010: Validator 스크립트 — Python

**결정:** 메시지 유실/중복 검증을 위한 Validator 스크립트를 Python으로 구현한다.

**선택 버전:** Python 3.12+

**이유:**
1. **데이터 처리 친화적**: 시퀀스 번호 비교, 집합 연산, 로그 파싱에 Python이 가장 편리
2. **Loki API 호출**: `requests` 라이브러리로 Loki LogQL 쿼리 결과를 쉽게 처리
3. **리포트 생성**: 결과를 Markdown 테이블, JSON으로 출력하기 용이
4. **낮은 진입장벽**: 검증 스크립트는 성능보다 가독성과 유지보수성이 중요

---

## D-011: 컨테이너 이미지 빌드/레지스트리

**결정:** 로컬 K8s 클러스터의 특성에 따라 결정한다.

**옵션:**
- **Kind/Minikube**: `docker build` + `kind load docker-image` / `minikube image load`
- **k3s**: 로컬 레지스트리 또는 containerd import
- **원격 클러스터**: GitHub Container Registry (ghcr.io) 또는 Docker Hub

**이유:**
- 검증 환경의 K8s 클러스터 종류에 따라 이미지 전달 방식이 달라지므로 셋업 시 확정

---

## D-012: Kubernetes 클러스터 환경

**결정:** 사용자의 기존 K8s 클러스터를 사용하거나, 없는 경우 Kind(Kubernetes in Docker)로 로컬 클러스터를 생성한다.

**Kind 선택 시 이유:**
1. **경량성**: Docker만 있으면 멀티노드 클러스터 생성 가능
2. **재현성**: 설정 파일로 동일한 환경을 재생성 가능
3. **CI 친화적**: 향후 자동화 테스트로 확장 가능

**대안:**
- Minikube: 단일 노드 제약, 멀티노드 지원이 Kind 대비 불안정
- k3s: 프로덕션에 가깝지만 설치 복잡도 증가
