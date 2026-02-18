# Tutorial 13: Prometheus Alert Rules 구성

> 테스트 중 이상 상황 자동 감지를 위한 Alert Rules를 배포합니다.

---

## Step 1: PrometheusRule 배포

```bash
kubectl apply -f infra/prometheus/alert-rules.yaml
```

## Step 2: Prometheus에서 규칙 확인

```bash
kubectl port-forward svc/kube-prometheus-prometheus -n monitoring 9090:9090
# http://localhost:9090 → Alerts 탭
```

배포한 Alert Rules가 목록에 표시되어야 합니다:
- KafkaConsumerLagHigh
- DualActiveConsumers
- BGSwitchTooLong
- ConsumerErrorRateHigh
- ProducerSendErrors
- ConsumerPodNotReady

## Step 3: 테스트 발화

Chaos injection으로 알람을 테스트할 수 있습니다:

```bash
# Consumer 에러율 높이기 → ConsumerErrorRateHigh 발화
kubectl exec -n kafka consumer-blue-0 -c consumer -- \
  curl -s -X PUT "http://localhost:8080/chaos/error-rate?rate=0.5"

# 2분 대기 후 Prometheus Alerts 확인
# 확인 후 리셋
kubectl exec -n kafka consumer-blue-0 -c consumer -- \
  curl -s -X POST http://localhost:8080/chaos/reset
```

---

## 다음 단계

- [Tutorial 14: 시나리오 1 — 정상 전환 테스트](14-scenario1-normal-switch.md)
