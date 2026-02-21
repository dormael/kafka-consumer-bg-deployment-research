# Tutorial 05: Producer/Consumer 앱 빌드 및 배포

> **관련 태스크:** plan/task02.md, plan/task03.md

---

## 사전 조건

- Java 17 JDK 설치
- Maven 3.8+ 설치
- Go 1.21+ 설치
- Docker 또는 containerd 사용 가능
- Kafka 클러스터 정상 동작 (Tutorial 03 완료)

## Step 1: Producer 앱 빌드

### 1.1 프로젝트 생성 및 빌드

```bash
cd apps/producer
mvn clean package -DskipTests
```

### 1.2 Docker 이미지 빌드

단일 노드 클러스터이므로 로컬 빌드 + `imagePullPolicy: Never` 사용.

```bash
cd apps/producer
docker build -t bg-test-producer:latest .
```

**minikube/kind 사용 시:**
```bash
# minikube의 경우
eval $(minikube docker-env)
docker build -t bg-test-producer:latest .

# kind의 경우
docker build -t bg-test-producer:latest .
kind load docker-image bg-test-producer:latest
```

### 1.3 K8s 배포

```bash
kubectl apply -f k8s/producer-deployment.yaml
```

### 1.4 동작 확인

```bash
# Pod 확인
kubectl get pods -n kafka-bg-test -l app=bg-producer

# 로그 확인
kubectl logs -n kafka-bg-test -l app=bg-producer --tail=20

# Producer 상태 확인
kubectl exec -n kafka-bg-test deploy/bg-producer -- \
  curl -s http://localhost:8080/producer/stats

# 생성률 변경 테스트
kubectl exec -n kafka-bg-test deploy/bg-producer -- \
  curl -s -X PUT http://localhost:8080/producer/config \
    -H "Content-Type: application/json" \
    -d '{"messagesPerSecond": 50, "messageSizeBytes": 1024}'
```

---

## Step 2: Consumer 앱 빌드

### 2.1 프로젝트 생성 및 빌드

```bash
cd apps/consumer
mvn clean package -DskipTests
```

### 2.2 Docker 이미지 빌드

```bash
cd apps/consumer
docker build -t bg-test-consumer:latest .
```

### 2.3 Blue Consumer 배포

```yaml
# k8s/consumer-blue-statefulset.yaml 의 핵심 설정:
# - KAFKA_GROUP_ID: bg-test-group
# - KAFKA_GROUP_INSTANCE_ID: ${POD_NAME} (Static Membership)
# - INITIAL_STATE: ACTIVE
```

```bash
kubectl apply -f k8s/consumer-configmap.yaml
kubectl apply -f k8s/consumer-blue-statefulset.yaml
```

### 2.4 Green Consumer 배포 (PAUSED 상태)

```yaml
# k8s/consumer-green-statefulset.yaml 의 핵심 설정:
# - KAFKA_GROUP_ID: bg-test-group (같은 Group - 전략 C)
# - KAFKA_GROUP_INSTANCE_ID: ${POD_NAME}
# - INITIAL_STATE: PAUSED
```

```bash
kubectl apply -f k8s/consumer-green-statefulset.yaml
```

### 2.5 Consumer 동작 확인

```bash
# Blue Consumer 상태 확인
kubectl exec -n kafka-bg-test consumer-blue-0 -c consumer -- \
  curl -s http://localhost:8080/lifecycle/status

# 예상 응답: {"state":"ACTIVE","containers":[...]}

# Green Consumer 상태 확인
kubectl exec -n kafka-bg-test consumer-green-0 -c consumer -- \
  curl -s http://localhost:8080/lifecycle/status

# 예상 응답: {"state":"PAUSED","containers":[...]}
```

### 2.6 Lifecycle 엔드포인트 테스트

```bash
# Blue Consumer Pause 테스트
kubectl exec -n kafka-bg-test consumer-blue-0 -c consumer -- \
  curl -s -X POST http://localhost:8080/lifecycle/pause

# 상태 확인 (PAUSED)
kubectl exec -n kafka-bg-test consumer-blue-0 -c consumer -- \
  curl -s http://localhost:8080/lifecycle/status

# Blue Consumer Resume 테스트
kubectl exec -n kafka-bg-test consumer-blue-0 -c consumer -- \
  curl -s -X POST http://localhost:8080/lifecycle/resume

# 상태 확인 (ACTIVE)
kubectl exec -n kafka-bg-test consumer-blue-0 -c consumer -- \
  curl -s http://localhost:8080/lifecycle/status
```

### 2.7 장애 주입 테스트

```bash
# 처리 지연 주입 (100ms)
kubectl exec -n kafka-bg-test consumer-blue-0 -c consumer -- \
  curl -s -X PUT http://localhost:8080/fault/processing-delay \
    -H "Content-Type: application/json" -d '{"delayMs": 100}'

# Consumer Lag 증가 관찰 (Prometheus)
# kafka_consumergroup_lag{consumergroup="bg-test-group"}

# 처리 지연 해제
kubectl exec -n kafka-bg-test consumer-blue-0 -c consumer -- \
  curl -s -X PUT http://localhost:8080/fault/processing-delay \
    -H "Content-Type: application/json" -d '{"delayMs": 0}'
```

---

## Step 3: Switch Controller 빌드 및 배포

### 3.1 빌드

```bash
cd apps/switch-controller
go build -o switch-controller ./cmd/controller/
docker build -t bg-switch-controller:latest .
```

### 3.2 배포

```bash
kubectl apply -f k8s/switch-rbac.yaml
kubectl apply -f k8s/switch-controller-deployment.yaml
```

### 3.3 확인

```bash
kubectl get pods -n kafka-bg-test -l app=switch-controller
kubectl logs -n kafka-bg-test -l app=switch-controller --tail=20
```

---

## Step 4: Switch Sidecar 빌드

### 4.1 빌드

```bash
cd apps/switch-sidecar
go build -o switch-sidecar ./cmd/sidecar/
docker build -t bg-switch-sidecar:latest .
```

Sidecar 이미지는 Consumer StatefulSet에 Sidecar 컨테이너로 포함되어 배포된다 (Step 2.3, 2.4에서 이미 포함됨).

---

## Step 5: 전체 시스템 동작 확인

### 5.1 Consumer Group 상태 확인

```bash
kubectl exec -n kafka kafka-cluster-dual-role-0 -- \
  bin/kafka-consumer-groups.sh \
    --bootstrap-server localhost:9092 \
    --group bg-test-group \
    --describe
```

**예상 출력:**
- Blue Consumer가 8개 파티션 중 일부를 소비 중
- Green Consumer가 나머지 파티션에 할당되었으나 PAUSED

### 5.2 Prometheus 지표 확인

Prometheus UI에서:
- `bg_producer_messages_sent_total` → Producer 발행 수
- `bg_consumer_messages_received_total` → Consumer 수신 수
- `bg_consumer_lifecycle_state` → Lifecycle 상태

### 5.3 Loki 로그 확인

Grafana > Explore > Loki:
- `{app="bg-producer"}` → Producer 로그
- `{app="bg-consumer", color="blue"}` → Blue Consumer 로그
- `{app="bg-consumer", color="green"}` → Green Consumer 로그

---

## Step 6: 전략 B 전용 Consumer 배포 (선택)

전략 B를 테스트하기 위해 별도 Consumer Group을 사용하는 Blue/Green을 추가 배포한다.

```bash
# 전략 B용 ConfigMap
kubectl apply -f k8s/consumer-strategy-b-configmap.yaml

# 전략 B용 Blue Consumer (group.id = bg-test-group-blue)
kubectl apply -f k8s/consumer-b-blue-deployment.yaml

# 전략 B용 Green Consumer (group.id = bg-test-group-green, replicas=0)
kubectl apply -f k8s/consumer-b-green-deployment.yaml
```

## 다음 단계

- [06-strategy-c-test.md](06-strategy-c-test.md): 전략 C 테스트 수행
