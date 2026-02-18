# Tutorial 08: Consumer 앱 구현 및 배포

> Spring Boot 기반 테스트용 Kafka Consumer를 빌드하고 Kubernetes에 배포합니다. Lifecycle 엔드포인트, Rebalance 방어, Chaos Injection을 포함합니다.

---

## Step 1: 프로젝트 빌드

```bash
cd apps/consumer
mvn clean package -DskipTests
```

## Step 2: Docker 이미지 빌드

```bash
docker build -t bg-test-consumer:latest .

# Kind 사용 시
kind load docker-image bg-test-consumer:latest --name <cluster-name>
```

## Step 3: ConfigMap 생성 (Active Version)

```bash
kubectl apply -f k8s/configmap-active-version.yaml
```

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kafka-consumer-active-version
  namespace: kafka
data:
  active: "blue"
  previous-version: ""
  switch-timestamp: ""
  rollback-allowed: "true"
```

## Step 4: Blue StatefulSet 배포

```bash
kubectl apply -f k8s/service-blue.yaml
kubectl apply -f k8s/statefulset-blue.yaml

kubectl rollout status statefulset/consumer-blue -n kafka
```

## Step 5: Lifecycle 엔드포인트 테스트

```bash
kubectl port-forward pod/consumer-blue-0 -n kafka 8080:8080

# 상태 확인
curl http://localhost:8080/lifecycle/status | jq .
# {"state":"ACTIVE","assignedPartitions":[0,1],"pausedPartitions":[]}

# Pause 테스트
curl -X POST http://localhost:8080/lifecycle/pause | jq .
# {"status":"PAUSED"}

curl http://localhost:8080/lifecycle/status | jq .
# {"state":"PAUSED","assignedPartitions":[0,1],"pausedPartitions":[0,1]}

# Resume 테스트
curl -X POST http://localhost:8080/lifecycle/resume | jq .
# {"status":"ACTIVE"}
```

## Step 6: Chaos Injection 테스트

```bash
# 처리 지연 설정
curl -X PUT "http://localhost:8080/chaos/processing-delay?delayMs=200"

# 에러율 설정
curl -X PUT "http://localhost:8080/chaos/error-rate?rate=0.1"

# 상태 확인
curl http://localhost:8080/chaos/status | jq .

# 리셋
curl -X POST http://localhost:8080/chaos/reset
```

## Step 7: Green StatefulSet 준비 (replicas=0)

```bash
kubectl apply -f k8s/service-green.yaml
kubectl apply -f k8s/statefulset-green.yaml
# Green은 초기 replicas=0, 필요 시 스케일 업
```

## Step 8: 지표 확인

```bash
curl http://localhost:8080/actuator/prometheus | grep bg_consumer
# bg_consumer_messages_consumed_total
# bg_consumer_lifecycle_state
# bg_consumer_rebalance_count_total
# bg_consumer_errors_total
```

---

## 다음 단계

- [Tutorial 09: Switch Sidecar 구현](09-switch-sidecar.md)
- [Tutorial 10: Switch Controller 구현](10-switch-controller.md)
