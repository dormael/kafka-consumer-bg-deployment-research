# Tutorial 07: Producer 앱 구현 및 배포

> Spring Boot 기반 테스트용 Kafka Producer를 빌드하고 Kubernetes에 배포합니다.

---

## Step 1: 프로젝트 빌드

```bash
cd apps/producer
mvn clean package -DskipTests
```

## Step 2: Docker 이미지 빌드

```bash
docker build -t bg-test-producer:latest .

# Kind 사용 시
kind load docker-image bg-test-producer:latest --name <cluster-name>

# Minikube 사용 시
minikube image load bg-test-producer:latest
```

## Step 3: Kubernetes 배포

```bash
kubectl apply -f k8s/deployment.yaml
kubectl apply -f k8s/service.yaml

# Pod Ready 대기
kubectl rollout status deployment/bg-test-producer -n kafka
```

## Step 4: 동작 확인

```bash
kubectl port-forward svc/bg-test-producer -n kafka 8080:8080

# 상태 확인
curl http://localhost:8080/producer/status

# 생성 시작
curl -X POST http://localhost:8080/producer/start

# TPS 변경
curl -X PUT "http://localhost:8080/producer/rate?ratePerSecond=100"

# 메시지 크기 변경
curl -X PUT "http://localhost:8080/producer/message-size?bytes=512"

# 중지
curl -X POST http://localhost:8080/producer/stop

# Prometheus 지표 확인
curl http://localhost:8080/actuator/prometheus | grep bg_producer
```

## Step 5: Kafka에서 메시지 확인

```bash
kubectl run kafka-consumer -n kafka --rm -it \
  --image=quay.io/strimzi/kafka:latest-kafka-3.8.0 -- \
  bin/kafka-console-consumer.sh --bootstrap-server bg-test-cluster-kafka-bootstrap:9092 \
  --topic bg-test-topic --from-beginning --max-messages 5
```

메시지에 `sequenceNumber` 필드가 포함되어 있는지 확인합니다.

---

## 다음 단계

- [Tutorial 08: Consumer 앱 구현](08-consumer-app.md)
