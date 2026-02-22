#!/usr/bin/env bash
# =============================================================================
# 전략 B 전환/롤백 헬퍼 스크립트
# 사용법: source tools/strategy-b-switch.sh
#
# 주요 함수:
#   check_offsets       — Blue/Green 그룹 offset 조회
#   check_b_status      — Deployment/Pod 상태 조회
#   switch_to_green     — Blue→Green 전환 자동화
#   switch_to_blue      — Green→Blue 롤백 (역방향)
#   inject_fault        — 장애 주입 (port-forward 기반)
#   clear_fault         — 장애 주입 해제
# =============================================================================

# --- 상수 ---
NS="kafka-bg-test"
KAFKA_NS="kafka"
KAFKA_POD="kafka-cluster-dual-role-0"
KAFKA_BOOTSTRAP="localhost:9092"
TOPIC="bg-test-topic"
BLUE_GROUP="bg-test-group-blue"
GREEN_GROUP="bg-test-group-green"
BLUE_DEPLOY="consumer-b-blue"
GREEN_DEPLOY="consumer-b-green"
REPLICAS=3

# --- Offset 조회 ---

check_offsets() {
  echo "=== Blue Group ($BLUE_GROUP) ==="
  kubectl exec -n "$KAFKA_NS" "$KAFKA_POD" -- \
    bin/kafka-consumer-groups.sh --bootstrap-server "$KAFKA_BOOTSTRAP" \
      --group "$BLUE_GROUP" --describe 2>/dev/null
  echo ""
  echo "=== Green Group ($GREEN_GROUP) ==="
  kubectl exec -n "$KAFKA_NS" "$KAFKA_POD" -- \
    bin/kafka-consumer-groups.sh --bootstrap-server "$KAFKA_BOOTSTRAP" \
      --group "$GREEN_GROUP" --describe 2>/dev/null
}

# --- Deployment/Pod 상태 ---

check_b_status() {
  echo "=== Deployments ==="
  kubectl get deploy "$BLUE_DEPLOY" "$GREEN_DEPLOY" -n "$NS" \
    -o custom-columns='NAME:.metadata.name,READY:.status.readyReplicas,DESIRED:.spec.replicas' \
    2>/dev/null
  echo ""
  echo "=== Pods ==="
  kubectl get pods -n "$NS" -l strategy=b \
    -o custom-columns='NAME:.metadata.name,STATUS:.status.phase,READY:.status.conditions[?(@.type=="Ready")].status,AGE:.metadata.creationTimestamp' \
    2>/dev/null
  echo ""
  echo "=== ConfigMap Active Version ==="
  kubectl get configmap kafka-consumer-active-version -n "$NS" \
    -o jsonpath='{.data.active}' 2>/dev/null && echo ""
}

# --- Blue→Green 전환 ---

switch_to_green() {
  echo "========================================="
  echo "  전략 B: Blue → Green 전환 시작"
  echo "========================================="

  local SWITCH_START
  SWITCH_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  local START_EPOCH
  START_EPOCH=$(date +%s)
  echo "[$(date -u +%T)] 전환 시작: $SWITCH_START"

  # Step 1: Blue의 현재 offset 저장
  echo ""
  echo "[Step 1] Blue 현재 offset 확인..."
  kubectl exec -n "$KAFKA_NS" "$KAFKA_POD" -- \
    bin/kafka-consumer-groups.sh --bootstrap-server "$KAFKA_BOOTSTRAP" \
      --group "$BLUE_GROUP" --describe 2>/dev/null

  # Step 2: Blue scale down (소비 중단)
  echo ""
  echo "[Step 2] Blue scale down (replicas=0)..."
  kubectl scale deployment "$BLUE_DEPLOY" -n "$NS" --replicas=0

  # Step 3: Blue graceful shutdown 대기 (offset commit 완료)
  echo ""
  echo "[Step 3] Blue shutdown 대기 (10초)..."
  kubectl wait --for=jsonpath='{.status.readyReplicas}'=0 \
    deployment/"$BLUE_DEPLOY" -n "$NS" --timeout=30s 2>/dev/null || true
  sleep 10

  # Step 4: Blue의 최종 committed offset 확인
  echo ""
  echo "[Step 4] Blue 최종 committed offset..."
  kubectl exec -n "$KAFKA_NS" "$KAFKA_POD" -- \
    bin/kafka-consumer-groups.sh --bootstrap-server "$KAFKA_BOOTSTRAP" \
      --group "$BLUE_GROUP" --describe 2>/dev/null

  # Step 5: Green group offset을 topic의 현재 end offset으로 리셋
  # --to-current: Blue shutdown 이후이므로 log-end-offset과 일치
  echo ""
  echo "[Step 5] Green group offset 동기화 (--to-current)..."
  kubectl exec -n "$KAFKA_NS" "$KAFKA_POD" -- \
    bin/kafka-consumer-groups.sh --bootstrap-server "$KAFKA_BOOTSTRAP" \
      --group "$GREEN_GROUP" --topic "$TOPIC" \
      --reset-offsets --to-current --execute 2>/dev/null

  # Step 6: Green scale up
  echo ""
  echo "[Step 6] Green scale up (replicas=$REPLICAS)..."
  kubectl scale deployment "$GREEN_DEPLOY" -n "$NS" --replicas="$REPLICAS"

  # Step 7: Green rollout 대기
  echo ""
  echo "[Step 7] Green rollout 대기..."
  kubectl rollout status deployment "$GREEN_DEPLOY" -n "$NS" --timeout=120s

  # Step 8: ConfigMap 업데이트
  echo ""
  echo "[Step 8] ConfigMap active → green..."
  kubectl patch configmap kafka-consumer-active-version -n "$NS" \
    --type merge -p '{"data":{"active":"green"}}'

  local END_EPOCH
  END_EPOCH=$(date +%s)
  local SWITCH_END
  SWITCH_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  local DURATION=$(( END_EPOCH - START_EPOCH ))

  echo ""
  echo "========================================="
  echo "  전환 완료!"
  echo "  시작: $SWITCH_START"
  echo "  종료: $SWITCH_END"
  echo "  소요: ${DURATION}초"
  echo "========================================="

  # 환경 변수로 시각 정보 export (Validator 사용 시 활용)
  export SWITCH_START SWITCH_END
}

# --- Green→Blue 롤백 ---

switch_to_blue() {
  echo "========================================="
  echo "  전략 B: Green → Blue 롤백 시작"
  echo "========================================="

  local ROLLBACK_START
  ROLLBACK_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  local START_EPOCH
  START_EPOCH=$(date +%s)
  echo "[$(date -u +%T)] 롤백 시작: $ROLLBACK_START"

  # Step 1: Green의 현재 offset 확인
  echo ""
  echo "[Step 1] Green 현재 offset 확인..."
  kubectl exec -n "$KAFKA_NS" "$KAFKA_POD" -- \
    bin/kafka-consumer-groups.sh --bootstrap-server "$KAFKA_BOOTSTRAP" \
      --group "$GREEN_GROUP" --describe 2>/dev/null

  # Step 2: Green scale down
  echo ""
  echo "[Step 2] Green scale down (replicas=0)..."
  kubectl scale deployment "$GREEN_DEPLOY" -n "$NS" --replicas=0

  # Step 3: Green graceful shutdown 대기
  echo ""
  echo "[Step 3] Green shutdown 대기 (10초)..."
  kubectl wait --for=jsonpath='{.status.readyReplicas}'=0 \
    deployment/"$GREEN_DEPLOY" -n "$NS" --timeout=30s 2>/dev/null || true
  sleep 10

  # Step 4: Blue group offset 동기화
  echo ""
  echo "[Step 4] Blue group offset 동기화 (--to-current)..."
  kubectl exec -n "$KAFKA_NS" "$KAFKA_POD" -- \
    bin/kafka-consumer-groups.sh --bootstrap-server "$KAFKA_BOOTSTRAP" \
      --group "$BLUE_GROUP" --topic "$TOPIC" \
      --reset-offsets --to-current --execute 2>/dev/null

  # Step 5: Blue scale up
  echo ""
  echo "[Step 5] Blue scale up (replicas=$REPLICAS)..."
  kubectl scale deployment "$BLUE_DEPLOY" -n "$NS" --replicas="$REPLICAS"

  # Step 6: Blue rollout 대기
  echo ""
  echo "[Step 6] Blue rollout 대기..."
  kubectl rollout status deployment "$BLUE_DEPLOY" -n "$NS" --timeout=120s

  # Step 7: ConfigMap 업데이트
  echo ""
  echo "[Step 7] ConfigMap active → blue..."
  kubectl patch configmap kafka-consumer-active-version -n "$NS" \
    --type merge -p '{"data":{"active":"blue"}}'

  local END_EPOCH
  END_EPOCH=$(date +%s)
  local ROLLBACK_END
  ROLLBACK_END=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  local DURATION=$(( END_EPOCH - START_EPOCH ))

  echo ""
  echo "========================================="
  echo "  롤백 완료!"
  echo "  시작: $ROLLBACK_START"
  echo "  종료: $ROLLBACK_END"
  echo "  소요: ${DURATION}초"
  echo "========================================="

  export ROLLBACK_START ROLLBACK_END
}

# --- 장애 주입 (port-forward 기반) ---
# 전략 B에서는 Sidecar가 없으므로 port-forward를 사용하여 Consumer에 직접 접근

inject_fault() {
  local deploy=$1   # consumer-b-blue 또는 consumer-b-green
  local fault=$2    # processing-delay, error-rate, commit-delay
  local payload=$3  # JSON payload, 예: '{"delayMs":200}'

  if [[ -z "$deploy" || -z "$fault" || -z "$payload" ]]; then
    echo "사용법: inject_fault <deployment> <fault-type> '<json-payload>'"
    echo "예시:"
    echo "  inject_fault consumer-b-blue processing-delay '{\"delayMs\":200}'"
    echo "  inject_fault consumer-b-green error-rate '{\"errorRatePercent\":80}'"
    return 1
  fi

  echo "=== $deploy 에 $fault 주입 ==="
  local pods
  pods=$(kubectl get pods -n "$NS" -l "strategy=b" \
    -l "app=bg-consumer" \
    --field-selector=status.phase=Running \
    -o jsonpath='{range .items[?(@.metadata.ownerReferences[0].name=="'"$(kubectl get deploy "$deploy" -n "$NS" -o jsonpath='{.metadata.uid}' 2>/dev/null)"'")]}{.metadata.name}{"\n"}{end}' 2>/dev/null)

  # ownerReferences UID 매칭이 복잡하므로, 레이블 기반으로 Pod 선택
  local color
  if [[ "$deploy" == *blue* ]]; then
    color="blue"
  else
    color="green"
  fi

  pods=$(kubectl get pods -n "$NS" -l "strategy=b,color=$color" \
    --field-selector=status.phase=Running \
    -o jsonpath='{.items[*].metadata.name}')

  if [[ -z "$pods" ]]; then
    echo "경고: $deploy 에 Running Pod가 없습니다."
    return 1
  fi

  for pod in $pods; do
    echo "  $pod: port-forward로 장애 주입 중..."
    # 임시 port-forward 시작
    local local_port
    local_port=$(shuf -i 10000-60000 -n 1)
    kubectl port-forward -n "$NS" "$pod" "${local_port}:8080" &>/dev/null &
    local pf_pid=$!
    sleep 1

    curl -s -X PUT "http://localhost:${local_port}/fault/${fault}" \
      -H "Content-Type: application/json" -d "$payload"
    echo ""

    # port-forward 종료
    kill "$pf_pid" 2>/dev/null
    wait "$pf_pid" 2>/dev/null
  done
  echo "=== 완료 ==="
}

# --- 장애 주입 해제 ---

clear_fault() {
  local deploy=$1   # consumer-b-blue 또는 consumer-b-green
  local fault=$2    # processing-delay, error-rate, commit-delay

  if [[ -z "$deploy" || -z "$fault" ]]; then
    echo "사용법: clear_fault <deployment> <fault-type>"
    echo "예시:"
    echo "  clear_fault consumer-b-blue processing-delay"
    echo "  clear_fault consumer-b-green error-rate"
    return 1
  fi

  local zero_payload
  case "$fault" in
    processing-delay) zero_payload='{"delayMs":0}' ;;
    error-rate)       zero_payload='{"errorRatePercent":0}' ;;
    commit-delay)     zero_payload='{"delayMs":0}' ;;
    *)
      echo "알 수 없는 fault type: $fault"
      return 1
      ;;
  esac

  inject_fault "$deploy" "$fault" "$zero_payload"
}

# --- 안내 메시지 ---
echo "전략 B 헬퍼 함수 로드 완료."
echo "사용 가능한 함수:"
echo "  check_offsets      — Blue/Green 그룹 offset 조회"
echo "  check_b_status     — Deployment/Pod 상태 조회"
echo "  switch_to_green    — Blue→Green 전환 (자동화)"
echo "  switch_to_blue     — Green→Blue 롤백 (자동화)"
echo "  inject_fault       — 장애 주입"
echo "  clear_fault        — 장애 주입 해제"
