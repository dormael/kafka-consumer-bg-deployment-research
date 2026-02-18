# Tutorial 03: Kafka Cluster 설치 (Strimzi Operator)

> Strimzi Operator를 설치하고, 검증용 Kafka Cluster(3 Broker)를 생성합니다. JMX Exporter와 Kafka Exporter를 포함하여 Prometheus 지표 수집을 설정합니다.

---

## 사전 요구사항

- Tutorial 01 완료 (Prometheus 설치됨)
- 클러스터에 최소 6GB 이상의 가용 메모리 (Kafka Broker 3개 + Zookeeper 3개)

---

## Step 1: Strimzi Operator 설치

```bash
helm repo add strimzi https://strimzi.io/charts/
helm repo update

kubectl create namespace kafka

helm install strimzi-operator strimzi/strimzi-kafka-operator \
  -n kafka \
  --set watchNamespaces="{kafka}" \
  --wait
```

## Step 2: Kafka Metrics ConfigMap 생성

JMX Exporter 설정입니다. `infra/kafka/kafka-metrics-config.yaml`로 저장합니다.

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kafka-metrics-config
  namespace: kafka
data:
  kafka-metrics-config.yml: |
    lowercaseOutputName: true
    rules:
      - pattern: kafka.server<type=(.+), name=(.+), clientId=(.+), topic=(.+), partition=(.*)><>Value
        name: kafka_server_$1_$2
        type: GAUGE
        labels:
          clientId: "$3"
          topic: "$4"
          partition: "$5"
      - pattern: kafka.server<type=(.+), name=(.+), clientId=(.+), topic=(.+)><>Value
        name: kafka_server_$1_$2
        type: GAUGE
        labels:
          clientId: "$3"
          topic: "$4"
      - pattern: kafka.server<type=(.+), name=(.+)><>(Count|Value)
        name: kafka_server_$1_$2
        type: GAUGE
      - pattern: kafka.server<type=group-coordinator-metrics, name=(.+)><>Value
        name: kafka_server_group_coordinator_$1
        type: GAUGE
      - pattern: kafka.controller<type=(.+), name=(.+)><>(Count|Value)
        name: kafka_controller_$1_$2
        type: GAUGE

  zookeeper-metrics-config.yml: |
    lowercaseOutputName: true
    rules:
      - pattern: "org.apache.ZooKeeperService<name0=ReplicatedServer_id(\\d+)><>(\\w+)"
        name: "zookeeper_$2"
        type: GAUGE
```

```bash
kubectl apply -f infra/kafka/kafka-metrics-config.yaml
```

## Step 3: Kafka Cluster CRD 배포

`infra/kafka/kafka-cluster.yaml`로 저장합니다.

```yaml
apiVersion: kafka.strimzi.io/v1beta2
kind: Kafka
metadata:
  name: bg-test-cluster
  namespace: kafka
spec:
  kafka:
    version: 3.8.0
    replicas: 3
    listeners:
      - name: plain
        port: 9092
        type: internal
        tls: false
    config:
      offsets.topic.replication.factor: 3
      transaction.state.log.replication.factor: 3
      transaction.state.log.min.isr: 2
      default.replication.factor: 3
      min.insync.replicas: 2
      group.min.session.timeout.ms: 6000
      group.max.session.timeout.ms: 300000
    storage:
      type: jbod
      volumes:
        - id: 0
          type: persistent-claim
          size: 10Gi
          deleteClaim: true
    metricsConfig:
      type: jmxPrometheusExporter
      valueFrom:
        configMapKeyRef:
          name: kafka-metrics-config
          key: kafka-metrics-config.yml
    resources:
      requests:
        cpu: 500m
        memory: 1Gi
      limits:
        cpu: "1"
        memory: 2Gi

  zookeeper:
    replicas: 3
    storage:
      type: persistent-claim
      size: 5Gi
      deleteClaim: true
    metricsConfig:
      type: jmxPrometheusExporter
      valueFrom:
        configMapKeyRef:
          name: kafka-metrics-config
          key: zookeeper-metrics-config.yml
    resources:
      requests:
        cpu: 200m
        memory: 512Mi
      limits:
        cpu: 500m
        memory: 1Gi

  entityOperator:
    topicOperator: {}
    userOperator: {}

  kafkaExporter:
    topicRegex: ".*"
    groupRegex: ".*"
    resources:
      requests:
        cpu: 200m
        memory: 64Mi
```

```bash
kubectl apply -f infra/kafka/kafka-cluster.yaml
```

생성에 3~5분 소요됩니다. 진행 상황을 확인합니다:

```bash
# Kafka 클러스터 상태 실시간 확인
kubectl get kafka bg-test-cluster -n kafka -w

# 모든 Pod 상태 확인
kubectl get pods -n kafka -w
```

`READY`가 `True`가 되면 클러스터 생성이 완료된 것입니다.

## Step 4: 테스트 토픽 생성

```yaml
# infra/kafka/test-topic.yaml
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaTopic
metadata:
  name: bg-test-topic
  namespace: kafka
  labels:
    strimzi.io/cluster: bg-test-cluster
spec:
  partitions: 8
  replicas: 3
  config:
    retention.ms: 604800000
    min.insync.replicas: 2
```

```bash
kubectl apply -f infra/kafka/test-topic.yaml
```

## Step 5: 토픽 확인

```bash
# 토픽 목록 확인
kubectl run kafka-test -n kafka --rm -it \
  --image=quay.io/strimzi/kafka:latest-kafka-3.8.0 -- \
  bin/kafka-topics.sh --bootstrap-server bg-test-cluster-kafka-bootstrap:9092 --list

# 토픽 상세 정보
kubectl run kafka-test -n kafka --rm -it \
  --image=quay.io/strimzi/kafka:latest-kafka-3.8.0 -- \
  bin/kafka-topics.sh --bootstrap-server bg-test-cluster-kafka-bootstrap:9092 \
  --describe --topic bg-test-topic
```

예상 출력:
```
Topic: bg-test-topic    PartitionCount: 8    ReplicationFactor: 3
    Partition: 0    Leader: 0    Replicas: 0,1,2    Isr: 0,1,2
    Partition: 1    Leader: 1    Replicas: 1,2,0    Isr: 1,2,0
    ...
```

## Step 6: Produce/Consume 테스트

```bash
# Producer 테스트
kubectl run kafka-producer -n kafka --rm -it \
  --image=quay.io/strimzi/kafka:latest-kafka-3.8.0 -- \
  bin/kafka-console-producer.sh --bootstrap-server bg-test-cluster-kafka-bootstrap:9092 \
  --topic bg-test-topic

# (다른 터미널에서) Consumer 테스트
kubectl run kafka-consumer -n kafka --rm -it \
  --image=quay.io/strimzi/kafka:latest-kafka-3.8.0 -- \
  bin/kafka-console-consumer.sh --bootstrap-server bg-test-cluster-kafka-bootstrap:9092 \
  --topic bg-test-topic --from-beginning
```

## Step 7: Prometheus 지표 수집 확인

```bash
# Prometheus에서 Kafka 관련 지표 확인
# Prometheus UI → http://localhost:9090
# 쿼리: kafka_server_BrokerTopicMetrics_MessagesInPerSec
# 쿼리: kafka_consumergroup_lag
```

Strimzi는 PodMonitor를 자동 생성할 수 있으나, kube-prometheus-stack 설정에 따라 수동 생성이 필요할 수 있습니다:

```yaml
# infra/kafka/kafka-podmonitor.yaml
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
  name: kafka-metrics
  namespace: kafka
  labels:
    release: kube-prometheus
spec:
  selector:
    matchLabels:
      strimzi.io/cluster: bg-test-cluster
  podMetricsEndpoints:
    - path: /metrics
      port: tcp-prometheus
```

```bash
kubectl apply -f infra/kafka/kafka-podmonitor.yaml
```

---

## 트러블슈팅

### Kafka Broker Pod가 CrashLoopBackOff인 경우

```bash
kubectl logs -n kafka bg-test-cluster-kafka-0 -c kafka
```

메모리 부족이 원인인 경우가 많습니다. Kafka Broker의 resources를 줄여보세요.

### Zookeeper 연결 실패

```bash
kubectl logs -n kafka bg-test-cluster-zookeeper-0
```

PVC가 정상 바인딩되었는지 확인합니다.

---

## 다음 단계

- [Tutorial 04: Argo Rollouts 설치](04-argo-rollouts-setup.md)
- [Tutorial 05: Strimzi KafkaConnect 구성](05-strimzi-kafkaconnect-setup.md)
