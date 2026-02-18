# Tutorial 11: Validator 스크립트 사용법

> Producer/Consumer 로그의 시퀀스 번호를 비교하여 메시지 유실/중복을 자동 검증합니다.

---

## Step 1: 설치

```bash
cd tools/validator
pip install -r requirements.txt
```

## Step 2: Loki 기반 검증

```bash
# Loki port-forward
kubectl port-forward svc/loki -n monitoring 3100:3100

# 검증 실행
python validator.py \
  --loki-url http://localhost:3100 \
  --start "2026-02-18T10:00:00Z" \
  --end "2026-02-18T10:30:00Z" \
  --consumer-color green \
  --scenario "scenario-1-strategy-C" \
  --output report/scenario-1-result.md
```

## Step 3: 파일 기반 검증 (Loki 대안)

```bash
# Producer 로그 추출
kubectl logs deployment/bg-test-producer -n kafka > /tmp/producer.log

# Consumer 로그 추출
kubectl logs consumer-green-0 -n kafka -c consumer > /tmp/consumer-green.log

# 파일 기반 검증
python validator.py \
  --producer-log /tmp/producer.log \
  --consumer-log /tmp/consumer-green.log \
  --scenario "scenario-1-strategy-C" \
  --output report/scenario-1-result.md
```

## Step 4: 결과 확인

생성된 `report/scenario-1-result.md`를 확인합니다.

```bash
cat report/scenario-1-result.md
```

예상 출력:
```markdown
## 시나리오: scenario-1-strategy-C

| 항목 | 값 |
|------|-----|
| 총 Produce 건수 | 180000 |
| 총 Consume 건수 | 180002 |
| 유니크 Consume 건수 | 180000 |
| **유실 건수** | **0** |
| **중복 건수** | **2** |
```

---

## 다음 단계

- [Tutorial 12: Grafana 대시보드](12-grafana-dashboard.md)
