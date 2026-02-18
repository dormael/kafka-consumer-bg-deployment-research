# Task 13: Prometheus Alert Rules 구성

**Phase:** 2 - 애플리케이션 구현
**의존성:** task01 (Prometheus), task07/08 (Producer/Consumer 지표)
**예상 소요:** 30분 ~ 1시간
**튜토리얼:** `tutorial/13-alert-rules.md`

---

## 목표

테스트 중 이상 상황을 자동 감지하기 위한 Prometheus Alert Rules를 PrometheusRule CRD로 구성한다.

---

## Alert Rules

```yaml
# alert-rules.yaml
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: bg-test-alerts
  namespace: kafka
  labels:
    release: kube-prometheus   # kube-prometheus-stack이 인식
spec:
  groups:
    - name: kafka-consumer-bluegreen
      rules:
        # 1. Consumer Lag 임계값 초과
        - alert: KafkaConsumerLagHigh
          expr: sum(kafka_consumergroup_lag{consumergroup=~"bg-test.*"}) by (consumergroup) > 500
          for: 2m
          labels:
            severity: critical
          annotations:
            summary: "Consumer Lag > 500"
            description: "{{ $labels.consumergroup }} lag: {{ $value }}"

        # 2. 양쪽 동시 Active 감지 (가장 Critical)
        - alert: DualActiveConsumers
          expr: |
            count(bg_consumer_lifecycle_state == 0) > 1
          for: 0s
          labels:
            severity: critical
          annotations:
            summary: "Blue/Green 양쪽 모두 ACTIVE 상태 감지"

        # 3. 전환 소요 시간 초과
        - alert: BGSwitchTooLong
          expr: bg_switch_duration_seconds > 30
          for: 0s
          labels:
            severity: warning
          annotations:
            summary: "전환 소요 시간 > 30초"

        # 4. Consumer 에러율 급증
        - alert: ConsumerErrorRateHigh
          expr: rate(bg_consumer_errors_total[2m]) > 0.01
          for: 1m
          labels:
            severity: warning
          annotations:
            summary: "Consumer 에러율 > 1%"

        # 5. Producer 전송 실패
        - alert: ProducerSendErrors
          expr: rate(bg_producer_send_errors_total[2m]) > 0
          for: 1m
          labels:
            severity: warning
          annotations:
            summary: "Producer 전송 에러 발생"

        # 6. Consumer Pod 미기동
        - alert: ConsumerPodNotReady
          expr: |
            kube_statefulset_status_replicas_ready{statefulset=~"consumer-.*"} <
            kube_statefulset_status_replicas{statefulset=~"consumer-.*"}
          for: 2m
          labels:
            severity: warning
          annotations:
            summary: "Consumer Pod가 Ready 상태가 아님"
```

---

## 완료 기준

- [ ] PrometheusRule CRD가 Prometheus에 로드됨
- [ ] Prometheus UI → Alerts에서 규칙 목록 확인 가능
- [ ] AlertManager에서 알람 수신 확인 (테스트 발화)
