# Tutorial 04: Argo Rollouts + KEDA 설치

> **관련 태스크:** plan/task01.md (1.8, 1.10)

---

## Step 1: Argo Rollouts Controller 설치

### 1.1 커스텀 values 파일 생성

```yaml
# k8s/helm-values/argo-rollouts-values.yaml
controller:
  resources:
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      cpu: 200m
      memory: 256Mi

dashboard:
  enabled: true
  service:
    type: NodePort
    nodePort: 30081
  resources:
    requests:
      cpu: 50m
      memory: 64Mi
    limits:
      cpu: 100m
      memory: 128Mi
```

### 1.2 Helm 설치

```bash
helm install argo-rollouts argo/argo-rollouts \
  --namespace argo-rollouts \
  --version 2.35.3 \
  -f k8s/helm-values/argo-rollouts-values.yaml \
  --wait
```

### 1.3 kubectl 플러그인 설치 (선택)

```bash
# Argo Rollouts kubectl 플러그인 설치
# https://argo-rollouts.readthedocs.io/en/stable/installation/#kubectl-plugin-installation
curl -LO https://github.com/argoproj/argo-rollouts/releases/download/v1.6.6/kubectl-argo-rollouts-linux-amd64
chmod +x kubectl-argo-rollouts-linux-amd64
sudo mv kubectl-argo-rollouts-linux-amd64 /usr/local/bin/kubectl-argo-rollouts
```

### 1.4 확인

```bash
# Controller Pod 확인
kubectl get pods -n argo-rollouts

# CRD 확인
kubectl get crd | grep argo

# (플러그인 설치 시) 버전 확인
kubectl argo rollouts version
```

**예상 CRD:**
```
analysisruns.argoproj.io
analysistemplates.argoproj.io
clusteranalysistemplates.argoproj.io
experiments.argoproj.io
rollouts.argoproj.io
```

### 1.5 Dashboard 접근

```bash
# NodePort (30081) 또는 port-forward
kubectl port-forward -n argo-rollouts svc/argo-rollouts-dashboard 3100:3100 &

# http://localhost:3100 접근
```

---

## Step 2: Argo Rollouts Blue/Green 테스트 (선택)

간단한 nginx 배포로 Argo Rollouts의 Blue/Green 전략이 정상 동작하는지 확인한다.

### 2.1 테스트용 Rollout 생성

```yaml
# k8s/test-rollout.yaml (테스트 후 삭제)
apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata:
  name: test-rollout
  namespace: kafka-bg-test
spec:
  replicas: 1
  revisionHistoryLimit: 2
  selector:
    matchLabels:
      app: test-rollout
  template:
    metadata:
      labels:
        app: test-rollout
    spec:
      containers:
        - name: nginx
          image: nginx:1.25
          ports:
            - containerPort: 80
  strategy:
    blueGreen:
      activeService: test-active-svc
      previewService: test-preview-svc
      autoPromotionEnabled: false
---
apiVersion: v1
kind: Service
metadata:
  name: test-active-svc
  namespace: kafka-bg-test
spec:
  selector:
    app: test-rollout
  ports:
    - port: 80
---
apiVersion: v1
kind: Service
metadata:
  name: test-preview-svc
  namespace: kafka-bg-test
spec:
  selector:
    app: test-rollout
  ports:
    - port: 80
```

```bash
kubectl apply -f k8s/test-rollout.yaml

# Rollout 상태 확인
kubectl argo rollouts get rollout test-rollout -n kafka-bg-test

# 정리
kubectl delete -f k8s/test-rollout.yaml
```

---

## Step 3: KEDA 설치 (선택)

### 3.1 커스텀 values 파일 생성

```yaml
# k8s/helm-values/keda-values.yaml
resources:
  operator:
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      cpu: 200m
      memory: 256Mi
  metricServer:
    requests:
      cpu: 50m
      memory: 64Mi
    limits:
      cpu: 100m
      memory: 128Mi
```

### 3.2 Helm 설치

```bash
helm install keda kedacore/keda \
  --namespace keda \
  --version 2.9.4 \
  -f k8s/helm-values/keda-values.yaml \
  --wait
```

### 3.3 확인

```bash
# KEDA Pod 확인
kubectl get pods -n keda

# CRD 확인
kubectl get crd | grep keda
```

**예상 CRD:**
```
scaledobjects.keda.sh
scaledjobs.keda.sh
triggerauthentications.keda.sh
clustertriggerauthentications.keda.sh
```

---

## Step 4: 전체 인프라 최종 확인

```bash
echo "=== monitoring ==="
kubectl get pods -n monitoring

echo "=== kafka ==="
kubectl get pods -n kafka

echo "=== argo-rollouts ==="
kubectl get pods -n argo-rollouts

echo "=== keda ==="
kubectl get pods -n keda

echo "=== kafka-bg-test ==="
kubectl get pods -n kafka-bg-test
```

모든 Pod가 Running/Ready 상태인지 확인한다.

---

## TODO: 다른 배포 도구 (향후 검토)

| 도구 | 설명 | 상태 |
|------|------|------|
| Flagger | Istio/Linkerd 서비스 메시 연동 Progressive Delivery | TODO |
| OpenKruise Rollout | CNCF 인큐베이팅, 더 유연한 롤아웃 전략 | TODO |
| Keptn | 자동 관찰 가능성 + 배포 오케스트레이션 | TODO |

## 다음 단계

- [05-producer-consumer-build.md](05-producer-consumer-build.md): Producer/Consumer 앱 빌드 및 배포
