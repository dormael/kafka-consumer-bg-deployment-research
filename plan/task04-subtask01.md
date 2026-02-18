# Task 04: Argo Rollouts Controller 설치

**Phase:** 1 - 인프라 셋업
**의존성:** 없음
**예상 소요:** 15분 ~ 30분
**튜토리얼:** `tutorial/04-argo-rollouts-setup.md`

---

## 목표

Argo Rollouts Controller를 설치하고, kubectl 플러그인을 구성한다. 전략 B, C에서 Blue/Green 전환/롤백 오케스트레이션 보조 도구로 사용한다.

---

## Subtask 04-01: Argo Rollouts 설치

```bash
# namespace 생성
kubectl create namespace argo-rollouts

# Helm repo 추가
helm repo add argo https://argoproj.github.io/argo-helm
helm repo update

# Argo Rollouts 설치
helm install argo-rollouts argo/argo-rollouts \
  -n argo-rollouts \
  --set dashboard.enabled=true
```

## Subtask 04-02: kubectl 플러그인 설치

```bash
# kubectl argo rollouts 플러그인 설치
# Linux
curl -LO https://github.com/argoproj/argo-rollouts/releases/latest/download/kubectl-argo-rollouts-linux-amd64
chmod +x ./kubectl-argo-rollouts-linux-amd64
sudo mv ./kubectl-argo-rollouts-linux-amd64 /usr/local/bin/kubectl-argo-rollouts

# 설치 확인
kubectl argo rollouts version
```

## Subtask 04-03: Argo Rollouts Dashboard 접근 확인

```bash
# Dashboard port-forward
kubectl port-forward svc/argo-rollouts-dashboard -n argo-rollouts 3100:3100
# 브라우저에서 http://localhost:3100 접근
```

## Subtask 04-04: AnalysisTemplate 준비 (Prometheus 연동)

전환 전후 헬스체크에 사용할 AnalysisTemplate을 준비한다.

```yaml
# analysis-template-consumer-health.yaml
apiVersion: argoproj.io/v1alpha1
kind: AnalysisTemplate
metadata:
  name: kafka-consumer-health-check
  namespace: kafka
spec:
  args:
    - name: consumer-group
  metrics:
    - name: consumer-error-rate
      interval: 30s
      count: 5
      successCondition: result[0] < 0.01
      provider:
        prometheus:
          address: http://kube-prometheus-prometheus.monitoring:9090
          query: |
            rate(kafka_consumer_errors_total{
              consumer_group="{{args.consumer-group}}"
            }[2m])
    - name: consumer-lag
      interval: 30s
      count: 5
      successCondition: result[0] < 100
      provider:
        prometheus:
          address: http://kube-prometheus-prometheus.monitoring:9090
          query: |
            sum(kafka_consumergroup_lag{
              consumergroup="{{args.consumer-group}}"
            })
```

---

## 한계 및 유의사항

- Argo Rollouts는 Kafka 파티션 할당을 직접 제어하지 못함 (공식 문서 명시)
- Pod 생명주기 관리 + AnalysisTemplate 기반 헬스체크에만 사용
- Kafka 수준 전환(pause/resume, offset sync)은 Switch Controller가 별도 담당

## TODO (향후 검토)

- [ ] Flagger 비교 검증
- [ ] OpenKruise Rollout 비교 검증
- [ ] Keptn 비교 검증

---

## 완료 기준

- [ ] Argo Rollouts Controller가 정상 기동됨
- [ ] `kubectl argo rollouts version`으로 플러그인 동작 확인
- [ ] Argo Rollouts Dashboard 접근 가능
- [ ] AnalysisTemplate이 Prometheus와 연동 가능하도록 준비됨
