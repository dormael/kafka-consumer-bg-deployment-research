# Task 05: Strimzi KafkaConnect CRD 구성 (전략 E용)

**Phase:** 1 - 인프라 셋업
**의존성:** task03 (Kafka Cluster 필요)
**예상 소요:** 30분 ~ 1시간
**튜토리얼:** `tutorial/05-strimzi-kafkaconnect-setup.md`

---

## 목표

전략 E(Kafka Connect REST API) 검증을 위해 Strimzi의 KafkaConnect CRD를 사용하여 Blue/Green 두 개의 Connect Cluster를 구성한다.

---

## Subtask 05-01: KafkaConnect CRD - Blue Cluster

```yaml
# kafkaconnect-blue.yaml
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaConnect
metadata:
  name: connect-blue
  namespace: kafka
  annotations:
    strimzi.io/use-connector-resources: "true"  # KafkaConnector CRD 사용 활성화
spec:
  version: 3.8.0
  replicas: 1    # 검증 환경에서는 1 replica로 충분
  bootstrapServers: bg-test-cluster-kafka-bootstrap:9092
  config:
    group.id: connect-cluster-blue
    config.storage.topic: connect-configs-blue
    offset.storage.topic: connect-offsets-blue
    status.storage.topic: connect-status-blue
    config.storage.replication.factor: 3
    offset.storage.replication.factor: 3
    status.storage.replication.factor: 3
    key.converter: org.apache.kafka.connect.storage.StringConverter
    value.converter: org.apache.kafka.connect.storage.StringConverter
  metricsConfig:
    type: jmxPrometheusExporter
    valueFrom:
      configMapKeyRef:
        name: connect-metrics-config
        key: connect-metrics-config.yml
  resources:
    requests:
      cpu: 200m
      memory: 512Mi
    limits:
      cpu: 500m
      memory: 1Gi
```

## Subtask 05-02: KafkaConnect CRD - Green Cluster

```yaml
# kafkaconnect-green.yaml
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
  bootstrapServers: bg-test-cluster-kafka-bootstrap:9092
  config:
    group.id: connect-cluster-green
    config.storage.topic: connect-configs-green
    offset.storage.topic: connect-offsets-green
    status.storage.topic: connect-status-green
    config.storage.replication.factor: 3
    offset.storage.replication.factor: 3
    status.storage.replication.factor: 3
    key.converter: org.apache.kafka.connect.storage.StringConverter
    value.converter: org.apache.kafka.connect.storage.StringConverter
  metricsConfig:
    type: jmxPrometheusExporter
    valueFrom:
      configMapKeyRef:
        name: connect-metrics-config
        key: connect-metrics-config.yml
  resources:
    requests:
      cpu: 200m
      memory: 512Mi
    limits:
      cpu: 500m
      memory: 1Gi
```

## Subtask 05-03: Connect Metrics ConfigMap

```yaml
# connect-metrics-config.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: connect-metrics-config
  namespace: kafka
data:
  connect-metrics-config.yml: |
    lowercaseOutputName: true
    rules:
      - pattern: kafka.connect<type=connect-worker-metrics><>(.+)
        name: kafka_connect_worker_$1
        type: GAUGE
      - pattern: kafka.connect<type=connector-metrics, connector=(.+)><>(.+)
        name: kafka_connect_connector_$2
        type: GAUGE
        labels:
          connector: "$1"
      - pattern: kafka.connect<type=task-metrics, connector=(.+), task=(.+)><>(.+)
        name: kafka_connect_task_$3
        type: GAUGE
        labels:
          connector: "$1"
          task: "$2"
```

## Subtask 05-04: FileStreamSinkConnector - Blue (running)

```yaml
# kafkaconnector-blue.yaml
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaConnector
metadata:
  name: file-sink-blue
  namespace: kafka
  labels:
    strimzi.io/cluster: connect-blue
spec:
  class: org.apache.kafka.connect.file.FileStreamSinkConnector
  tasksMax: 2
  state: running
  config:
    topics: bg-test-topic
    file: /tmp/sink-output-blue.txt
```

## Subtask 05-05: FileStreamSinkConnector - Green (stopped)

```yaml
# kafkaconnector-green.yaml
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaConnector
metadata:
  name: file-sink-green
  namespace: kafka
  labels:
    strimzi.io/cluster: connect-green
spec:
  class: org.apache.kafka.connect.file.FileStreamSinkConnector
  tasksMax: 2
  state: stopped    # 대기 상태
  config:
    topics: bg-test-topic
    file: /tmp/sink-output-green.txt
```

## Subtask 05-06: 설치 확인

```bash
# KafkaConnect Cluster 상태 확인
kubectl get kafkaconnect -n kafka

# KafkaConnector 상태 확인
kubectl get kafkaconnector -n kafka

# Blue Connector REST API 직접 확인 (port-forward)
kubectl port-forward svc/connect-blue-connect-api -n kafka 8083:8083
curl http://localhost:8083/connectors
curl http://localhost:8083/connectors/file-sink-blue/status
```

---

## 핵심 검증 포인트 (전략 E)

1. **CRD `spec.state` 변경으로 전환**: `kubectl patch kafkaconnector`로 state 변경 시 Strimzi Operator가 REST API를 대행하는지
2. **REST API 직접 호출 vs CRD 충돌**: Strimzi Issue #3277에서 보고된 reconcile 충돌 재현 확인
3. **config topic 기반 pause 영속성**: Connector pause 후 Worker 재시작해도 pause 상태 유지 확인

---

## 완료 기준

- [ ] Blue KafkaConnect Cluster가 Running 상태
- [ ] Green KafkaConnect Cluster가 Running 상태
- [ ] Blue FileStreamSinkConnector가 RUNNING 상태로 bg-test-topic을 소비 중
- [ ] Green FileStreamSinkConnector가 STOPPED 상태
- [ ] Connect Worker의 REST API(8083)에 접근 가능
- [ ] Connect JMX 지표가 Prometheus에 수집됨
