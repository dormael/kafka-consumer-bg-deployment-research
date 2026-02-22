
# 쿠버네티스 사이드카 동적 설정 갱신 패턴 가이드

이 문서는 ConfigMap 변경 감지부터 수천 개 규모의 Pod을 위한 대규모 아키텍처(xDS, Aggregator)까지의 설계 패턴을 정리합니다.

## 1. 기본 개념: Sidecar 기반 동적 갱신

쿠버네티스에서 ConfigMap을 볼륨으로 마운트할 때, 애플리케이션의 재시작 없이 변경 사항을 반영하는 가장 일반적인 패턴입니다.

### 동작 원리

1. **공유 볼륨:** Main과 Sidecar가 동일한 볼륨(ConfigMap 또는 EmptyDir)을 공유합니다.
2. **변경 감지:** Sidecar가 파일 변경을 감지(inotify 등)합니다.
3. **신호 전달:** Sidecar가 Main 컨테이너에게 HTTP 요청(Webhook)이나 OS 시그널(SIGHUP)을 보내 설정을 다시 읽게 합니다.

---

## 2. 주요 구현 패턴 및 프레임워크 사례

### A. HTTP Webhook 패턴 (Prometheus 사례)

* **방식:** Sidecar가 변경 감지 후 `POST /-/reload` 호출.
* **특징:** 가장 단순하고 직관적이나 Kubelet의 마운트 지연(최대 1분)이 발생함.

### B. API Watch 패턴 (Spring Cloud Kubernetes 사례)

* **방식:** Sidecar 없이 앱이 직접 K8s API Server를 Watch.
* **특징:** 마운트 지연 없이 즉시 반영되나, 앱에 K8s 의존 라이브러리가 포함됨.

### C. Rolling Update 강제 패턴 (Reloader 도구)

* **방식:** 컨트롤러가 ConfigMap 변경 시 Deployment의 Annotation을 수정하여 Pod을 순차 재시작.
* **특징:** 앱 수정이 전혀 필요 없으나, 재시작 비용이 발생함.

---

## 3. 고도화 패턴: Envoy/Istio의 xDS

대규모 서비스 메시 환경에서 사용하는 **gRPC 기반 스트리밍** 방식입니다.

* **xDS API:** LDS(리스너), RDS(루트), CDS(클러스터), EDS(엔드포인트) 등 설정을 세분화하여 관리.
* **동작:** Istiod(Control Plane)가 API Server를 Watch하고, 변경된 설정만 Envoy(Sidecar)에게 gRPC로 푸시.
* **장점:** 마운트 지연 없음, Hot Reload 지원, 수천 개 Pod의 원자적 설정 갱신.

---

## 4. 대규모 클러스터(수천 개 Pod)를 위한 설계 전략

수천 개의 사이드카가 직접 Kube API Server를 Watch하면 서버가 마비될 수 있습니다. 이를 해결하기 위한 **Aggregator(집계자)** 패턴이 필수적입니다.

### 대표적인 해결책

1. **Aggregator (Central Controller):** - 중앙 컨트롤러만 API Server를 보고, 사이드카는 컨트롤러와 gRPC로 통신. (Istio 방식)
2. **Node-Local Agent (DaemonSet):** - 노드당 1개의 에이전트만 API 서버와 통신하고, 해당 노드의 Pod들은 에이전트로부터 정보를 전달받음.
3. **External Pub/Sub:** - Redis, NATS 등 외부 메시지 브로커를 통해 설정을 전파하여 K8s API 부하를 완전히 격리.

### 업계 실제 사례

| 사례 | 기술 명칭 | 주요 목적 |
| --- | --- | --- |
| **Netflix** | Metacat | 대규모 메타데이터 전역 동기화 및 DB 보호 |
| **Uber** | Flip | 수천 개 서비스의 기능 플래그(Feature Flag) 실시간 전파 |
| **Yelp** | Paasta Aggregator | 사이드카 메모리 점유율 최적화 (필요한 정보만 필터링) |
| **OPA** | Bundle Service | 수만 개의 보안 정책 사이드카에 정책 파일 배포 |

---

## 5. 설계 시 주의사항

* **Security:** 사이드카에 `pods/exec` 권한을 주는 것은 보안상 매우 위험하므로 `shareProcessNamespace`를 통한 시그널 전달을 권장합니다.
* **Thundering Herd:** 수천 개의 Pod이 동시에 설정을 갱신하면 순간적으로 CPU/네트워크 부하가 치솟습니다. 전파 시 **Jitter(무작위 지연)**를 추가하는 것이 좋습니다.
* **SubPath 주의:** ConfigMap 마운트 시 `subPath`를 사용하면 파일이 자동 업데이트되지 않으므로 디렉토리 전체 마운트를 권장합니다.

---

**Tip:** 파일로 저장하려면 위 내용을 복사하여 `kubernetes-sidecar-patterns.md`라는 이름으로 저장하시면 됩니다. 추가로 특정 방식의 상세 구현 코드(Go, Java 등)가 필요하시면 말씀해 주세요!
