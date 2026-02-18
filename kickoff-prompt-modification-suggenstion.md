# Kickoff Prompt 수정 및 추가 제안 사항

`kafka-consumer-bluegreen-design.md`의 검증을 위한 `kickoff-prompt.md`에 아래 내용을 보완하면 더욱 정밀하고 신뢰도 높은 테스트가 가능할 것으로 판단됩니다.

## 1. 전략별 테스트 대상(SUT) 명확화 (전략 E 관련)
*   **현황:** "Java Spring 애플리케이션으로 구현한다"고 되어 있으나, 전략 E(Kafka Connect)는 프레임워크 자체 기능을 검증하는 것임.
*   **제안:** 전략 E 검증을 위해 커스텀 Java 앱 대신, **Strimzi `KafkaConnector` CRD와 표준 Sink Connector(예: FileStreamSink 또는 Echo Sink)**를 사용하여 REST API 및 CRD 제어 방식을 검증하는 태스크를 명시함.

## 2. 메시지 정합성(유실/중복) 검증 자동화 도구
*   **현황:** "시퀀스 번호를 기록한다"는 수동 확인 위주의 계획.
*   **제안:** 대량의 메시지 처리를 고려하여, 테스트 완료 후 Producer의 발행 로그와 Consumer의 수신 로그(Loki/DB)를 비교해 **유실/중복 시퀀스를 자동으로 산출하는 'Validator 스크립트'** 작성을 계획에 추가함.

## 3. Static Membership 동작 정밀 검증
*   **현황:** 전략 C의 핵심인 Static Membership에 대한 언급이 부족함.
*   **제안:** 테스트 시나리오에 **"Static Membership 동작 검증"**을 명시. Pod 재시작 시 Rebalance 발생 여부를 리스너 로그(JoinGroup/SyncGroup)를 통해 확인하는 절차를 포함함.

## 4. KEDA 오토스케일 정합성 시나리오 추가
*   **현황:** KEDA는 선택 사항으로 되어 있음.
*   **제안:** **"PAUSED 상태에서의 스케일 업 정합성 확인"** 시나리오 추가. 새로 투입되는 Pod가 초기 상태를 PAUSED로 올바르게 유지하여 'Dual Active'를 방지하는지 검증함.

## 5. Switch Controller의 원자적 전환(Lease) 구현
*   **현황:** "양쪽 동시 Active 방지"라는 목표만 제시됨.
*   **제안:** Controller 구현 시 **"K8s Lease API를 사용한 상호 배제(Mutual Exclusion) 로직"**을 서브태스크로 구체화하여, 물리적으로 두 색상이 동시에 활성화될 수 없음을 보장함.

## 6. 환경 정리(Cleanup) 태스크 추가
*   **제안:** 테스트에 사용된 대규모 리소스(Kafka, Prometheus, Strimzi 등)를 완전히 제거하는 **`cleanup` 스크립트**와 가이드를 추가하여 클러스터 자원 관리를 용이하게 함.
