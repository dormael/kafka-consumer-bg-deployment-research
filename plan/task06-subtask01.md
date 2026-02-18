# Task 06: KEDA 설치 (선택)

**Phase:** 1 - 인프라 셋업
**의존성:** task03 (Kafka Cluster 필요)
**예상 소요:** 15분 ~ 30분
**튜토리얼:** `tutorial/06-keda-setup.md`
**우선순위:** 선택 (핵심 검증 범위 밖)

---

## 목표

KEDA를 설치하여 Consumer Lag 기반 자동 스케일링을 선택적으로 검증할 수 있는 환경을 준비한다.

---

## Subtask 06-01: KEDA 설치

```bash
helm repo add kedacore https://kedacore.github.io/charts
helm repo update

kubectl create namespace keda

helm install keda kedacore/keda \
  -n keda
```

## Subtask 06-02: ScaledObject 준비 (적용은 Phase 3에서)

```yaml
# keda-kafka-scaledobject.yaml (템플릿)
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: consumer-green-scaler
  namespace: kafka
spec:
  scaleTargetRef:
    name: consumer-green   # 대상 Deployment/StatefulSet 이름
  minReplicaCount: 2
  maxReplicaCount: 8
  triggers:
    - type: kafka
      metadata:
        bootstrapServers: bg-test-cluster-kafka-bootstrap.kafka:9092
        consumerGroup: bg-test-consumer-group
        topic: bg-test-topic
        lagThreshold: "100"
```

## Subtask 06-03: 설치 확인

```bash
kubectl get pods -n keda
kubectl get scaledobjects -A
```

---

## 완료 기준

- [ ] KEDA Operator가 정상 기동됨
- [ ] `kubectl get scaledobjects` 명령이 동작함
- [ ] ScaledObject 템플릿이 준비됨 (실제 적용은 Phase 3)
