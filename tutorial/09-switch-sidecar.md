# Tutorial 09: Switch Sidecar 빌드 및 배포

> Go 기반 Switch Sidecar를 빌드하고, Consumer StatefulSet의 Sidecar 컨테이너로 배포합니다.

---

## Step 1: 빌드

```bash
cd apps/switch-sidecar
go build -o switch-sidecar .
```

## Step 2: Docker 이미지 빌드

```bash
docker build -t bg-switch-sidecar:latest .

# Kind 사용 시
kind load docker-image bg-switch-sidecar:latest --name <cluster-name>
```

## Step 3: RBAC 적용

```bash
kubectl apply -f k8s/rbac.yaml
```

## Step 4: Consumer StatefulSet에 Sidecar 추가

Consumer StatefulSet의 `spec.template.spec.containers`에 Sidecar 컨테이너를 추가합니다. 이미 `statefulset-blue.yaml`에 포함되어 있으므로 별도 작업 불필요.

## Step 5: 동작 확인

```bash
# Sidecar 로그 확인
kubectl logs consumer-blue-0 -n kafka -c switch-sidecar

# ConfigMap 변경 테스트
kubectl patch configmap kafka-consumer-active-version -n kafka \
  --type merge -p '{"data":{"active":"green"}}'

# Sidecar 로그에서 pause 요청 확인
kubectl logs consumer-blue-0 -n kafka -c switch-sidecar --tail=10

# 복원
kubectl patch configmap kafka-consumer-active-version -n kafka \
  --type merge -p '{"data":{"active":"blue"}}'
```

---

## 다음 단계

- [Tutorial 10: Switch Controller 구현](10-switch-controller.md)
