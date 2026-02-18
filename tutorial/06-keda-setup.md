# Tutorial 06: KEDA 설치 (선택)

> KEDA를 설치하여 Consumer Lag 기반 자동 스케일링을 준비합니다. 핵심 검증 범위 밖이므로 선택적으로 진행합니다.

---

## Step 1: 설치

```bash
helm repo add kedacore https://kedacore.github.io/charts
helm repo update

kubectl create namespace keda

helm install keda kedacore/keda -n keda --wait
```

## Step 2: 확인

```bash
kubectl get pods -n keda
# keda-operator-xxx                 1/1     Running
# keda-metrics-apiserver-xxx        1/1     Running
```

## Step 3: ScaledObject 적용 (Phase 3에서)

Phase 3 테스트 시 필요에 따라 ScaledObject를 적용합니다. 템플릿은 `plan/task06-subtask01.md`를 참조하세요.

---

## 다음 단계

- [Tutorial 07: Producer 앱 구현](07-producer-app.md)
