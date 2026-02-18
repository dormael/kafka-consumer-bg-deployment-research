# Tutorial 01: Prometheus / Grafana 설치

> 이 튜토리얼은 kube-prometheus-stack을 사용하여 Prometheus, Grafana, AlertManager를 설치하는 방법을 안내합니다.

---

## 사전 요구사항

- Kubernetes 클러스터 (kubectl 접근 가능)
- Helm 3.x 설치됨
- 클러스터에 최소 4GB 이상의 가용 메모리

---

## Step 1: Helm repo 추가

```bash
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
```

## Step 2: Namespace 생성

```bash
kubectl create namespace monitoring
```

## Step 3: Values 파일 작성

아래 내용을 `infra/prometheus/values-prometheus.yaml`로 저장합니다.

```yaml
grafana:
  adminPassword: "admin"
  service:
    type: NodePort
    nodePort: 30300
  sidecar:
    dashboards:
      enabled: true
      searchNamespace: ALL
    datasources:
      enabled: true
      searchNamespace: ALL

prometheus:
  prometheusSpec:
    retention: 7d
    serviceMonitorSelectorNilUsesHelmValues: false
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
  alertmanagerSpec:
    storage:
      volumeClaimTemplate:
        spec:
          accessModes: ["ReadWriteOnce"]
          resources:
            requests:
              storage: 2Gi
```

## Step 4: 설치

```bash
helm install kube-prometheus prometheus-community/kube-prometheus-stack \
  -n monitoring \
  -f infra/prometheus/values-prometheus.yaml \
  --wait
```

설치에 2~5분 정도 소요됩니다.

## Step 5: 설치 확인

```bash
# 모든 Pod가 Running/Ready 상태인지 확인
kubectl get pods -n monitoring

# 예상 출력:
# NAME                                                     READY   STATUS    RESTARTS   AGE
# alertmanager-kube-prometheus-alertmanager-0               2/2     Running   0          2m
# kube-prometheus-grafana-xxx                               3/3     Running   0          2m
# kube-prometheus-kube-state-metrics-xxx                    1/1     Running   0          2m
# kube-prometheus-prometheus-node-exporter-xxx              1/1     Running   0          2m
# prometheus-kube-prometheus-prometheus-0                   2/2     Running   0          2m
# kube-prometheus-operator-xxx                              1/1     Running   0          2m
```

## Step 6: Grafana 접근

```bash
# 방법 1: NodePort (클러스터 외부 접근 가능한 경우)
# 브라우저에서 http://<node-ip>:30300 접근

# 방법 2: Port-forward (로컬 개발 환경)
kubectl port-forward svc/kube-prometheus-grafana -n monitoring 3000:80
# 브라우저에서 http://localhost:3000 접근
# 로그인: admin / admin
```

## Step 7: Prometheus 접근

```bash
kubectl port-forward svc/kube-prometheus-prometheus -n monitoring 9090:9090
# 브라우저에서 http://localhost:9090 접근
```

## Step 8: 연동 확인

1. **Grafana → Prometheus 데이터소스**: Grafana UI → Configuration → Data Sources → Prometheus가 이미 추가되어 있어야 함
2. **Prometheus Targets**: Prometheus UI → Status → Targets에서 수집 대상 목록 확인
3. **테스트 쿼리**: Prometheus UI에서 `up` 쿼리 실행 → 결과가 표시되면 정상

---

## 트러블슈팅

### PVC가 Pending 상태인 경우

StorageClass가 없거나 기본 StorageClass가 설정되지 않은 경우 발생합니다.

```bash
# StorageClass 확인
kubectl get sc

# 기본 StorageClass가 없으면 설정
kubectl patch sc <storage-class-name> -p '{"metadata":{"annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}'
```

또는 values 파일에서 storage를 emptyDir로 변경합니다 (데이터 유실 주의):

```yaml
prometheus:
  prometheusSpec:
    storageSpec: {}  # emptyDir 사용
```

### 리소스 부족 시

```yaml
prometheus:
  prometheusSpec:
    resources:
      requests:
        cpu: 200m
        memory: 512Mi
      limits:
        cpu: 500m
        memory: 1Gi
```

---

## 다음 단계

- [Tutorial 02: Loki 설치](02-loki-setup.md) — 로그 수집 환경 구성
- [Tutorial 03: Kafka Cluster 설치](03-kafka-cluster-setup.md) — Strimzi로 Kafka 설치
