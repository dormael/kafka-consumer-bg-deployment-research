# Tutorial 03: Strimzi Operator + Kafka 클러스터 설치

> **관련 태스크:** plan/task01.md (1.5 ~ 1.7, 1.9)

---

## Step 1: Strimzi Operator 설치

### 1.1 커스텀 values 파일 생성

```yaml
# k8s/helm-values/strimzi-values.yaml
# Strimzi Operator 0.43.0
watchNamespaces:
  - kafka
  - kafka-bg-test
resources:
  requests:
    cpu: 200m
    memory: 384Mi
  limits:
    cpu: 500m
    memory: 512Mi
```

### 1.2 Helm 설치

```bash
helm install strimzi strimzi/strimzi-kafka-operator \
  --namespace kafka \
  --version 0.43.0 \
  -f k8s/helm-values/strimzi-values.yaml \
  --wait
```

### 1.3 확인

```bash
# Operator Pod 확인
kubectl get pods -n kafka -l name=strimzi-cluster-operator

# CRD 설치 확인
kubectl get crd | grep kafka
```

**예상 출력:**
```
kafkabridges.kafka.strimzi.io
kafkaconnectors.kafka.strimzi.io
kafkaconnects.kafka.strimzi.io
kafkamirrormaker2s.kafka.strimzi.io
kafkamirrormakers.kafka.strimzi.io
kafkanodepools.kafka.strimzi.io
kafkarebalances.kafka.strimzi.io
kafkas.kafka.strimzi.io
kafkatopics.kafka.strimzi.io
kafkausers.kafka.strimzi.io
```

---

## Step 2: Kafka 클러스터 배포 (KRaft 모드)

### 2.1 Kafka 클러스터 매니페스트 생성

```yaml
# k8s/kafka-cluster.yaml
apiVersion: kafka.strimzi.io/v1beta2
kind: Kafka
metadata:
  name: kafka-cluster
  namespace: kafka
  annotations:
    strimzi.io/kraft: "enabled"
    strimzi.io/node-pools: "enabled"
spec:
  kafka:
    version: 3.8.0
    listeners:
      - name: plain
        port: 9092
        type: internal
        tls: false
      - name: tls
        port: 9093
        type: internal
        tls: true
    config:
      offsets.topic.replication.factor: 1
      transaction.state.log.replication.factor: 1
      transaction.state.log.min.isr: 1
      default.replication.factor: 1
      min.insync.replicas: 1
      # Consumer Group 관련 설정
      group.min.session.timeout.ms: 6000
      group.max.session.timeout.ms: 300000
    metricsConfig:
      type: jmxPrometheusExporter
      valueFrom:
        configMapKeyRef:
          name: kafka-metrics
          key: kafka-metrics-config.yml
    resources:
      requests:
        cpu: 500m
        memory: 1Gi
      limits:
        cpu: 1000m
        memory: 2Gi
    storage:
      type: persistent-claim
      size: 20Gi
  # KRaft Controller (ZooKeeper 대체)
  entityOperator:
    topicOperator:
      resources:
        requests:
          cpu: 100m
          memory: 256Mi
        limits:
          cpu: 200m
          memory: 384Mi
    userOperator:
      resources:
        requests:
          cpu: 100m
          memory: 256Mi
        limits:
          cpu: 200m
          memory: 384Mi
  kafkaExporter:
    topicRegex: "bg-.*"
    groupRegex: "bg-.*"
    resources:
      requests:
        cpu: 50m
        memory: 64Mi
      limits:
        cpu: 100m
        memory: 128Mi
---
# KRaft Node Pool (Controller + Broker 겸용, 단일 노드)
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaNodePool
metadata:
  name: dual-role
  namespace: kafka
  labels:
    strimzi.io/cluster: kafka-cluster
spec:
  replicas: 1
  roles:
    - controller
    - broker
  storage:
    type: persistent-claim
    size: 20Gi
```

### 2.2 JMX Exporter 설정 ConfigMap

```yaml
# k8s/kafka-metrics-configmap.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kafka-metrics
  namespace: kafka
data:
  kafka-metrics-config.yml: |
    lowercaseOutputName: true
    lowercaseOutputLabelNames: true
    rules:
      # Kafka Broker 지표
      - pattern: kafka.server<type=(.+), name=(.+), clientId=(.+), topic=(.+), partition=(.*)><>Value
        name: kafka_server_$1_$2
        type: GAUGE
        labels:
          clientId: "$3"
          topic: "$4"
          partition: "$5"
      - pattern: kafka.server<type=(.+), name=(.+), clientId=(.+), brokerHost=(.+), brokerPort=(.+)><>Value
        name: kafka_server_$1_$2
        type: GAUGE
        labels:
          clientId: "$3"
          broker: "$4:$5"
      - pattern: kafka.server<type=(.+), name=(.+)><>Value
        name: kafka_server_$1_$2
        type: GAUGE
      - pattern: kafka.server<type=(.+), name=(.+)><>Count
        name: kafka_server_$1_$2_total
        type: COUNTER
      # Consumer Group 지표
      - pattern: kafka.coordinator.group<type=(.+), name=(.+)><>Value
        name: kafka_coordinator_group_$1_$2
        type: GAUGE
```

### 2.3 배포

```bash
kubectl apply -f k8s/kafka-metrics-configmap.yaml
kubectl apply -f k8s/kafka-cluster.yaml
```

### 2.4 배포 상태 확인

```bash
# Kafka 클러스터 상태 확인 (Ready가 될 때까지 대기, 수 분 소요)
kubectl get kafka -n kafka -w

# Pod 상태 확인
kubectl get pods -n kafka

# Kafka 클러스터가 Ready 상태인지 확인
kubectl wait kafka/kafka-cluster --for=condition=Ready --timeout=300s -n kafka
```

**예상 Pod 목록:**
```
kafka-cluster-dual-role-0         1/1     Running
kafka-cluster-entity-operator-... 2/2     Running
kafka-cluster-kafka-exporter-...  1/1     Running
strimzi-cluster-operator-...      1/1     Running
```

---

## Step 3: 테스트 토픽 생성

```yaml
# k8s/kafka-topic.yaml
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaTopic
metadata:
  name: bg-test-topic
  namespace: kafka
  labels:
    strimzi.io/cluster: kafka-cluster
spec:
  partitions: 8
  replicas: 1
  config:
    retention.ms: "86400000"     # 1일
    min.insync.replicas: "1"
    cleanup.policy: "delete"
```

```bash
kubectl apply -f k8s/kafka-topic.yaml

# 토픽 확인
kubectl get kafkatopic -n kafka
```

---

## Step 4: Kafka 동작 테스트

### Producer 테스트

```bash
kubectl exec -n kafka kafka-cluster-dual-role-0 -it -- \
  bin/kafka-console-producer.sh \
    --bootstrap-server localhost:9092 \
    --topic bg-test-topic

# 메시지 입력 후 Ctrl+C
> hello world
> test message
```

### Consumer 테스트

```bash
kubectl exec -n kafka kafka-cluster-dual-role-0 -it -- \
  bin/kafka-console-consumer.sh \
    --bootstrap-server localhost:9092 \
    --topic bg-test-topic \
    --from-beginning \
    --timeout-ms 10000
```

**예상 출력:**
```
hello world
test message
```

---

## Step 5: Prometheus에서 Kafka 지표 확인

### ServiceMonitor 확인

```bash
# Strimzi가 자동으로 생성하는 PodMonitor 확인
kubectl get podmonitor -n kafka
```

### Prometheus 쿼리

Prometheus UI (http://localhost:9090)에서:
- `kafka_server_brokertopicmetrics_messagesin_total` → Broker 메시지 수신
- `kafka_consumergroup_lag` → Consumer Group Lag (Kafka Exporter)

---

## Step 6: KafkaConnect 클러스터 배포 (전략 E용)

### Blue Connect 클러스터

```yaml
# k8s/kafka-connect.yaml
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaConnect
metadata:
  name: connect-blue
  namespace: kafka
  annotations:
    strimzi.io/use-connector-resources: "true"
spec:
  version: 3.8.0
  replicas: 1
  bootstrapServers: kafka-cluster-kafka-bootstrap:9092
  config:
    group.id: connect-cluster-blue
    config.storage.topic: connect-configs-blue
    offset.storage.topic: connect-offsets-blue
    status.storage.topic: connect-status-blue
    config.storage.replication.factor: 1
    offset.storage.replication.factor: 1
    status.storage.replication.factor: 1
  resources:
    requests:
      cpu: 200m
      memory: 512Mi
    limits:
      cpu: 500m
      memory: 1Gi
  metricsConfig:
    type: jmxPrometheusExporter
    valueFrom:
      configMapKeyRef:
        name: kafka-metrics
        key: kafka-metrics-config.yml
---
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaConnect
metadata:
  name: connect-green
  namespace: kafka
  annotations:
    strimzi.io/use-connector-resources: "true"
spec:
  version: 3.8.0
  replicas: 1
  bootstrapServers: kafka-cluster-kafka-bootstrap:9092
  config:
    group.id: connect-cluster-green
    config.storage.topic: connect-configs-green
    offset.storage.topic: connect-offsets-green
    status.storage.topic: connect-status-green
    config.storage.replication.factor: 1
    offset.storage.replication.factor: 1
    status.storage.replication.factor: 1
  resources:
    requests:
      cpu: 200m
      memory: 512Mi
    limits:
      cpu: 500m
      memory: 1Gi
```

```bash
kubectl apply -f k8s/kafka-connect.yaml

# KafkaConnect 상태 확인
kubectl get kafkaconnect -n kafka
```

---

## 트러블슈팅

### Kafka Pod가 Pending 상태일 때
```bash
# PVC 확인
kubectl get pvc -n kafka

# 노드 리소스 확인
kubectl describe node kafka-bg-test | grep -A 10 "Allocated resources"
```

### KRaft 모드에서 부팅 실패 시
```bash
# Kafka 로그 확인
kubectl logs -n kafka kafka-cluster-dual-role-0 --tail=100
```

## 다음 단계

- [04-argo-rollouts-setup.md](04-argo-rollouts-setup.md): Argo Rollouts + KEDA 설치
