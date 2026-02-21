# Tutorial 02: 모니터링 스택 설치 (Prometheus + Grafana + Loki)

> **관련 태스크:** plan/task01.md (1.3 ~ 1.4)

---

## Step 1: kube-prometheus-stack 설치

### 1.1 커스텀 values 파일 생성

아래 내용으로 `k8s/helm-values/prometheus-values.yaml` 파일을 생성한다.

```yaml
# k8s/helm-values/prometheus-values.yaml
prometheus:
  prometheusSpec:
    retention: 7d
    resources:
      requests:
        cpu: 200m
        memory: 512Mi
      limits:
        cpu: 500m
        memory: 1Gi
    # 모든 네임스페이스의 ServiceMonitor 수집
    serviceMonitorSelectorNilUsesHelmValues: false
    podMonitorSelectorNilUsesHelmValues: false
    # 단일 노드이므로 replica 1개
    replicas: 1
    storageSpec:
      volumeClaimTemplate:
        spec:
          accessModes: ["ReadWriteOnce"]
          resources:
            requests:
              storage: 10Gi

grafana:
  enabled: true
  adminPassword: "admin"  # 테스트 환경
  service:
    type: NodePort
    nodePort: 30080
  resources:
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      cpu: 200m
      memory: 256Mi
  # Loki 데이터소스는 Step 2에서 추가

alertmanager:
  alertmanagerSpec:
    resources:
      requests:
        cpu: 50m
        memory: 64Mi
      limits:
        cpu: 100m
        memory: 128Mi

# kube-state-metrics
kube-state-metrics:
  resources:
    requests:
      cpu: 50m
      memory: 64Mi
    limits:
      cpu: 100m
      memory: 128Mi

# node-exporter
prometheus-node-exporter:
  resources:
    requests:
      cpu: 50m
      memory: 32Mi
    limits:
      cpu: 100m
      memory: 64Mi
```

### 1.2 Helm 설치

```bash
helm install prometheus prometheus-community/kube-prometheus-stack \
  --namespace monitoring \
  --version 51.10.0 \
  -f k8s/helm-values/prometheus-values.yaml \
  --wait
```

### 1.3 설치 확인

```bash
# Pod 상태 확인
kubectl get pods -n monitoring

# Prometheus UI 접근 (port-forward)
kubectl port-forward -n monitoring svc/prometheus-kube-prometheus-prometheus 9090:9090 &

# Grafana UI 접근 (NodePort 30080 또는 port-forward)
kubectl port-forward -n monitoring svc/prometheus-grafana 3000:80 &
```

**확인 항목:**
- Prometheus UI (http://localhost:9090): Status > Targets에서 정상 수집 확인
- Grafana UI (http://localhost:3000): admin/admin으로 로그인

---

## Step 2: Loki Stack 설치

### 2.1 커스텀 values 파일 생성

```yaml
# k8s/helm-values/loki-values.yaml
loki:
  enabled: true
  persistence:
    enabled: true
    size: 10Gi
  resources:
    requests:
      cpu: 100m
      memory: 256Mi
    limits:
      cpu: 200m
      memory: 512Mi
  config:
    limits_config:
      reject_old_samples: true
      reject_old_samples_max_age: 168h  # 7일

promtail:
  enabled: true
  resources:
    requests:
      cpu: 50m
      memory: 64Mi
    limits:
      cpu: 100m
      memory: 128Mi

# Grafana는 kube-prometheus-stack에서 이미 설치했으므로 비활성화
grafana:
  enabled: false
```

### 2.2 Helm 설치

```bash
helm install loki grafana/loki-stack \
  --namespace monitoring \
  --version 2.10.2 \
  -f k8s/helm-values/loki-values.yaml \
  --wait
```

### 2.3 설치 확인

```bash
# Loki Pod 확인
kubectl get pods -n monitoring -l app=loki

# Promtail DaemonSet 확인
kubectl get pods -n monitoring -l app=promtail
```

---

## Step 3: Grafana에 Loki 데이터소스 추가

### 방법 A: Grafana UI에서 수동 추가

1. Grafana (http://localhost:3000) 접속
2. Configuration > Data Sources > Add data source
3. Loki 선택
4. URL: `http://loki:3100`
5. Save & Test

### 방법 B: ConfigMap으로 자동 추가

```yaml
# k8s/grafana-loki-datasource.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: grafana-loki-datasource
  namespace: monitoring
  labels:
    grafana_datasource: "1"
data:
  loki-datasource.yaml: |
    apiVersion: 1
    datasources:
      - name: Loki
        type: loki
        access: proxy
        url: http://loki:3100
        isDefault: false
        editable: true
```

```bash
kubectl apply -f k8s/grafana-loki-datasource.yaml

# Grafana Pod 재시작하여 데이터소스 반영
kubectl rollout restart deployment prometheus-grafana -n monitoring
```

### 확인

Grafana > Explore > 데이터소스를 Loki로 변경 > `{namespace="monitoring"}` 쿼리 실행 → 로그 표시 확인

---

## Step 4: 기본 동작 확인 체크리스트

- [ ] Prometheus UI에서 K8s 기본 지표 조회 가능 (예: `up`, `kube_pod_status_phase`)
- [ ] Grafana에서 기본 대시보드 표시 (Kubernetes / Compute Resources)
- [ ] Grafana에서 Loki 데이터소스를 통해 Pod 로그 조회 가능
- [ ] Alertmanager Pod 정상 Running

## 트러블슈팅

### Prometheus가 지표를 수집하지 않을 때
```bash
# ServiceMonitor 확인
kubectl get servicemonitor -A

# Prometheus 설정 reload
kubectl port-forward -n monitoring svc/prometheus-kube-prometheus-prometheus 9090:9090
# http://localhost:9090/-/reload (POST)
```

### Loki에 로그가 보이지 않을 때
```bash
# Promtail 로그 확인
kubectl logs -n monitoring -l app=promtail --tail=50

# Loki 헬스체크
kubectl exec -n monitoring deploy/loki -- wget -qO- http://localhost:3100/ready
```

## 다음 단계

- [03-kafka-setup.md](03-kafka-setup.md): Strimzi + Kafka 클러스터 설치
