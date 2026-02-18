# Tutorial 04: Argo Rollouts Controller 설치

> Argo Rollouts를 설치하고 kubectl 플러그인을 구성합니다.

---

## Step 1: 설치

```bash
kubectl create namespace argo-rollouts

helm repo add argo https://argoproj.github.io/argo-helm
helm repo update

helm install argo-rollouts argo/argo-rollouts \
  -n argo-rollouts \
  --set dashboard.enabled=true \
  --wait
```

## Step 2: kubectl 플러그인 설치

```bash
curl -LO https://github.com/argoproj/argo-rollouts/releases/latest/download/kubectl-argo-rollouts-linux-amd64
chmod +x ./kubectl-argo-rollouts-linux-amd64
sudo mv ./kubectl-argo-rollouts-linux-amd64 /usr/local/bin/kubectl-argo-rollouts

# 확인
kubectl argo rollouts version
```

## Step 3: Dashboard 접근

```bash
kubectl port-forward svc/argo-rollouts-dashboard -n argo-rollouts 3100:3100
# http://localhost:3100
```

## Step 4: 확인

```bash
kubectl get pods -n argo-rollouts
# argo-rollouts-xxx   1/1     Running
```

---

## 다음 단계

- [Tutorial 05: Strimzi KafkaConnect 구성](05-strimzi-kafkaconnect-setup.md)
