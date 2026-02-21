# Tutorial 09: 테스트 환경 정리 (Cleanup)

> **목적:** 테스트 완료 후 클러스터 리소스를 완전히 제거한다.

---

## 정리 순서

리소스 의존성 순서를 고려하여 역순으로 삭제한다.

### Step 1: 워크로드 정리

```bash
echo "=== Step 1: 워크로드 정리 ==="

# Switch Controller
kubectl delete -f k8s/switch-controller-deployment.yaml --ignore-not-found
kubectl delete -f k8s/switch-rbac.yaml --ignore-not-found

# Consumer Green (전략 C)
kubectl delete -f k8s/consumer-green-statefulset.yaml --ignore-not-found

# Consumer Blue (전략 C)
kubectl delete -f k8s/consumer-blue-statefulset.yaml --ignore-not-found

# Consumer (전략 B)
kubectl delete -f k8s/consumer-b-green-deployment.yaml --ignore-not-found
kubectl delete -f k8s/consumer-b-blue-deployment.yaml --ignore-not-found

# ConfigMap
kubectl delete -f k8s/consumer-configmap.yaml --ignore-not-found
kubectl delete -f k8s/consumer-strategy-b-configmap.yaml --ignore-not-found

# Producer
kubectl delete -f k8s/producer-deployment.yaml --ignore-not-found

echo "워크로드 정리 완료"
```

### Step 2: Kafka Connect 및 Connector 정리

```bash
echo "=== Step 2: Kafka Connect 정리 ==="

# KafkaConnector 삭제
kubectl delete kafkaconnector bg-sink-blue -n kafka --ignore-not-found
kubectl delete kafkaconnector bg-sink-green -n kafka --ignore-not-found

# KafkaConnect 클러스터 삭제
kubectl delete kafkaconnect connect-blue -n kafka --ignore-not-found
kubectl delete kafkaconnect connect-green -n kafka --ignore-not-found

echo "Kafka Connect 정리 완료"
```

### Step 3: Kafka 클러스터 정리

```bash
echo "=== Step 3: Kafka 클러스터 정리 ==="

# KafkaTopic 삭제
kubectl delete kafkatopic bg-test-topic -n kafka --ignore-not-found

# Kafka 클러스터 삭제
kubectl delete kafka kafka-cluster -n kafka --ignore-not-found

# PVC 삭제 (Kafka 데이터)
kubectl delete pvc -n kafka -l strimzi.io/name=kafka-cluster-kafka

echo "Kafka 클러스터 정리 완료"
```

### Step 4: Operator 및 인프라 정리

```bash
echo "=== Step 4: Operator 및 인프라 정리 ==="

# KEDA
helm uninstall keda -n keda 2>/dev/null || echo "KEDA already removed"

# Argo Rollouts
helm uninstall argo-rollouts -n argo-rollouts 2>/dev/null || echo "Argo Rollouts already removed"

# Strimzi Operator
helm uninstall strimzi -n kafka 2>/dev/null || echo "Strimzi already removed"

# Strimzi CRD 삭제 (Helm uninstall로 삭제되지 않는 경우)
kubectl get crd | grep kafka | awk '{print $1}' | xargs kubectl delete crd 2>/dev/null || true

echo "Operator 정리 완료"
```

### Step 5: 모니터링 스택 정리

```bash
echo "=== Step 5: 모니터링 스택 정리 ==="

# Loki Stack
helm uninstall loki -n monitoring 2>/dev/null || echo "Loki already removed"

# kube-prometheus-stack
helm uninstall prometheus -n monitoring 2>/dev/null || echo "Prometheus already removed"

# Prometheus CRD 삭제
kubectl get crd | grep -E "monitoring.coreos.com|prometheus" | awk '{print $1}' | xargs kubectl delete crd 2>/dev/null || true

# PVC 삭제 (Prometheus, Loki 데이터)
kubectl delete pvc -n monitoring --all

echo "모니터링 정리 완료"
```

### Step 6: 네임스페이스 삭제

```bash
echo "=== Step 6: 네임스페이스 삭제 ==="

kubectl delete namespace kafka-bg-test --ignore-not-found
kubectl delete namespace kafka --ignore-not-found
kubectl delete namespace argo-rollouts --ignore-not-found
kubectl delete namespace keda --ignore-not-found
kubectl delete namespace monitoring --ignore-not-found

echo "네임스페이스 삭제 완료"
```

### Step 7: 최종 확인

```bash
echo "=== 최종 확인 ==="

# 남아있는 리소스 확인
kubectl get all -A | grep -v kube-system | grep -v default

# CRD 확인
kubectl get crd | grep -E "kafka|argo|keda|monitoring"

# PVC 확인
kubectl get pvc -A

echo "=== 정리 완료 ==="
```

---

## 원클릭 정리 스크립트

위 모든 단계를 하나의 스크립트로 실행할 수 있다.

```bash
# tools/cleanup.sh
#!/bin/bash
set -e

echo "=== Kafka Consumer Blue/Green 테스트 환경 정리 시작 ==="
echo "경고: 모든 테스트 데이터가 삭제됩니다."
read -p "계속하시겠습니까? (y/N) " confirm
[ "$confirm" != "y" ] && echo "취소됨" && exit 0

# Step 1~7 순서대로 실행
# (위 내용 참조)

echo "=== 정리 완료 ==="
```

```bash
chmod +x tools/cleanup.sh
./tools/cleanup.sh
```
