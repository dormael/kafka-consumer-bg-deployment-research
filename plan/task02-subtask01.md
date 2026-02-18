# Task 02: Loki (로그 수집) 설치

**Phase:** 1 - 인프라 셋업
**의존성:** task01 (Grafana 데이터소스 추가 필요)
**예상 소요:** 20분 ~ 40분
**튜토리얼:** `tutorial/02-loki-setup.md`

---

## 목표

Grafana Loki를 설치하여 Consumer App, Switch Controller, Kafka Broker의 로그를 수집하고, Grafana에서 LogQL로 조회할 수 있는 환경을 구성한다.

---

## Subtask 02-01: Loki Helm chart 설치

```bash
helm repo add grafana https://grafana.github.io/helm-charts
helm repo update
```

**배포 모드 선택:** 검증 환경이므로 SingleBinary 모드(단일 Pod)를 사용한다.

```yaml
# values-loki.yaml
loki:
  auth_enabled: false
  commonConfig:
    replication_factor: 1
  storage:
    type: filesystem
  schemaConfig:
    configs:
      - from: "2024-01-01"
        store: tsdb
        object_store: filesystem
        schema: v13
        index:
          prefix: index_
          period: 24h

singleBinary:
  replicas: 1
  persistence:
    size: 10Gi

gateway:
  enabled: false

# 로그 수집 에이전트
promtail:
  enabled: true
```

```bash
helm install loki grafana/loki -n monitoring -f values-loki.yaml
```

## Subtask 02-02: Promtail 설치 (Loki chart에 포함되지 않는 경우)

Promtail이 Loki chart에 포함되지 않는 경우 별도로 설치한다.

```bash
helm install promtail grafana/promtail -n monitoring \
  --set "config.clients[0].url=http://loki:3100/loki/api/v1/push"
```

## Subtask 02-03: Grafana에 Loki 데이터소스 추가

kube-prometheus-stack의 Grafana에 Loki 데이터소스를 ConfigMap으로 추가한다.

```yaml
# grafana-loki-datasource.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: grafana-loki-datasource
  namespace: monitoring
  labels:
    grafana_datasource: "1"   # Grafana sidecar가 자동 인식
data:
  loki-datasource.yaml: |
    apiVersion: 1
    datasources:
      - name: Loki
        type: loki
        access: proxy
        url: http://loki:3100
        isDefault: false
```

```bash
kubectl apply -f grafana-loki-datasource.yaml -n monitoring
```

## Subtask 02-04: 설치 확인

```bash
# Loki Pod 상태 확인
kubectl get pods -n monitoring -l app.kubernetes.io/name=loki

# Promtail Pod 상태 확인 (DaemonSet)
kubectl get pods -n monitoring -l app.kubernetes.io/name=promtail

# Grafana에서 Loki 데이터소스 연결 확인
# Grafana UI → Configuration → Data Sources → Loki → Test
```

---

## 완료 기준

- [ ] Loki가 정상 기동됨
- [ ] Promtail이 모든 노드에서 로그를 수집하고 있음
- [ ] Grafana에서 Loki 데이터소스로 `{namespace="monitoring"}` 같은 기본 LogQL 쿼리가 동작함
- [ ] Grafana Explore 탭에서 로그 스트림 확인 가능
