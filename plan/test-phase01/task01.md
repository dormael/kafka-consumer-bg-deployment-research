# Task 01: Kubernetes 인프라 셋업

> **의존:** 없음
> **선행 조건:** kubectl, helm CLI 사용 가능, K8s 클러스터 접근 가능
> **튜토리얼:** `tutorial/01-cluster-setup.md`, `tutorial/02-monitoring-setup.md`, `tutorial/03-kafka-setup.md`, `tutorial/04-argo-rollouts-setup.md`

---

## 목표

검증 테스트에 필요한 모든 인프라 컴포넌트를 Kubernetes 클러스터에 설치하고, 기본 동작을 확인한다.

## 네임스페이스 구조

```
kafka-bg-test (기본 K8s 노드)
├── monitoring       # Prometheus, Grafana, Loki
├── kafka            # Strimzi Operator, Kafka Cluster
├── argo-rollouts    # Argo Rollouts Controller
├── keda             # KEDA (선택)
└── kafka-bg-test    # Producer, Consumer, Switch Controller (테스트 워크로드)
```

## 세부 단계

### 1.1 Helm Repo 추가

```bash
helm repo add strimzi https://strimzi.io/charts/
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo add grafana https://grafana.github.io/helm-charts
helm repo add argo https://argoproj.github.io/argo-helm
helm repo add kedacore https://kedacore.github.io/charts
helm repo update
```

### 1.2 네임스페이스 생성

```bash
kubectl create namespace monitoring
kubectl create namespace kafka
kubectl create namespace argo-rollouts
kubectl create namespace keda
kubectl create namespace kafka-bg-test
```

### 1.3 kube-prometheus-stack 설치

- **Chart 버전:** 51.10.0
- **설치 네임스페이스:** monitoring
- **커스텀 values:** `k8s/helm-values/prometheus-values.yaml`
- **핵심 설정:**
  - Grafana 활성화 (NodePort 또는 port-forward로 접근)
  - ServiceMonitor 활성화 (Strimzi, Consumer App 지표 자동 수집)
  - Prometheus retention: 7d
  - 리소스 제한 설정 (단일 노드 환경 고려)

**확인 항목:**
- [x] Prometheus UI 접근 가능
- [x] Grafana UI 접근 가능
- [x] 기본 K8s 지표 수집 확인

### 1.4 Loki Stack 설치

- **Chart 버전:** 2.10.2
- **설치 네임스페이스:** monitoring
- **커스텀 values:** `k8s/helm-values/loki-values.yaml`
- **핵심 설정:**
  - Promtail 활성화
  - Grafana에 Loki 데이터소스 자동 추가
  - 리소스 제한 설정

**확인 항목:**
- [x] Grafana에서 Loki 데이터소스 조회 가능
- [x] Pod 로그가 Loki에 수집되는지 확인

### 1.5 Strimzi Operator 설치

- **Chart 버전:** 0.43.0
- **설치 네임스페이스:** kafka
- **커스텀 values:** `k8s/helm-values/strimzi-values.yaml`
- **핵심 설정:**
  - watchNamespaces: kafka, kafka-bg-test
  - 리소스 제한 설정

**확인 항목:**
- [x] Strimzi Operator Pod 정상 Running — `strimzi-cluster-operator` Running
- [x] Kafka CRD 확인: `kubectl get crd | grep kafka`

### 1.6 Kafka Cluster 배포 (Strimzi CR)

- **Kafka 버전:** 3.8.0
- **모드:** KRaft (ZooKeeper 없음)
- **매니페스트:** `k8s/kafka-cluster.yaml`
- **핵심 설정:**
  - KRaft controller 1개 (단일 노드)
  - Kafka broker 1개 (단일 노드, 테스트 환경)
  - 파티션: 테스트 토픽 8개 파티션
  - JMX Exporter 활성화 → Prometheus 지표 수집
  - Kafka Exporter 활성화 → Consumer Group Lag 지표
  - 리소스 제한 설정

**확인 항목:**
- [x] Kafka broker Pod 정상 Running — `kafka-cluster-dual-role-0` Running
- [x] 토픽 생성 및 메시지 produce/consume 테스트 — `bg-test-topic` Ready
- [x] JMX Exporter 지표 Prometheus에 수집 확인 — `kafka-cluster-kafka-exporter` Running

### 1.7 테스트 토픽 생성

```yaml
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaTopic
metadata:
  name: bg-test-topic
  namespace: kafka
  labels:
    strimzi.io/cluster: kafka-cluster
spec:
  partitions: 8
  replicas: 1
  config:
    retention.ms: "86400000"  # 1일
    min.insync.replicas: "1"
```

### 1.8 Argo Rollouts Controller 설치

- **Chart 버전:** 2.35.3
- **설치 네임스페이스:** argo-rollouts
- **커스텀 values:** `k8s/helm-values/argo-rollouts-values.yaml`
- **핵심 설정:**
  - Dashboard 활성화
  - 리소스 제한 설정

**확인 항목:**
- [x] Argo Rollouts Controller Pod 정상 Running — 2 replicas + dashboard Running
- [x] `kubectl argo rollouts version` 확인
- [x] Rollout CRD 설치 확인

**TODO (다른 배포 도구 - 향후 검토):**
- Flagger (Istio/Linkerd 서비스 메시 연동)
- OpenKruise Rollout (CNCF 인큐베이팅)
- Keptn (자동 관찰 가능성 + 배포)

### 1.9 Strimzi KafkaConnect 클러스터 배포 (전략 E용)

- **매니페스트:** `k8s/kafka-connect.yaml`
- **핵심 설정:**
  - Blue/Green 별도 KafkaConnect 클러스터 2개 (물리적 분리)
  - 각각 별도 config/offset/status 토픽
  - FileStreamSink Connector 플러그인 포함
  - JMX Exporter 활성화

**확인 항목:**
- [x] Blue/Green KafkaConnect Pod 정상 Running — `connect-blue-connect-0`, `connect-green-connect-0` Running
- [ ] REST API 접근 가능: `curl http://<connect-svc>:8083/` — 배포 후 확인 필요
- [ ] Connector 목록 조회: `curl http://<connect-svc>:8083/connectors` — 배포 후 확인 필요

### 1.10 KEDA 설치 (선택)

- **Chart 버전:** 2.9.4
- **설치 네임스페이스:** keda
- **커스텀 values:** `k8s/helm-values/keda-values.yaml`

**확인 항목:**
- [x] KEDA Operator Pod 정상 Running — `keda-operator` + `keda-operator-metrics-apiserver` Running
- [x] ScaledObject CRD 설치 확인

### 1.11 Grafana 대시보드 Import

- **대시보드 JSON:** `k8s/grafana-dashboards/`
- **Row 1:** Blue/Green 상태 개요
- **Row 2:** Consumer Lag 비교
- **Row 3:** 처리 성능
- **Row 4:** 전환 이벤트 타임라인

## 완료 기준

- [x] 모든 컴포넌트 Pod가 Running/Ready 상태 — 2026-02-21 클러스터 확인 완료 (monitoring 8, kafka 6, argo-rollouts 3, keda 2)
- [x] Prometheus에서 Kafka 관련 지표 조회 가능 — Kafka Exporter Running
- [x] Grafana에서 Loki 로그 조회 가능 — Loki + Promtail Running
- [x] Kafka 토픽에 메시지 produce/consume 정상 동작 — bg-test-topic Ready, ConfigMap active=blue
- [ ] Grafana 대시보드에서 기본 지표 표시 — 앱 배포 후 검증 필요
