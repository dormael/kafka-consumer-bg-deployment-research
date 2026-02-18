# Task 12: Grafana 대시보드 구성

**Phase:** 2 - 애플리케이션 구현
**의존성:** task01 (Grafana), task07/08 (Producer/Consumer 지표)
**예상 소요:** 1시간 ~ 2시간
**튜토리얼:** `tutorial/12-grafana-dashboard.md`

---

## 목표

테스트 중 실시간 확인을 위한 Grafana 대시보드를 JSON 모델로 작성하고, ConfigMap으로 자동 프로비저닝한다.

---

## 대시보드 구조

kickoff-prompt.md에서 정의한 4개 Row로 구성한다.

### Row 1: Blue/Green 상태 개요

| 패널 | 타입 | 쿼리 |
|------|------|------|
| Active Version Badge | Stat | `bg_consumer_lifecycle_state{color=~"blue\|green"}` |
| Blue Pod Count (Running/Ready) | Stat | `kube_statefulset_status_replicas_ready{statefulset="consumer-blue"}` |
| Green Pod Count (Running/Ready) | Stat | `kube_statefulset_status_replicas_ready{statefulset="consumer-green"}` |
| 전환 상태 | Stat | `bg_switch_duration_seconds` (마지막 전환 소요시간) |

### Row 2: Consumer Lag 비교

| 패널 | 타입 | 쿼리 |
|------|------|------|
| Blue Consumer Lag 시계열 | Time Series | `sum(kafka_consumergroup_lag{consumergroup=~".*blue.*"}) by (topic)` |
| Green Consumer Lag 시계열 | Time Series | `sum(kafka_consumergroup_lag{consumergroup=~".*green.*"}) by (topic)` |
| 파티션별 Lag 히트맵 | Heatmap | `kafka_consumergroup_lag{topic="bg-test-topic"} by (partition, consumergroup)` |

### Row 3: 처리 성능

| 패널 | 타입 | 쿼리 |
|------|------|------|
| Messages/sec (Blue vs Green) | Time Series | `rate(bg_consumer_messages_consumed_total[1m])` |
| Producer TPS | Time Series | `rate(bg_producer_messages_sent_total[1m])` |
| Consumer 에러율 | Time Series | `rate(bg_consumer_errors_total[1m])` |
| 평균 처리 지연시간 | Time Series | `bg_consumer_processing_latency_seconds` |

### Row 4: 전환 이벤트 타임라인

| 패널 | 타입 | 쿼리 |
|------|------|------|
| Rebalance 발생 횟수 | Time Series | `bg_consumer_rebalance_count_total` |
| Lifecycle 상태 변경 | Annotation | Loki 로그 기반: `{app="bg-switch-controller"} \|= "Switch"` |
| 전환/롤백 소요 시간 | Time Series | `bg_switch_duration_seconds` |
| 양쪽 동시 Active 감지 | Stat | `bg_dual_active_events_total` |

---

## Subtask 12-01: 대시보드 JSON 작성

`infra/grafana/dashboards/bg-test-dashboard.json` 파일로 작성한다.

## Subtask 12-02: ConfigMap으로 프로비저닝

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: grafana-bg-test-dashboard
  namespace: monitoring
  labels:
    grafana_dashboard: "1"   # Grafana sidecar가 자동 인식
data:
  bg-test-dashboard.json: |
    { ... 대시보드 JSON ... }
```

## Subtask 12-03: Annotation 쿼리 설정

Grafana Annotation을 Loki 로그 기반으로 설정하여 전환 이벤트를 타임라인에 표시한다.

---

## 완료 기준

- [ ] Grafana에 "Kafka BG Test" 대시보드가 자동 프로비저닝됨
- [ ] 4개 Row의 모든 패널이 데이터를 표시함
- [ ] Producer/Consumer 지표가 실시간으로 업데이트됨
- [ ] Consumer Lag이 파티션별로 조회 가능
- [ ] 전환 이벤트가 Annotation으로 타임라인에 표시됨
