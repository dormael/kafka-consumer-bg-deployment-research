# Tutorial 05: Strimzi KafkaConnect CRD 구성 (전략 E용)

> 전략 E 검증을 위해 Blue/Green 두 개의 KafkaConnect Cluster와 FileStreamSinkConnector를 구성합니다.

---

## 사전 요구사항

- Tutorial 03 완료 (Kafka Cluster 가동 중)

---

## Step 1: Connect Metrics ConfigMap 생성

```bash
kubectl apply -f infra/kafka/connect-metrics-config.yaml
```

## Step 2: Blue KafkaConnect Cluster 생성

```bash
kubectl apply -f infra/kafka/kafkaconnect-blue.yaml
```

## Step 3: Green KafkaConnect Cluster 생성

```bash
kubectl apply -f infra/kafka/kafkaconnect-green.yaml
```

## Step 4: Blue Connector 생성 (running)

```bash
kubectl apply -f infra/kafka/kafkaconnector-blue.yaml
```

## Step 5: Green Connector 생성 (stopped)

```bash
kubectl apply -f infra/kafka/kafkaconnector-green.yaml
```

## Step 6: 상태 확인

```bash
# KafkaConnect 클러스터 상태
kubectl get kafkaconnect -n kafka
# NAME            DESIRED REPLICAS   READY
# connect-blue    1                  True
# connect-green   1                  True

# KafkaConnector 상태
kubectl get kafkaconnector -n kafka
# NAME              CLUSTER         CONNECTOR CLASS                                          MAX TASKS   READY
# file-sink-blue    connect-blue    org.apache.kafka.connect.file.FileStreamSinkConnector    2           True
# file-sink-green   connect-green   org.apache.kafka.connect.file.FileStreamSinkConnector    2           True
```

## Step 7: REST API 직접 확인

```bash
kubectl port-forward svc/connect-blue-connect-api -n kafka 8083:8083

# Connector 목록
curl http://localhost:8083/connectors

# Blue Connector 상태
curl http://localhost:8083/connectors/file-sink-blue/status | jq .
# {"name":"file-sink-blue","connector":{"state":"RUNNING","worker_id":"..."},"tasks":[...]}
```

## Step 8: 전환 테스트 (CRD 방식)

```bash
# Blue를 stopped, Green을 running으로 전환
kubectl patch kafkaconnector file-sink-blue -n kafka --type merge -p '{"spec":{"state":"stopped"}}'
kubectl patch kafkaconnector file-sink-green -n kafka --type merge -p '{"spec":{"state":"running"}}'

# 상태 확인
kubectl get kafkaconnector -n kafka

# 롤백
kubectl patch kafkaconnector file-sink-green -n kafka --type merge -p '{"spec":{"state":"stopped"}}'
kubectl patch kafkaconnector file-sink-blue -n kafka --type merge -p '{"spec":{"state":"running"}}'
```

---

## 주의사항: REST API 직접 호출 vs CRD 충돌

Strimzi 환경에서는 **반드시 CRD의 `spec.state`를 통해 제어**해야 합니다. REST API를 직접 호출하면 Strimzi Operator가 CRD 상태로 덮어쓸 수 있습니다.

```
예시 (하지 말 것):
curl -X PUT http://connect:8083/connectors/file-sink-blue/pause
→ Strimzi Operator가 CRD의 state=running을 보고 다시 resume 시킴
```

이 동작을 확인하는 것 자체가 전략 E의 검증 포인트 중 하나입니다 (Strimzi Issue #3277).

---

## 다음 단계

- [Tutorial 06: KEDA 설치](06-keda-setup.md)
- [Tutorial 07: Producer 앱 구현](07-producer-app.md)
