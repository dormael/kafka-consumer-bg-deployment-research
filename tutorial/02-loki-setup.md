# Tutorial 02: Loki (로그 수집) 설치

> Grafana Loki를 설치하여 Consumer, Switch Controller, Kafka Broker의 로그를 수집하고 Grafana에서 조회합니다.

---

## 사전 요구사항

- Tutorial 01 완료 (Grafana 설치됨)

---

## Step 1: Helm repo 추가

```bash
helm repo add grafana https://grafana.github.io/helm-charts
helm repo update
```

## Step 2: Loki Values 파일 작성

`infra/loki/values-loki.yaml`로 저장합니다.

```yaml
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

deploymentMode: SingleBinary

singleBinary:
  replicas: 1
  persistence:
    enabled: true
    size: 10Gi

chunksCache:
  enabled: false

resultsCache:
  enabled: false

gateway:
  enabled: false

# Promtail은 별도 설치
```

## Step 3: Loki 설치

```bash
helm install loki grafana/loki -n monitoring -f infra/loki/values-loki.yaml --wait
```

## Step 4: Promtail 설치

Promtail은 각 노드에서 로그를 수집하여 Loki로 전송하는 DaemonSet입니다.

```yaml
# infra/loki/values-promtail.yaml
config:
  clients:
    - url: http://loki:3100/loki/api/v1/push
```

```bash
helm install promtail grafana/promtail -n monitoring -f infra/loki/values-promtail.yaml --wait
```

## Step 5: Grafana에 Loki 데이터소스 추가

```yaml
# infra/loki/grafana-loki-datasource.yaml
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
```

```bash
kubectl apply -f infra/loki/grafana-loki-datasource.yaml
```

Grafana sidecar가 자동으로 감지하여 데이터소스를 추가합니다 (1~2분 소요).

## Step 6: 확인

```bash
# Loki Pod 상태
kubectl get pods -n monitoring -l app.kubernetes.io/name=loki

# Promtail DaemonSet 상태
kubectl get pods -n monitoring -l app.kubernetes.io/name=promtail

# Loki API 직접 테스트
kubectl port-forward svc/loki -n monitoring 3100:3100
curl http://localhost:3100/ready
# 응답: ready
```

## Step 7: Grafana에서 로그 조회 테스트

1. Grafana UI 접속 (http://localhost:3000)
2. 왼쪽 메뉴 → Explore 클릭
3. 상단 데이터소스를 "Loki"로 변경
4. LogQL 쿼리 입력: `{namespace="monitoring"}`
5. "Run query" 클릭 → 로그 스트림이 표시되면 정상

---

## 유용한 LogQL 쿼리 예시

```logql
# 특정 namespace의 모든 로그
{namespace="kafka"}

# 특정 앱의 로그
{namespace="kafka", app="bg-test-consumer"}

# 에러 로그만 필터
{namespace="kafka"} |= "ERROR"

# JSON 로그 파싱
{namespace="kafka"} | json | level="error"

# 시퀀스 번호 추출
{app="bg-test-producer"} |= "[SEQ:" | regexp `\[SEQ:(?P<seq>\d+)\]`
```

---

## 다음 단계

- [Tutorial 03: Kafka Cluster 설치](03-kafka-cluster-setup.md)
