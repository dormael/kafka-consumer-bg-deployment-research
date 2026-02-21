# Task 04: Validator 스크립트 구현

> **의존:** task02 (Producer/Consumer 로그 포맷 정의)
> **경로:** `tools/validator/`
> **언어:** Python 3.9+

---

## 목표

Producer 발행 로그와 Consumer 수신 로그를 비교하여 메시지 유실/중복 시퀀스를 자동으로 산출하는 Validator 스크립트를 구현한다. TPS 100 기준 수만 건의 메시지를 수동 검증하면 신뢰도를 보장하기 어렵기 때문이다.

## 기능 요구사항

### 1. 로그 수집

두 가지 소스에서 시퀀스 번호를 수집한다:

**방법 A: Loki HTTP API 쿼리**
```python
# Producer 시퀀스 수집
GET /loki/api/v1/query_range
  query: {app="bg-producer"} |= "Message sent" | json | line_format "{{.seq}}"
  start: <test_start_time>
  end: <test_end_time>

# Consumer 시퀀스 수집
GET /loki/api/v1/query_range
  query: {app="bg-consumer"} |= "Message consumed" | json | line_format "{{.seq}}"
  start: <test_start_time>
  end: <test_end_time>
```

**방법 B: 파일 기반 (Loki 장애 시 폴백)**
- Producer/Consumer가 시퀀스 번호를 파일(`/tmp/sequences.log`)에도 기록
- PV(PersistentVolume)로 마운트하여 Pod 외부에서 접근

### 2. 시퀀스 비교 분석

```python
def analyze(produced_sequences: set, consumed_sequences: set) -> dict:
    missing = produced_sequences - consumed_sequences  # 유실
    duplicates = [s for s in consumed if consumed.count(s) > 1]  # 중복
    extra = consumed_sequences - produced_sequences  # 예상 외 수신

    return {
        "total_produced": len(produced_sequences),
        "total_consumed": len(consumed_sequences),
        "missing_count": len(missing),
        "missing_sequences": sorted(missing),
        "duplicate_count": len(duplicates),
        "duplicate_sequences": sorted(set(duplicates)),
        "extra_count": len(extra),
        "extra_sequences": sorted(extra),
        "loss_rate": len(missing) / len(produced_sequences) * 100,
        "duplication_rate": len(duplicates) / len(consumed_sequences) * 100,
    }
```

### 3. 시간 범위 분석

전환 전/중/후 구간별로 유실/중복을 분리하여 정확히 언제 문제가 발생했는지 추적한다.

```python
def analyze_by_phase(sequences, switch_start, switch_end):
    pre_switch = [s for s in sequences if s.timestamp < switch_start]
    during_switch = [s for s in sequences if switch_start <= s.timestamp <= switch_end]
    post_switch = [s for s in sequences if s.timestamp > switch_end]
    # 각 구간별 analyze() 수행
```

### 4. 리포트 출력

```
=== Blue/Green Switch Validation Report ===
Period: 2026-02-20T10:00:00Z ~ 2026-02-20T10:10:00Z
Strategy: C (Pause/Resume Atomic Switch)

--- Summary ---
Total Produced:     60,000
Total Consumed:     60,003
Missing (Loss):     0 (0.000%)
Duplicates:         3 (0.005%)
Extra:              0

--- Phase Analysis ---
Pre-Switch:    Produced=30,000  Consumed=30,000  Loss=0  Dup=0
During Switch: Produced=200     Consumed=203     Loss=0  Dup=3
Post-Switch:   Produced=29,800  Consumed=29,800  Loss=0  Dup=0

--- Duplicate Details ---
Seq#45123: consumed 2 times (partitions: [3, 3], offsets: [1234, 1234])
Seq#45124: consumed 2 times (partitions: [3, 3], offsets: [1235, 1235])
Seq#45125: consumed 2 times (partitions: [3, 3], offsets: [1236, 1236])

--- RESULT: PASS (Loss=0, Dup within tolerance) ---
```

## 프로젝트 구조

```
tools/validator/
├── validator.py          # 메인 스크립트
├── loki_client.py        # Loki HTTP API 클라이언트
├── file_reader.py        # 파일 기반 시퀀스 리더
├── analyzer.py           # 시퀀스 비교 분석 로직
├── reporter.py           # 리포트 생성 (stdout + markdown)
├── requirements.txt      # requests
└── README.md             # 사용 방법
```

## CLI 인터페이스

```bash
# Loki 기반 분석
python validator.py \
  --source loki \
  --loki-url http://localhost:3100 \
  --start "2026-02-20T10:00:00Z" \
  --end "2026-02-20T10:10:00Z" \
  --switch-start "2026-02-20T10:05:00Z" \
  --switch-end "2026-02-20T10:05:05Z" \
  --strategy C \
  --output report/validation-strategy-c-scenario1.md

# 파일 기반 분석
python validator.py \
  --source file \
  --producer-log /data/producer-sequences.log \
  --consumer-log /data/consumer-sequences.log \
  --strategy C \
  --output report/validation-strategy-c-scenario1.md
```

## 완료 기준

- [ ] Loki에서 Producer/Consumer 로그 쿼리 성공
- [ ] 시퀀스 비교로 유실/중복 정확히 산출
- [ ] 구간별 (전환 전/중/후) 분석 동작
- [ ] Markdown 형식 리포트 자동 생성
- [ ] 1만 건 이상 시퀀스에 대해 수 초 내 분석 완료
