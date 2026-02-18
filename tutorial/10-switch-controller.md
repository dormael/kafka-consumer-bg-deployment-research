# Tutorial 10: Switch Controller 빌드 및 배포

> Go 기반 Switch Controller를 빌드하고 Kubernetes에 배포합니다.

---

## Step 1: 빌드

```bash
cd apps/switch-controller
go build -o switch-controller .
```

## Step 2: Docker 이미지 빌드

```bash
docker build -t bg-switch-controller:latest .

# Kind 사용 시
kind load docker-image bg-switch-controller:latest --name <cluster-name>
```

## Step 3: Lease 및 RBAC 적용

```bash
kubectl apply -f k8s/lease.yaml
kubectl apply -f k8s/rbac.yaml
```

## Step 4: 배포

```bash
kubectl apply -f k8s/deployment.yaml

kubectl rollout status deployment/bg-switch-controller -n kafka
```

## Step 5: 동작 확인

```bash
kubectl port-forward svc/bg-switch-controller -n kafka 8090:8090

# 상태 확인
curl http://localhost:8090/status | jq .

# 헬스체크
curl http://localhost:8090/health

# 전환 테스트 (주의: 실제 전환이 수행됨)
curl -X POST http://localhost:8090/switch

# 롤백
curl -X POST http://localhost:8090/rollback
```

## Step 6: 지표 확인

```bash
curl http://localhost:8090/metrics | grep bg_switch
# bg_switch_duration_seconds
# bg_switch_total
# bg_switch_failures_total
```

---

## 다음 단계

- [Tutorial 11: Validator 스크립트](11-validator-script.md)
