# Tutorial 01: Kubernetes 클러스터 기본 셋업

> **관련 태스크:** plan/task01.md (1.1 ~ 1.2)

---

## 사전 조건

- kubectl CLI가 설치되어 있고 K8s 클러스터에 접근 가능한 상태
- Helm v3.12+ 설치됨

## Step 1: 현재 클러스터 상태 확인

```bash
# K8s 버전 확인
kubectl version

# 노드 상태 확인
kubectl get nodes -o wide

# 노드 리소스 확인 (단일 노드이므로 리소스 제한 중요)
kubectl describe node kafka-bg-test | grep -A 5 "Allocatable"
```

**예상 결과:**
```
NAME            STATUS   ROLES                  AGE   VERSION
kafka-bg-test   Ready    control-plane,master   ...   v1.23.8
```

## Step 2: Helm Repo 추가

```bash
# 필요한 모든 Helm repo 추가
helm repo add strimzi https://strimzi.io/charts/
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo add grafana https://grafana.github.io/helm-charts
helm repo add argo https://argoproj.github.io/argo-helm
helm repo add kedacore https://kedacore.github.io/charts

# repo 인덱스 업데이트
helm repo update
```

**확인:**
```bash
helm repo list
```

## Step 3: 네임스페이스 생성

```bash
kubectl create namespace monitoring
kubectl create namespace kafka
kubectl create namespace argo-rollouts
kubectl create namespace keda
kubectl create namespace kafka-bg-test
```

**확인:**
```bash
kubectl get namespaces
```

**예상 결과:**
```
NAME              STATUS   AGE
default           Active   ...
kube-system       Active   ...
kube-public       Active   ...
monitoring        Active   ...
kafka             Active   ...
argo-rollouts     Active   ...
keda              Active   ...
kafka-bg-test     Active   ...
```

## Step 4: 리소스 제한 고려사항

단일 노드 클러스터이므로 전체 리소스 사용량을 관리해야 한다. 아래는 각 컴포넌트의 예상 리소스 사용량이다.

| 컴포넌트 | CPU 요청 | 메모리 요청 | CPU 상한 | 메모리 상한 |
|----------|----------|-------------|----------|-------------|
| Prometheus | 200m | 512Mi | 500m | 1Gi |
| Grafana | 100m | 128Mi | 200m | 256Mi |
| Alertmanager | 50m | 64Mi | 100m | 128Mi |
| Loki | 100m | 256Mi | 200m | 512Mi |
| Promtail | 50m | 64Mi | 100m | 128Mi |
| Strimzi Operator | 200m | 384Mi | 500m | 512Mi |
| Kafka Broker (1개) | 500m | 1Gi | 1000m | 2Gi |
| Kafka Exporter | 50m | 64Mi | 100m | 128Mi |
| Argo Rollouts | 100m | 128Mi | 200m | 256Mi |
| KEDA | 100m | 128Mi | 200m | 256Mi |
| **합계 (인프라)** | **~1450m** | **~2.7Gi** | | |
| Producer (1개) | 200m | 256Mi | 500m | 512Mi |
| Consumer Blue (3개) | 600m | 768Mi | 1500m | 1.5Gi |
| Consumer Green (3개) | 600m | 768Mi | 1500m | 1.5Gi |
| Switch Controller | 50m | 64Mi | 100m | 128Mi |
| Sidecar (6개) | 300m | 384Mi | 600m | 768Mi |
| **합계 (워크로드)** | **~1750m** | **~2.2Gi** | | |
| **총 합계** | **~3200m** | **~4.9Gi** | | |

노드의 Allocatable 리소스가 위 요구량을 충족하는지 확인해야 한다. 부족한 경우:
- Consumer/Producer 레플리카 수를 줄이거나
- 리소스 요청(requests)을 낮추어 오버커밋 허용

## 다음 단계

- [02-monitoring-setup.md](02-monitoring-setup.md): Prometheus + Grafana + Loki 설치
