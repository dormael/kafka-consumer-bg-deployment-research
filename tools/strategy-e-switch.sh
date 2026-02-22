#!/usr/bin/env bash
# =============================================================================
# 전략 E 전환/롤백 헬퍼 스크립트
# 사용법: source tools/strategy-e-switch.sh
#
# 주요 함수:
#   check_e_status       — Connector/Worker 상태 조회
#   check_e_offsets      — Blue/Green Connect 그룹 offset 조회
#   switch_to_green_crd  — Blue→Green 전환 (CRD 방식)
#   switch_to_blue_crd   — Green→Blue 롤백 (CRD 방식)
#   switch_to_green_rest — Blue→Green 전환 (REST API 방식, 비교용)
#   switch_to_blue_rest  — Green→Blue 롤백 (REST API 방식, 비교용)
#   test_config_persist  — config topic 영속성 테스트
#   test_crd_rest_conflict — CRD vs REST API 충돌 테스트
# =============================================================================

# --- 상수 ---
KAFKA_NS="kafka"
KAFKA_POD="kafka-cluster-dual-role-0"
KAFKA_BOOTSTRAP="localhost:9092"
TOPIC="bg-test-topic"

BLUE_CONNECTOR="bg-sink-blue"
GREEN_CONNECTOR="bg-sink-green"
BLUE_CONNECT="connect-blue"
GREEN_CONNECT="connect-green"
BLUE_CONNECT_API="connect-blue-connect-api:8083"
GREEN_CONNECT_API="connect-green-connect-api:8083"

# Strimzi reconcile 타임아웃 (초)
RECONCILE_TIMEOUT=120

# --- Connector/Worker 상태 조회 ---

check_e_status() {
  echo "=== KafkaConnect Clusters ==="
  kubectl get kafkaconnect -n "$KAFKA_NS" \
    -o custom-columns='NAME:.metadata.name,READY:.status.conditions[?(@.type=="Ready")].status,REPLICAS:.spec.replicas' \
    2>/dev/null

  echo ""
  echo "=== KafkaConnectors ==="
  kubectl get kafkaconnector -n "$KAFKA_NS" \
    -o custom-columns='NAME:.metadata.name,CLUSTER:.metadata.labels.strimzi\.io/cluster,SPEC_STATE:.spec.state,ACTUAL_STATE:.status.connectorStatus.connector.state,TASKS:.spec.tasksMax' \
    2>/dev/null

  echo ""
  echo "=== Connect Worker Pods ==="
  kubectl get pods -n "$KAFKA_NS" -l 'strimzi.io/kind=KafkaConnect' \
    -o custom-columns='NAME:.metadata.name,STATUS:.status.phase,READY:.status.conditions[?(@.type=="Ready")].status' \
    2>/dev/null

  echo ""
  echo "=== REST API 상태 (Blue) ==="
  kubectl exec -n "$KAFKA_NS" "$KAFKA_POD" -- \
    curl -sf "http://${BLUE_CONNECT_API}/connectors/${BLUE_CONNECTOR}/status" 2>/dev/null \
    | python3 -m json.tool 2>/dev/null || echo "(접근 불가 또는 Connector 미등록)"

  echo ""
  echo "=== REST API 상태 (Green) ==="
  kubectl exec -n "$KAFKA_NS" "$KAFKA_POD" -- \
    curl -sf "http://${GREEN_CONNECT_API}/connectors/${GREEN_CONNECTOR}/status" 2>/dev/null \
    | python3 -m json.tool 2>/dev/null || echo "(접근 불가 또는 Connector 미등록)"
}

# --- Connect Group Offset 조회 ---

check_e_offsets() {
  echo "=== Blue Connect Group (connect-cluster-blue) ==="
  kubectl exec -n "$KAFKA_NS" "$KAFKA_POD" -- \
    bin/kafka-consumer-groups.sh --bootstrap-server "$KAFKA_BOOTSTRAP" \
      --group "connect-cluster-blue" --describe 2>/dev/null

  echo ""
  echo "=== Green Connect Group (connect-cluster-green) ==="
  kubectl exec -n "$KAFKA_NS" "$KAFKA_POD" -- \
    bin/kafka-consumer-groups.sh --bootstrap-server "$KAFKA_BOOTSTRAP" \
      --group "connect-cluster-green" --describe 2>/dev/null
}

# --- CRD 상태 대기 헬퍼 ---
# 사용: _wait_connector_state <connector-name> <desired-state> [timeout]

_wait_connector_state() {
  local connector=$1
  local desired=$2
  local timeout=${3:-$RECONCILE_TIMEOUT}
  local elapsed=0

  echo "  $connector → $desired 대기 (최대 ${timeout}초)..."
  while [ $elapsed -lt "$timeout" ]; do
    local state
    state=$(kubectl get kafkaconnector "$connector" -n "$KAFKA_NS" \
      -o jsonpath='{.status.connectorStatus.connector.state}' 2>/dev/null)
    if [ "$state" = "$desired" ]; then
      echo "  $connector: $desired (${elapsed}초)"
      return 0
    fi
    sleep 2
    elapsed=$((elapsed + 2))
    # 10초마다 현재 상태 출력
    if [ $((elapsed % 10)) -eq 0 ]; then
      echo "  ... 현재 상태: ${state:-unknown} (${elapsed}초 경과)"
    fi
  done

  echo "  타임아웃! $connector 상태: $(kubectl get kafkaconnector "$connector" -n "$KAFKA_NS" \
    -o jsonpath='{.status.connectorStatus.connector.state}' 2>/dev/null)"
  return 1
}

# --- Blue→Green 전환 (CRD 방식) ---

switch_to_green_crd() {
  echo "========================================="
  echo "  전략 E: Blue → Green 전환 (CRD)"
  echo "========================================="

  local SWITCH_START
  SWITCH_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  local START_EPOCH
  START_EPOCH=$(date +%s)
  echo "[$(date -u +%T)] 전환 시작: $SWITCH_START"

  # Step 1: Blue Connector → STOPPED
  echo ""
  echo "[Step 1] Blue Connector → STOPPED..."
  if ! kubectl patch kafkaconnector "$BLUE_CONNECTOR" -n "$KAFKA_NS" \
    --type merge -p '{"spec":{"state":"stopped"}}'; then
    echo "FATAL: Blue Connector patch 실패. 전환을 중단합니다."
    return 1
  fi

  # Step 2: Blue STOPPED 확인
  echo ""
  echo "[Step 2] Blue STOPPED 대기..."
  if ! _wait_connector_state "$BLUE_CONNECTOR" "STOPPED"; then
    echo "경고: Blue STOPPED 타임아웃. 계속 진행합니다."
  fi

  # Step 3: Green Connector → RUNNING
  echo ""
  echo "[Step 3] Green Connector → RUNNING..."
  if ! kubectl patch kafkaconnector "$GREEN_CONNECTOR" -n "$KAFKA_NS" \
    --type merge -p '{"spec":{"state":"running"}}'; then
    echo "FATAL: Green Connector patch 실패. Blue를 복원합니다."
    kubectl patch kafkaconnector "$BLUE_CONNECTOR" -n "$KAFKA_NS" \
      --type merge -p '{"spec":{"state":"running"}}' 2>/dev/null
    return 1
  fi

  # Step 4: Green RUNNING 확인
  echo ""
  echo "[Step 4] Green RUNNING 대기..."
  if ! _wait_connector_state "$GREEN_CONNECTOR" "RUNNING"; then
    echo "경고: Green RUNNING 타임아웃."
  fi

  local END_EPOCH
  END_EPOCH=$(date +%s)
  local SWITCH_END
  SWITCH_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  local DURATION=$((END_EPOCH - START_EPOCH))

  echo ""
  echo "========================================="
  echo "  전환 완료! (CRD)"
  echo "  시작: $SWITCH_START"
  echo "  종료: $SWITCH_END"
  echo "  소요: ${DURATION}초"
  echo "========================================="

  export SWITCH_START SWITCH_END
}

# --- Green→Blue 롤백 (CRD 방식) ---

switch_to_blue_crd() {
  echo "========================================="
  echo "  전략 E: Green → Blue 롤백 (CRD)"
  echo "========================================="

  local ROLLBACK_START
  ROLLBACK_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  local START_EPOCH
  START_EPOCH=$(date +%s)
  echo "[$(date -u +%T)] 롤백 시작: $ROLLBACK_START"

  # Step 1: Green Connector → STOPPED
  echo ""
  echo "[Step 1] Green Connector → STOPPED..."
  if ! kubectl patch kafkaconnector "$GREEN_CONNECTOR" -n "$KAFKA_NS" \
    --type merge -p '{"spec":{"state":"stopped"}}'; then
    echo "FATAL: Green Connector patch 실패. 롤백을 중단합니다."
    return 1
  fi

  # Step 2: Green STOPPED 확인
  echo ""
  echo "[Step 2] Green STOPPED 대기..."
  if ! _wait_connector_state "$GREEN_CONNECTOR" "STOPPED"; then
    echo "경고: Green STOPPED 타임아웃. 계속 진행합니다."
  fi

  # Step 3: Blue Connector → RUNNING
  echo ""
  echo "[Step 3] Blue Connector → RUNNING..."
  if ! kubectl patch kafkaconnector "$BLUE_CONNECTOR" -n "$KAFKA_NS" \
    --type merge -p '{"spec":{"state":"running"}}'; then
    echo "FATAL: Blue Connector patch 실패. Green을 복원합니다."
    kubectl patch kafkaconnector "$GREEN_CONNECTOR" -n "$KAFKA_NS" \
      --type merge -p '{"spec":{"state":"running"}}' 2>/dev/null
    return 1
  fi

  # Step 4: Blue RUNNING 확인
  echo ""
  echo "[Step 4] Blue RUNNING 대기..."
  if ! _wait_connector_state "$BLUE_CONNECTOR" "RUNNING"; then
    echo "경고: Blue RUNNING 타임아웃."
  fi

  local END_EPOCH
  END_EPOCH=$(date +%s)
  local ROLLBACK_END
  ROLLBACK_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  local DURATION=$((END_EPOCH - START_EPOCH))

  echo ""
  echo "========================================="
  echo "  롤백 완료! (CRD)"
  echo "  시작: $ROLLBACK_START"
  echo "  종료: $ROLLBACK_END"
  echo "  소요: ${DURATION}초"
  echo "========================================="

  export ROLLBACK_START ROLLBACK_END
}

# --- Blue→Green 전환 (REST API 방식, 비교용) ---
# 주의: Strimzi 환경에서는 CRD reconcile에 의해 덮어써질 수 있음.
#       CRD의 spec.state를 먼저 변경하고 REST API 반영 시간만 측정하는 것이 정확.

switch_to_green_rest() {
  echo "========================================="
  echo "  전략 E: Blue → Green 전환 (REST API)"
  echo "========================================="
  echo "  주의: 이 방식은 CRD spec.state도 함께 변경합니다."
  echo ""

  local SWITCH_START
  SWITCH_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  local START_EPOCH
  START_EPOCH=$(date +%s)
  echo "[$(date -u +%T)] 전환 시작: $SWITCH_START"

  # Step 1: CRD spec.state 변경 (reconcile 충돌 방지)
  echo ""
  echo "[Step 1] CRD spec.state 업데이트..."
  kubectl patch kafkaconnector "$BLUE_CONNECTOR" -n "$KAFKA_NS" \
    --type merge -p '{"spec":{"state":"stopped"}}'
  kubectl patch kafkaconnector "$GREEN_CONNECTOR" -n "$KAFKA_NS" \
    --type merge -p '{"spec":{"state":"running"}}'

  # Step 2: REST API로 Blue stop
  echo ""
  echo "[Step 2] REST API: Blue stop..."
  kubectl exec -n "$KAFKA_NS" "$KAFKA_POD" -- \
    curl -sf -X PUT "http://${BLUE_CONNECT_API}/connectors/${BLUE_CONNECTOR}/stop"
  echo ""

  # Step 3: REST API로 Blue STOPPED 확인
  echo "[Step 3] Blue STOPPED 폴링..."
  local elapsed=0
  while [ $elapsed -lt 30 ]; do
    local state
    state=$(kubectl exec -n "$KAFKA_NS" "$KAFKA_POD" -- \
      curl -sf "http://${BLUE_CONNECT_API}/connectors/${BLUE_CONNECTOR}/status" 2>/dev/null \
      | python3 -c "import sys,json; print(json.load(sys.stdin)['connector']['state'])" 2>/dev/null)
    echo "  Blue: $state (${elapsed}초)"
    [ "$state" = "STOPPED" ] && break
    sleep 1
    elapsed=$((elapsed + 1))
  done

  # Step 4: REST API로 Green resume
  echo ""
  echo "[Step 4] REST API: Green resume..."
  kubectl exec -n "$KAFKA_NS" "$KAFKA_POD" -- \
    curl -sf -X PUT "http://${GREEN_CONNECT_API}/connectors/${GREEN_CONNECTOR}/resume"
  echo ""

  # Step 5: Green RUNNING 확인
  echo "[Step 5] Green RUNNING 폴링..."
  elapsed=0
  while [ $elapsed -lt 30 ]; do
    local state
    state=$(kubectl exec -n "$KAFKA_NS" "$KAFKA_POD" -- \
      curl -sf "http://${GREEN_CONNECT_API}/connectors/${GREEN_CONNECTOR}/status" 2>/dev/null \
      | python3 -c "import sys,json; print(json.load(sys.stdin)['connector']['state'])" 2>/dev/null)
    echo "  Green: $state (${elapsed}초)"
    [ "$state" = "RUNNING" ] && break
    sleep 1
    elapsed=$((elapsed + 1))
  done

  local END_EPOCH
  END_EPOCH=$(date +%s)
  local SWITCH_END
  SWITCH_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  local DURATION=$((END_EPOCH - START_EPOCH))

  echo ""
  echo "========================================="
  echo "  전환 완료! (REST API)"
  echo "  시작: $SWITCH_START"
  echo "  종료: $SWITCH_END"
  echo "  소요: ${DURATION}초"
  echo "========================================="

  export SWITCH_START SWITCH_END
}

# --- Green→Blue 롤백 (REST API 방식, 비교용) ---

switch_to_blue_rest() {
  echo "========================================="
  echo "  전략 E: Green → Blue 롤백 (REST API)"
  echo "========================================="
  echo "  주의: 이 방식은 CRD spec.state도 함께 변경합니다."
  echo ""

  local ROLLBACK_START
  ROLLBACK_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  local START_EPOCH
  START_EPOCH=$(date +%s)
  echo "[$(date -u +%T)] 롤백 시작: $ROLLBACK_START"

  # Step 1: CRD spec.state 변경
  echo ""
  echo "[Step 1] CRD spec.state 업데이트..."
  kubectl patch kafkaconnector "$GREEN_CONNECTOR" -n "$KAFKA_NS" \
    --type merge -p '{"spec":{"state":"stopped"}}'
  kubectl patch kafkaconnector "$BLUE_CONNECTOR" -n "$KAFKA_NS" \
    --type merge -p '{"spec":{"state":"running"}}'

  # Step 2: REST API로 Green stop
  echo ""
  echo "[Step 2] REST API: Green stop..."
  kubectl exec -n "$KAFKA_NS" "$KAFKA_POD" -- \
    curl -sf -X PUT "http://${GREEN_CONNECT_API}/connectors/${GREEN_CONNECTOR}/stop"
  echo ""

  # Step 3: Green STOPPED 확인
  echo "[Step 3] Green STOPPED 폴링..."
  local elapsed=0
  while [ $elapsed -lt 30 ]; do
    local state
    state=$(kubectl exec -n "$KAFKA_NS" "$KAFKA_POD" -- \
      curl -sf "http://${GREEN_CONNECT_API}/connectors/${GREEN_CONNECTOR}/status" 2>/dev/null \
      | python3 -c "import sys,json; print(json.load(sys.stdin)['connector']['state'])" 2>/dev/null)
    echo "  Green: $state (${elapsed}초)"
    [ "$state" = "STOPPED" ] && break
    sleep 1
    elapsed=$((elapsed + 1))
  done

  # Step 4: REST API로 Blue resume
  echo ""
  echo "[Step 4] REST API: Blue resume..."
  kubectl exec -n "$KAFKA_NS" "$KAFKA_POD" -- \
    curl -sf -X PUT "http://${BLUE_CONNECT_API}/connectors/${BLUE_CONNECTOR}/resume"
  echo ""

  # Step 5: Blue RUNNING 확인
  echo "[Step 5] Blue RUNNING 폴링..."
  elapsed=0
  while [ $elapsed -lt 30 ]; do
    local state
    state=$(kubectl exec -n "$KAFKA_NS" "$KAFKA_POD" -- \
      curl -sf "http://${BLUE_CONNECT_API}/connectors/${BLUE_CONNECTOR}/status" 2>/dev/null \
      | python3 -c "import sys,json; print(json.load(sys.stdin)['connector']['state'])" 2>/dev/null)
    echo "  Blue: $state (${elapsed}초)"
    [ "$state" = "RUNNING" ] && break
    sleep 1
    elapsed=$((elapsed + 1))
  done

  local END_EPOCH
  END_EPOCH=$(date +%s)
  local ROLLBACK_END
  ROLLBACK_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  local DURATION=$((END_EPOCH - START_EPOCH))

  echo ""
  echo "========================================="
  echo "  롤백 완료! (REST API)"
  echo "  시작: $ROLLBACK_START"
  echo "  종료: $ROLLBACK_END"
  echo "  소요: ${DURATION}초"
  echo "========================================="

  export ROLLBACK_START ROLLBACK_END
}

# --- config topic 영속성 테스트 ---

test_config_persist() {
  echo "========================================="
  echo "  config topic 영속성 테스트"
  echo "========================================="

  # Step 1: Blue Connector를 PAUSED로 변경
  echo ""
  echo "[Step 1] Blue → PAUSED..."
  kubectl patch kafkaconnector "$BLUE_CONNECTOR" -n "$KAFKA_NS" \
    --type merge -p '{"spec":{"state":"paused"}}'
  _wait_connector_state "$BLUE_CONNECTOR" "PAUSED" 60

  # Step 2: 현재 상태 기록
  echo ""
  echo "[Step 2] PAUSED 상태 확인..."
  local before_state
  before_state=$(kubectl get kafkaconnector "$BLUE_CONNECTOR" -n "$KAFKA_NS" \
    -o jsonpath='{.status.connectorStatus.connector.state}' 2>/dev/null)
  echo "  재시작 전: $before_state"

  # Step 3: Connect Worker Pod 강제 삭제
  echo ""
  echo "[Step 3] Blue Connect Worker Pod 강제 삭제..."
  kubectl delete pod -n "$KAFKA_NS" -l "strimzi.io/name=${BLUE_CONNECT}-connect" --force 2>/dev/null
  echo "  Pod 삭제 완료. 복구 대기 중..."

  # Step 4: Worker 복귀 대기
  echo ""
  echo "[Step 4] Worker 복귀 대기..."
  sleep 5
  kubectl wait --for=condition=Ready pod -n "$KAFKA_NS" \
    -l "strimzi.io/name=${BLUE_CONNECT}-connect" --timeout=120s 2>/dev/null

  # Strimzi가 Connector 상태를 reconcile할 시간 추가 대기
  echo "  Strimzi reconcile 대기 (30초)..."
  sleep 30

  # Step 5: 재시작 후 상태 확인
  echo ""
  echo "[Step 5] 재시작 후 상태 확인..."
  local after_state
  after_state=$(kubectl get kafkaconnector "$BLUE_CONNECTOR" -n "$KAFKA_NS" \
    -o jsonpath='{.status.connectorStatus.connector.state}' 2>/dev/null)
  echo "  재시작 후: $after_state"

  echo ""
  echo "========================================="
  if [ "$after_state" = "PAUSED" ]; then
    echo "  결과: PASS"
    echo "  config topic에서 PAUSED 상태가 정상 복원됨"
  else
    echo "  결과: FAIL"
    echo "  기대: PAUSED, 실제: $after_state"
  fi
  echo "========================================="

  # Blue를 RUNNING으로 복원
  echo ""
  echo "[정리] Blue → RUNNING 복원..."
  kubectl patch kafkaconnector "$BLUE_CONNECTOR" -n "$KAFKA_NS" \
    --type merge -p '{"spec":{"state":"running"}}'
  _wait_connector_state "$BLUE_CONNECTOR" "RUNNING" 60
}

# --- CRD vs REST API 충돌 테스트 ---

test_crd_rest_conflict() {
  echo "========================================="
  echo "  CRD vs REST API 충돌 테스트"
  echo "========================================="

  # Step 1: CRD에 running 확인
  echo ""
  echo "[Step 1] Blue CRD spec.state 확인..."
  local crd_state
  crd_state=$(kubectl get kafkaconnector "$BLUE_CONNECTOR" -n "$KAFKA_NS" \
    -o jsonpath='{.spec.state}' 2>/dev/null)
  echo "  CRD spec.state: $crd_state"

  if [ "$crd_state" != "running" ]; then
    echo "  Blue가 running이 아닙니다. running으로 변경 후 대기..."
    kubectl patch kafkaconnector "$BLUE_CONNECTOR" -n "$KAFKA_NS" \
      --type merge -p '{"spec":{"state":"running"}}'
    _wait_connector_state "$BLUE_CONNECTOR" "RUNNING" 60
  fi

  # Step 2: REST API로 직접 pause
  echo ""
  echo "[Step 2] REST API로 Blue pause..."
  kubectl exec -n "$KAFKA_NS" "$KAFKA_POD" -- \
    curl -sf -X PUT "http://${BLUE_CONNECT_API}/connectors/${BLUE_CONNECTOR}/pause"
  echo ""

  # Step 3: 즉시 상태 확인
  echo ""
  echo "[Step 3] REST API pause 직후 상태..."
  local rest_state
  rest_state=$(kubectl exec -n "$KAFKA_NS" "$KAFKA_POD" -- \
    curl -sf "http://${BLUE_CONNECT_API}/connectors/${BLUE_CONNECTOR}/status" 2>/dev/null \
    | python3 -c "import sys,json; print(json.load(sys.stdin)['connector']['state'])" 2>/dev/null)
  echo "  REST API 상태: $rest_state"

  # Step 4: CRD spec.state 확인 (변경 없어야 함)
  echo ""
  echo "[Step 4] CRD spec.state (변경 없어야 함)..."
  crd_state=$(kubectl get kafkaconnector "$BLUE_CONNECTOR" -n "$KAFKA_NS" \
    -o jsonpath='{.spec.state}' 2>/dev/null)
  echo "  CRD spec.state: $crd_state (여전히 running)"

  # Step 5: Strimzi reconcile 대기
  echo ""
  echo "[Step 5] Strimzi reconcile 대기 (최대 180초)..."
  echo "  Strimzi 기본 reconcile 주기: 약 120초"
  local elapsed=0
  local final_state=""
  while [ $elapsed -lt 180 ]; do
    final_state=$(kubectl exec -n "$KAFKA_NS" "$KAFKA_POD" -- \
      curl -sf "http://${BLUE_CONNECT_API}/connectors/${BLUE_CONNECTOR}/status" 2>/dev/null \
      | python3 -c "import sys,json; print(json.load(sys.stdin)['connector']['state'])" 2>/dev/null)
    if [ "$final_state" = "RUNNING" ]; then
      echo "  ${elapsed}초 후: RUNNING (CRD에 의해 복원됨)"
      break
    fi
    if [ $((elapsed % 30)) -eq 0 ]; then
      echo "  ${elapsed}초 경과: $final_state"
    fi
    sleep 10
    elapsed=$((elapsed + 10))
  done

  echo ""
  echo "========================================="
  if [ "$final_state" = "RUNNING" ]; then
    echo "  결과: CRD 우선 확인 (PASS)"
    echo "  REST API의 PAUSED가 Strimzi reconcile에 의해 RUNNING으로 복원됨"
    echo "  → Strimzi 환경에서는 반드시 CRD를 통해 제어해야 함"
  else
    echo "  결과: 예상과 다름 (OBSERVE)"
    echo "  최종 상태: $final_state"
    echo "  REST API pause가 유지됨 — reconcile 주기 확인 필요"
  fi
  echo "========================================="
}

# --- 안내 메시지 ---
echo "전략 E 헬퍼 함수 로드 완료."
echo "사용 가능한 함수:"
echo "  check_e_status        — Connector/Worker 상태 조회"
echo "  check_e_offsets       — Connect 그룹 offset 조회"
echo "  switch_to_green_crd   — Blue→Green 전환 (CRD)"
echo "  switch_to_blue_crd    — Green→Blue 롤백 (CRD)"
echo "  switch_to_green_rest  — Blue→Green 전환 (REST API, 비교용)"
echo "  switch_to_blue_rest   — Green→Blue 롤백 (REST API, 비교용)"
echo "  test_config_persist   — config topic 영속성 테스트"
echo "  test_crd_rest_conflict — CRD vs REST API 충돌 테스트"
