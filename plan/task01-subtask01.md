# Task 01: Prometheus / Grafana 설치

**Phase:** 1 - 인프라 셋업
**의존성:** 없음 (첫 번째 작업)
**예상 소요:** 30분 ~ 1시간
**튜토리얼:** `tutorial/01-prometheus-grafana-setup.md`

---

## 목표

kube-prometheus-stack Helm chart를 사용하여 Prometheus, Grafana, AlertManager를 설치하고, Kafka/Consumer 지표 수집을 위한 기본 설정을 완료한다.

---

## Subtask 01-01: Helm repo 추가 및 namespace 생성

```bash
# Helm repo 추가
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update

# monitoring namespace 생성
kubectl create namespace monitoring
```

## Subtask 01-02: kube-prometheus-stack 설치

커스텀 values 파일을 작성하여 설치한다.

**주요 설정:**
- Grafana: NodePort 또는 port-forward로 접근
- Prometheus: retention 7일 (검증 환경 적합)
- AlertManager: 기본 설정 (추후 task13에서 Alert Rules 추가)
- ServiceMonitor 자동 검색: 모든 namespace의 ServiceMonitor 수집

```yaml
# values-prometheus.yaml 주요 항목
grafana:
  adminPassword: "admin"  # 검증 환경 전용
  service:
    type: NodePort
    nodePort: 30300
  sidecar:
    dashboards:
      enabled: true
      searchNamespace: ALL

prometheus:
  prometheusSpec:
    retention: 7d
    serviceMonitorSelectorNilUsesHelmValues: false  # 모든 namespace의 ServiceMonitor 수집
    podMonitorSelectorNilUsesHelmValues: false
    ruleSelectorNilUsesHelmValues: false
    storageSpec:
      volumeClaimTemplate:
        spec:
          accessModes: ["ReadWriteOnce"]
          resources:
            requests:
              storage: 10Gi

alertmanager:
  enabled: true
```

```bash
helm install kube-prometheus prometheus-community/kube-prometheus-stack \
  -n monitoring \
  -f values-prometheus.yaml
```

## Subtask 01-03: 설치 확인

```bash
# Pod 상태 확인
kubectl get pods -n monitoring

# Grafana 접근 확인
kubectl port-forward svc/kube-prometheus-grafana -n monitoring 3000:80

# Prometheus 접근 확인
kubectl port-forward svc/kube-prometheus-prometheus -n monitoring 9090:9090
```

## Subtask 01-04: Strimzi Kafka 지표 수집을 위한 추가 ServiceMonitor 준비

Strimzi에서 생성하는 지표(JMX Exporter, Kafka Exporter)를 Prometheus가 수집할 수 있도록 ServiceMonitor 설정을 준비한다. (실제 적용은 task03에서 Kafka 설치 후)

---

## 완료 기준

- [ ] Prometheus가 정상 기동되어 자체 지표(`up`)를 수집하고 있음
- [ ] Grafana에 접근 가능하고 Prometheus 데이터소스가 연결됨
- [ ] AlertManager가 정상 기동됨
- [ ] `serviceMonitorSelectorNilUsesHelmValues: false` 설정으로 외부 namespace ServiceMonitor 수집 가능 확인
