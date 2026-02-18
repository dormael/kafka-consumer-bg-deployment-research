# Task 09: Switch Sidecar 구현 (Go)

**Phase:** 2 - 애플리케이션 구현
**의존성:** task08 (Consumer App의 `/lifecycle` 엔드포인트)
**예상 소요:** 2시간 ~ 3시간
**튜토리얼:** `tutorial/09-switch-sidecar.md`

---

## 목표

Consumer Pod의 Sidecar 컨테이너로 동작하며, ConfigMap/CRD 변경을 Watch하여 Consumer App의 `/lifecycle` 엔드포인트에 HTTP POST를 전송하는 Go 애플리케이션을 구현한다.

---

## 프로젝트 구조

```
apps/switch-sidecar/
├── main.go
├── pkg/
│   ├── watcher/
│   │   └── configmap_watcher.go    # ConfigMap Watch 로직
│   ├── lifecycle/
│   │   └── client.go               # Consumer lifecycle HTTP 클라이언트
│   └── config/
│       └── config.go               # 설정 로딩 (환경변수)
├── Dockerfile
├── go.mod
├── go.sum
└── k8s/
    └── rbac.yaml                   # ConfigMap 읽기 권한
```

---

## Subtask 09-01: 환경변수 기반 설정

```go
type Config struct {
    ConsumerLifecycleURL string // e.g., "http://localhost:8080/lifecycle"
    ConfigMapName        string // e.g., "kafka-consumer-active-version"
    ConfigMapNamespace   string // e.g., "kafka"
    MyColor              string // "blue" or "green"
    PodName              string
    PollIntervalMs       int    // Watch 실패 시 fallback polling 간격
}
```

## Subtask 09-02: ConfigMap Watcher

```go
func (w *ConfigMapWatcher) Watch(ctx context.Context) error {
    // 1. K8s client-go Informer로 ConfigMap Watch
    // 2. "active" 필드 변경 감지
    // 3. 변경 시:
    //    - active == myColor → Consumer에 POST /lifecycle/resume
    //    - active != myColor → Consumer에 POST /lifecycle/pause
}
```

**핵심 동작 흐름:**

```
ConfigMap 변경 이벤트 수신
  ├── active == myColor (나의 색상이 활성)
  │   └── Consumer /lifecycle/resume 호출
  │       └── 상태 확인: GET /lifecycle/status → ACTIVE 확인
  │
  └── active != myColor (나의 색상이 비활성)
      └── Consumer /lifecycle/pause 호출
          └── 상태 확인: GET /lifecycle/status → PAUSED 확인
```

## Subtask 09-03: Lifecycle HTTP Client

```go
type LifecycleClient struct {
    baseURL    string
    httpClient *http.Client
}

func (c *LifecycleClient) Pause() error {
    resp, err := c.httpClient.Post(c.baseURL+"/pause", "application/json", nil)
    // 응답 처리, 에러 핸들링, 로깅
}

func (c *LifecycleClient) Resume() error {
    resp, err := c.httpClient.Post(c.baseURL+"/resume", "application/json", nil)
    // 응답 처리, 에러 핸들링, 로깅
}

func (c *LifecycleClient) Status() (string, error) {
    resp, err := c.httpClient.Get(c.baseURL + "/status")
    // 상태 파싱, 반환
}
```

## Subtask 09-04: 재시도 및 에러 처리

```go
// Pause/Resume 요청 실패 시 재시도 로직
func (w *ConfigMapWatcher) retryLifecycleAction(action func() error, maxRetries int) error {
    for i := 0; i < maxRetries; i++ {
        err := action()
        if err == nil {
            return nil
        }
        log.Printf("Retry %d/%d: %v", i+1, maxRetries, err)
        time.Sleep(time.Duration(math.Pow(2, float64(i))) * time.Second)
    }
    return fmt.Errorf("failed after %d retries", maxRetries)
}
```

## Subtask 09-05: 로깅

모든 주요 이벤트를 구조화된 JSON 로그로 출력한다 (Loki 수집 용이):

```json
{
  "level": "info",
  "msg": "ConfigMap changed",
  "active": "green",
  "myColor": "blue",
  "action": "pause",
  "pod": "consumer-blue-0",
  "timestamp": "2026-02-18T10:30:00Z"
}
```

## Subtask 09-06: Dockerfile

```dockerfile
FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o switch-sidecar .

FROM alpine:3.19
COPY --from=builder /app/switch-sidecar /usr/local/bin/
ENTRYPOINT ["switch-sidecar"]
```

## Subtask 09-07: RBAC

```yaml
# Sidecar가 ConfigMap을 읽을 수 있도록 RBAC 설정
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: configmap-reader
  namespace: kafka
rules:
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["get", "watch", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: sidecar-configmap-reader
  namespace: kafka
subjects:
  - kind: ServiceAccount
    name: consumer-sa
    namespace: kafka
roleRef:
  kind: Role
  name: configmap-reader
  apiGroup: rbac.authorization.k8s.io
```

---

## 완료 기준

- [ ] Go 빌드 성공
- [ ] ConfigMap 변경 시 Consumer `/lifecycle/pause` 또는 `/lifecycle/resume` 호출 확인
- [ ] Consumer 상태 변경 후 `/lifecycle/status`로 상태 확인
- [ ] 재시도 로직 동작 확인 (Consumer 일시 불가 시)
- [ ] 구조화된 JSON 로그 출력
- [ ] Docker 이미지 크기 < 20MB
- [ ] RBAC으로 ConfigMap 읽기 권한 정상 동작
