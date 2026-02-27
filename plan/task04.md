# Task 04: Approach C 테스트 — Rollouts Webhook + Consumer API 직접 호출 (경량)

> **의존:** Task 01 (Foundation)
> **차단:** Task 05 (비교 분석)

---

## 목표

Switch Controller/Sidecar 없이, Argo Rollouts의 prePromotionAnalysis 웹후크가 Consumer REST API를 **직접** 호출하여 Blue-Green 전환을 수행한다. Approach A와의 핵심 차이는 전용 Webhook Job 서비스 대신 **경량 curl Job 또는 간단한 스크립트**만 사용한다는 점이다.

---

## 아키텍처

```
Argo Rollouts (Rollout CR)
  ├── prePromotionAnalysis
  │   ├── Job: curl 기반 스크립트 → Active Pods /lifecycle/stop
  │   ├── Job: curl 기반 스크립트 → Preview Pods /lifecycle/start
  │   └── Prometheus: Consumer Lag 확인
  └── postPromotionAnalysis
      └── Prometheus: Error Rate, Lag 확인

Consumer Pods (Deployment via Rollout)
  ├── REST API: /lifecycle/start, /lifecycle/stop, /lifecycle/status
  └── 기본 시작 상태: STOPPED
```

**Approach A와의 차이:**

| 항목 | Approach A (Rollouts Native) | Approach C (Webhook + API) |
|------|---------------------------|--------------------------|
| Webhook 구현 | 전용 Go 서비스 (bg-webhook-job) | curl 기반 K8s Job 또는 간단 셸 스크립트 |
| Pod 발견 | Go 코드 내 K8s API 호출 | kubectl + jq 파이프라인 또는 Service DNS |
| 오케스트레이션 | 구조화된 Go 로직 (재시도, 타임아웃) | 셸 스크립트 수준 (순차 curl) |
| 이미지 의존 | 커스텀 Docker 이미지 | kubectl/curl 포함 이미지 (bitnami/kubectl 등) |
| 복잡도 | 중간 | **최소** |
| 견고성 | 높음 (구조화된 에러 처리) | 낮음 (스크립트 수준) |

**특징:**
- 가장 단순한 아키텍처
- 추가 컴포넌트 빌드 불필요 (기존 kubectl/curl 이미지 활용)
- 단일 실패 지점 (웹후크 Job 실패 시 안전망 없음)

---

## Webhook Job 구현 (Approach C 전용)

### curl 기반 AnalysisTemplate

```yaml
apiVersion: argoproj.io/v1alpha1
kind: AnalysisTemplate
metadata:
  name: consumer-switch-curl
  namespace: bg-test
spec:
  args:
  - name: active-selector
  - name: preview-selector
  - name: namespace
    value: "bg-test"
  metrics:
  - name: switch-consumers
    provider:
      job:
        spec:
          template:
            spec:
              serviceAccountName: switch-job-sa  # Pod 조회 권한
              containers:
              - name: switch
                image: bitnami/kubectl:1.23
                command: ["/bin/bash", "-c"]
                args:
                - |
                  set -e
                  NS="{{ args.namespace }}"

                  echo "=== Phase 1: Stop Active Consumers ==="
                  ACTIVE_PODS=$(kubectl get pods -n $NS \
                    -l "{{ args.active-selector }}" \
                    -o jsonpath='{.items[*].status.podIP}')
                  for IP in $ACTIVE_PODS; do
                    echo "Stopping consumer at $IP"
                    curl -sf -X POST "http://$IP:8080/lifecycle/stop" || true
                  done

                  echo "=== Phase 2: Wait for group leave ==="
                  sleep 5

                  echo "=== Phase 3: Start Preview Consumers ==="
                  PREVIEW_PODS=$(kubectl get pods -n $NS \
                    -l "{{ args.preview-selector }}" \
                    -o jsonpath='{.items[*].status.podIP}')
                  for IP in $PREVIEW_PODS; do
                    echo "Starting consumer at $IP"
                    curl -sf -X POST "http://$IP:8080/lifecycle/start" || exit 1
                  done

                  echo "=== Phase 4: Verify all started ==="
                  sleep 3
                  for IP in $PREVIEW_PODS; do
                    STATUS=$(curl -sf "http://$IP:8080/lifecycle/status" | grep -o '"state":[0-9]' | grep -o '[0-9]')
                    if [ "$STATUS" != "0" ]; then
                      echo "ERROR: Pod $IP not ACTIVE (state=$STATUS)"
                      exit 1
                    fi
                  done
                  echo "=== Switch complete ==="
              restartPolicy: Never
          backoffLimit: 1
```

### RBAC (Pod 조회 권한)

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: switch-job-sa
  namespace: bg-test
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: pod-reader
  namespace: bg-test
rules:
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: switch-job-pod-reader
  namespace: bg-test
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: pod-reader
subjects:
- kind: ServiceAccount
  name: switch-job-sa
  namespace: bg-test
```

---

## C-1: 단일 그룹 (pause/resume) 테스트

### 전환 시퀀스 상세

```
[T0] kubectl argo rollouts set image consumer consumer=bg-test-consumer:v2
  │
  ├── Argo: Preview RS 생성 → Green Pods STOPPED
  │
[T1] prePromotionAnalysis 시작
  │   ├── [Job] kubectl로 Active Pod IP 조회
  │   ├── [Job] curl → Active Pods /lifecycle/stop
  │   ├── [Job] sleep 5 (그룹 탈퇴 대기)
  │   ├── [Job] kubectl로 Preview Pod IP 조회
  │   ├── [Job] curl → Preview Pods /lifecycle/start
  │   └── [Job] curl → Preview Pods /lifecycle/status 확인
  │
  │   └── [Prometheus] Consumer Lag < 100 확인
  │
[T2] promote → Blue RS scale down
  │
[T3] postPromotionAnalysis → Lag 안정, Error Rate 정상
  │
[T4] 전환 완료
```

### 시나리오별 테스트

#### S1: 정상 Blue→Green 전환

| 단계 | 행위 | 검증 |
|------|------|------|
| 준비 | Blue ACTIVE, Producer TPS 100 | |
| 전환 | `kubectl argo rollouts set image consumer consumer=bg-test-consumer:v2` | |
| 자동 실행 | prePromotion curl Job 실행 | Job 로그: stop/start 순서 확인 |
| 검증 | Green ACTIVE, Lag 수렴 | 전환 시간 측정 |
| 프로모션 | `kubectl argo rollouts promote consumer` | |
| 최종 검증 | Validator 시퀀스 검증 | 유실 0건, 중복 측정 |

#### S2: 즉시 롤백

Approach A의 S2와 동일 구조.

#### S3: Consumer Lag 발생 중 전환

Approach A의 S3과 동일 구조.

#### S4: Pod 장애 중 전환

| 단계 | 행위 | 검증 |
|------|------|------|
| 준비 | prePromotion curl Job 실행 중 | |
| 장애 주입 | Green Pod 1개 강제 종료 | |
| 검증 | curl → Green Pod 연결 실패 → Job exit 1 | AnalysisRun Failure |
| 자동 롤백 | Argo 자동 롤백 → Blue 유지 | |
| **핵심 관찰** | **안전망 없음** — Job 실패 시 바로 롤백 (Approach B와 대조적) | |

#### S5: AnalysisRun 실패 → 자동 롤백

Approach A의 S5와 동일 구조.

---

## C-2: 개별 그룹 (scale 0/N) 테스트

### 구성

- Rollout: 2개 (`consumer-blue`, `consumer-green`)
- group.id: `bg-test-group-blue`, `bg-test-group-green`
- 전환: curl 스크립트 기반 offset 동기화 + scale up/down

### Webhook Job (개별 그룹용)

```yaml
# AnalysisTemplate 내 Job
args:
- |
  set -e
  NS="bg-test"
  BROKER="my-cluster-kafka-bootstrap.kafka:9092"

  echo "=== Phase 1: Offset sync ==="
  kafka-consumer-groups.sh --bootstrap-server $BROKER \
    --group bg-test-group-green --topic bg-test-topic \
    --reset-offsets --to-current --execute

  echo "=== Phase 2: Scale up Green ==="
  kubectl scale rollout consumer-green -n $NS --replicas=3

  echo "=== Phase 3: Wait for Green ready ==="
  kubectl rollout status rollout/consumer-green -n $NS --timeout=60s

  echo "=== Phase 4: Verify Green consuming ==="
  sleep 10
  # Prometheus query or curl check

  echo "=== Phase 5: Scale down Blue ==="
  kubectl scale rollout consumer-blue -n $NS --replicas=0

  echo "=== Switch complete ==="
```

### S1~S5: Approach A의 A-2와 동일 시나리오 구조

curl 스크립트 기반으로 동일 검증 수행.

---

## Approach A vs C 핵심 비교 포인트 (이 Task에서 측정)

| 비교 항목 | 측정 방법 |
|----------|----------|
| 전환 시간 차이 | T0→T4 비교 |
| Job 실행 시간 | Webhook Job 소요 시간 비교 |
| 안정성 (재시도 동작) | Pod 장애 시 동작 차이 |
| 구현 복잡도 | 코드/매니페스트 라인 수 |
| 디버깅 용이성 | Job 로그 가독성 비교 |

---

## 완료 조건

- [ ] curl 기반 AnalysisTemplate 작성 (단일 그룹 + 개별 그룹)
- [ ] RBAC 매니페스트 작성 (Pod 조회 권한)
- [ ] C-1 (단일 그룹): S1~S5 전체 5개 시나리오 실행 완료
- [ ] C-2 (개별 그룹): S1~S5 전체 5개 시나리오 실행 완료
- [ ] Approach A와의 비교 데이터 수집
- [ ] 각 시나리오별 측정 데이터 수집
