# Task 10: Switch Controller 구현 (Go)

**Phase:** 2 - 애플리케이션 구현
**의존성:** task08 (Consumer App), task09 (Switch Sidecar)
**예상 소요:** 3시간 ~ 5시간
**튜토리얼:** `tutorial/10-switch-controller.md`

---

## 목표

"Pause First, Resume Second" 원칙에 따라 Blue/Green 전환을 오케스트레이션하는 Switch Controller를 Go로 구현한다. K8s Lease 기반 양쪽 동시 Active 방지를 포함한다.

---

## 프로젝트 구조

```
apps/switch-controller/
├── main.go
├── pkg/
│   ├── controller/
│   │   └── switch_controller.go    # 전환 오케스트레이션 핵심 로직
│   ├── lifecycle/
│   │   └── client.go               # Consumer lifecycle HTTP 클라이언트 (sidecar와 공유)
│   ├── lease/
│   │   └── lease_manager.go        # K8s Lease 기반 분산 잠금
│   ├── offset/
│   │   └── sync.go                 # Offset 동기화 (전략 B용)
│   ├── configmap/
│   │   └── manager.go              # ConfigMap 업데이트
│   └── metrics/
│       └── metrics.go              # 전환 소요 시간 등 지표
├── Dockerfile
├── go.mod
├── go.sum
└── k8s/
    ├── deployment.yaml
    ├── rbac.yaml
    └── lease.yaml
```

---

## Subtask 10-01: 전환 오케스트레이션 (핵심)

### Switch Sequence (전략 C)

```go
func (sc *SwitchController) ExecuteSwitch(from, to string) error {
    startTime := time.Now()

    // Step 0: Pre-check
    if err := sc.preCheck(from, to); err != nil {
        return fmt.Errorf("pre-check failed: %w", err)
    }

    // Step 1: Lease 획득 (양쪽 동시 Active 방지)
    if err := sc.leaseManager.Acquire(to); err != nil {
        return fmt.Errorf("lease acquisition failed: %w", err)
    }
    defer sc.leaseManager.Release()

    // Step 2: "from" Consumer PAUSE
    if err := sc.pauseConsumers(from); err != nil {
        return fmt.Errorf("pause %s failed: %w", from, err)
    }

    // Step 3: "from" PAUSED 상태 확인 (polling)
    if err := sc.waitForState(from, "PAUSED", 30*time.Second); err != nil {
        // 롤백: resume "from"
        sc.resumeConsumers(from)
        return fmt.Errorf("wait for %s PAUSED failed: %w", from, err)
    }

    // Step 4: ConfigMap 업데이트 (active: to)
    if err := sc.configMapManager.SetActive(to); err != nil {
        sc.resumeConsumers(from)
        return fmt.Errorf("configmap update failed: %w", err)
    }

    // Step 5: "to" Consumer RESUME
    if err := sc.resumeConsumers(to); err != nil {
        // 롤백: ConfigMap 복구 + resume "from"
        sc.configMapManager.SetActive(from)
        sc.resumeConsumers(from)
        return fmt.Errorf("resume %s failed: %w", to, err)
    }

    // Step 6: "to" ACTIVE 상태 확인
    if err := sc.waitForState(to, "ACTIVE", 30*time.Second); err != nil {
        sc.autoRollback(from, to)
        return fmt.Errorf("wait for %s ACTIVE failed: %w", to, err)
    }

    duration := time.Since(startTime)
    sc.metrics.RecordSwitchDuration(duration)
    log.Printf("Switch %s -> %s completed in %v", from, to, duration)
    return nil
}
```

### Rollback Sequence

```go
func (sc *SwitchController) ExecuteRollback() error {
    // 동일 메커니즘, 방향만 반대
    current := sc.configMapManager.GetActive()
    previous := sc.configMapManager.GetPrevious()
    return sc.ExecuteSwitch(current, previous)
}
```

## Subtask 10-02: K8s Lease 기반 분산 잠금

```go
type LeaseManager struct {
    client    kubernetes.Interface
    namespace string
    leaseName string
}

func (lm *LeaseManager) Acquire(holder string) error {
    // 1. Lease 객체 조회 또는 생성
    // 2. holderIdentity 설정
    // 3. leaseDurationSeconds 설정 (30초)
    // 4. acquireTime, renewTime 업데이트
    // 5. 이미 다른 holder가 보유 중이면 에러 반환
}

func (lm *LeaseManager) Release() error {
    // holderIdentity를 빈 문자열로 설정
}

func (lm *LeaseManager) IsHeldBy(holder string) bool {
    // 현재 Lease holder 확인
}
```

```yaml
# k8s/lease.yaml
apiVersion: coordination.k8s.io/v1
kind: Lease
metadata:
  name: bg-consumer-active-lease
  namespace: kafka
spec:
  holderIdentity: "blue"
  leaseDurationSeconds: 30
```

## Subtask 10-03: Offset 동기화 (전략 B용)

```go
type OffsetSyncer struct {
    bootstrapServers string
    adminClient      *kafka.AdminClient
}

func (os *OffsetSyncer) SyncOffsets(fromGroup, toGroup, topic string) error {
    // 1. fromGroup의 현재 offset 조회
    // 2. toGroup의 offset을 fromGroup 값으로 재설정
    // kafka-consumer-groups.sh --reset-offsets 와 동일한 동작
}
```

## Subtask 10-04: Pre-Check 로직

```go
func (sc *SwitchController) preCheck(from, to string) error {
    // 1. "from" Consumer들이 모두 ACTIVE 상태인지 확인
    // 2. "to" Consumer Pod들이 모두 Ready인지 확인
    // 3. "from" Consumer Lag 확인 (< threshold)
    // 4. Kafka Broker 상태 확인
}
```

## Subtask 10-05: 지표 노출

```go
type Metrics struct {
    switchDuration    prometheus.Histogram
    switchTotal       prometheus.Counter
    switchFailures    prometheus.Counter
    rollbackTotal     prometheus.Counter
    dualActiveEvents  prometheus.Counter  // 양쪽 동시 Active 감지 횟수
}

// 지표명:
// - bg_switch_duration_seconds: 전환 소요 시간
// - bg_switch_total: 전환 시도 횟수
// - bg_switch_failures_total: 전환 실패 횟수
// - bg_rollback_total: 롤백 시도 횟수
// - bg_dual_active_events_total: 양쪽 동시 Active 감지 횟수
```

## Subtask 10-06: REST API (수동 전환 트리거)

```go
// POST /switch         - Blue→Green 또는 Green→Blue 전환 실행
// POST /rollback       - 이전 상태로 롤백
// GET  /status         - 현재 전환 상태 조회
// GET  /health         - 컨트롤러 헬스체크
```

## Subtask 10-07: K8s 매니페스트

```yaml
# k8s/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: bg-switch-controller
  namespace: kafka
spec:
  replicas: 1   # 단일 인스턴스 (Lease로 보호)
  selector:
    matchLabels:
      app: bg-switch-controller
  template:
    spec:
      serviceAccountName: switch-controller-sa
      containers:
        - name: controller
          image: bg-switch-controller:latest
          ports:
            - containerPort: 8090
          env:
            - name: BLUE_SERVICE_URL
              value: "http://consumer-blue.kafka:8080"
            - name: GREEN_SERVICE_URL
              value: "http://consumer-green.kafka:8080"
            - name: CONFIGMAP_NAME
              value: "kafka-consumer-active-version"
            - name: NAMESPACE
              value: "kafka"
            - name: LEASE_NAME
              value: "bg-consumer-active-lease"
```

---

## 완료 기준

- [ ] `POST /switch`로 Blue→Green 전환 성공
- [ ] "Pause First, Resume Second" 순서 보장 확인
- [ ] K8s Lease 획득/해제 동작 확인
- [ ] 전환 중 한쪽 Consumer가 응답하지 않을 때 자동 롤백 동작
- [ ] 전환 소요 시간 지표(`bg_switch_duration_seconds`) 수집 확인
- [ ] Offset 동기화 (전략 B용) 동작 확인
- [ ] 양쪽 동시 Active 감지 및 방지 동작
