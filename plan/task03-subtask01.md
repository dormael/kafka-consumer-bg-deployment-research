# Task 03: Kafka Cluster 설치 (Strimzi Operator)

**Phase:** 1 - 인프라 셋업
**의존성:** task01 (Prometheus 지표 수집)
**예상 소요:** 30분 ~ 1시간
**튜토리얼:** `tutorial/03-kafka-cluster-setup.md`

---

## 목표

Strimzi Operator를 설치하고, Kafka Cluster를 생성한다. 지표(JMX Exporter, Kafka Exporter)와 로그 수집 설정을 포함한다.

---

## Subtask 03-01: Strimzi Operator 설치

```bash
# Strimzi Helm repo 추가
helm repo add strimzi https://strimzi.io/charts/
helm repo update

# kafka namespace 생성
kubectl create namespace kafka

# Strimzi Operator 설치
helm install strimzi-operator strimzi/strimzi-kafka-operator \
  -n kafka \
  --set watchNamespaces="{kafka}"
```

## Subtask 03-02: Kafka Cluster CRD 작성 및 배포

검증 환경에 적합한 Kafka Cluster를 생성한다.

```yaml
# kafka-cluster.yaml
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
      - name: tls
        port: 9093
        type: internal
        tls: true
    config:
      offsets.topic.replication.factor: 3
      transaction.state.log.replication.factor: 3
      transaction.state.log.min.isr: 2
      default.replication.factor: 3
      min.insync.replicas: 2
      # Consumer Group 관련 설정 (검증에 중요)
      group.min.session.timeout.ms: 6000
      group.max.session.timeout.ms: 300000
    storage:
      type: jbod
      volumes:
        - id: 0
          type: persistent-claim
          size: 10Gi
          deleteClaim: true
    # JMX Exporter를 통한 지표 수집
    metricsConfig:
      type: jmxPrometheusExporter
      valueFrom:
        configMapKeyRef:
          name: kafka-metrics-config
          key: kafka-metrics-config.yml

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

  entityOperator:
    topicOperator: {}
    userOperator: {}

  # Kafka Exporter (Consumer Group Lag 지표용)
  kafkaExporter:
    topicRegex: ".*"
    groupRegex: ".*"
    resources:
      requests:
        cpu: 200m
        memory: 64Mi
```

## Subtask 03-03: Kafka Metrics ConfigMap 생성

JMX Exporter 설정을 위한 ConfigMap을 생성한다.

```yaml
# kafka-metrics-config.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kafka-metrics-config
  namespace: kafka
data:
  kafka-metrics-config.yml: |
    lowercaseOutputName: true
    rules:
      # Kafka Server 지표
      - pattern: kafka.server<type=(.+), name=(.+), clientId=(.+), topic=(.+), partition=(.*)><>Value
        name: kafka_server_$1_$2
        type: GAUGE
        labels:
          clientId: "$3"
          topic: "$4"
          partition: "$5"
      # Consumer Group 관련 지표
      - pattern: kafka.server<type=group-coordinator-metrics, name=(.+)><>Value
        name: kafka_server_group_coordinator_$1
        type: GAUGE
      # 기타 주요 지표
      - pattern: kafka.server<type=ReplicaManager, name=(.+)><>(Count|Value)
        name: kafka_server_replicamanager_$1
        type: GAUGE

  zookeeper-metrics-config.yml: |
    lowercaseOutputName: true
    rules:
      - pattern: "org.apache.ZooKeeperService<name0=ReplicatedServer_id(\\d+)><>(\\w+)"
        name: "zookeeper_$2"
        type: GAUGE
```

## Subtask 03-04: 테스트 토픽 생성

검증에 사용할 토픽을 생성한다.

```yaml
# test-topic.yaml
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaTopic
metadata:
  name: bg-test-topic
  namespace: kafka
  labels:
    strimzi.io/cluster: bg-test-cluster
spec:
  partitions: 8        # Consumer 4개 기준, 파티션당 2개씩 할당
  replicas: 3
  config:
    retention.ms: 604800000    # 7일
    min.insync.replicas: 2
```

## Subtask 03-05: ServiceMonitor / PodMonitor 확인

Strimzi가 자동 생성하는 PodMonitor(또는 직접 생성 필요한 경우 ServiceMonitor)가 Prometheus에 의해 수집되는지 확인한다.

```bash
# Strimzi가 생성한 PodMonitor 확인
kubectl get podmonitors -n kafka

# Prometheus Targets에서 Kafka 관련 타겟이 보이는지 확인
# Prometheus UI → Status → Targets → kafka 관련 타겟 검색
```

## Subtask 03-06: 설치 확인

```bash
# Kafka 클러스터 상태 확인
kubectl get kafka -n kafka
kubectl get pods -n kafka

# Kafka 내부 접속 테스트
kubectl run kafka-test -n kafka --rm -it --image=quay.io/strimzi/kafka:latest-kafka-3.8.0 -- \
  bin/kafka-topics.sh --bootstrap-server bg-test-cluster-kafka-bootstrap:9092 --list

# 토픽 상세 확인
kubectl run kafka-test -n kafka --rm -it --image=quay.io/strimzi/kafka:latest-kafka-3.8.0 -- \
  bin/kafka-topics.sh --bootstrap-server bg-test-cluster-kafka-bootstrap:9092 \
  --describe --topic bg-test-topic
```

---

## 완료 기준

- [ ] Strimzi Operator가 정상 기동됨
- [ ] Kafka Cluster (3 Broker)가 Running 상태
- [ ] Zookeeper (3 노드)가 Running 상태
- [ ] Kafka Exporter가 기동되어 Consumer Group Lag 지표를 수집 중
- [ ] JMX Exporter를 통한 Kafka Broker 지표가 Prometheus에 수집됨
- [ ] `bg-test-topic` 토픽이 8개 파티션으로 생성됨
- [ ] 토픽에 produce/consume 테스트 메시지 전송/수신 가능
