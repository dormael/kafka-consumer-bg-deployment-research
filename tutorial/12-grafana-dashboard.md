# Tutorial 12: Grafana 대시보드 구성

> Blue/Green 전환 모니터링을 위한 Grafana 대시보드를 배포합니다.

---

## Step 1: 대시보드 ConfigMap 배포

```bash
kubectl apply -f infra/grafana/dashboards/bg-test-dashboard-configmap.yaml
```

Grafana sidecar가 자동으로 대시보드를 프로비저닝합니다 (1~2분 소요).

## Step 2: Grafana 접근

```bash
kubectl port-forward svc/kube-prometheus-grafana -n monitoring 3000:80
# http://localhost:3000
# Dashboards → Kafka BG Test
```

## Step 3: 대시보드 확인

4개 Row가 모두 데이터를 표시하는지 확인합니다:

1. **Row 1**: Blue/Green 상태 개요 — Active Version, Pod Count
2. **Row 2**: Consumer Lag 비교 — Blue/Green Lag 시계열
3. **Row 3**: 처리 성능 — Messages/sec, 에러율
4. **Row 4**: 전환 이벤트 — Rebalance 횟수, 전환 시간

---

## 다음 단계

- [Tutorial 13: Alert Rules 구성](13-alert-rules.md)
