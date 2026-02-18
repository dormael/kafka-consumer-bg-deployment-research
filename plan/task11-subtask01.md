# Task 11: Validator 스크립트 구현

**Phase:** 2 - 애플리케이션 구현
**의존성:** task07 (Producer 로그 포맷), task08 (Consumer 로그 포맷)
**예상 소요:** 1시간 ~ 2시간
**튜토리얼:** `tutorial/11-validator-script.md`

---

## 목표

Producer 발행 로그와 Consumer 수신 로그를 비교하여 메시지 유실/중복 시퀀스를 자동 산출하는 Validator 스크립트를 구현한다.

---

## 프로젝트 구조

```
tools/validator/
├── validator.py           # 메인 검증 스크립트
├── loki_client.py         # Loki LogQL 쿼리 클라이언트
├── report_generator.py    # 결과 리포트 생성
├── requirements.txt
└── README.md
```

---

## Subtask 11-01: Loki 로그 수집 클라이언트

```python
class LokiClient:
    def __init__(self, loki_url: str):
        self.base_url = loki_url

    def query_range(self, logql: str, start: str, end: str) -> list[dict]:
        """LogQL 쿼리 실행 후 로그 엔트리 반환"""
        # GET /loki/api/v1/query_range
        pass

    def get_producer_sequences(self, start: str, end: str) -> set[int]:
        """Producer 로그에서 시퀀스 번호 추출"""
        logql = '{app="bg-test-producer"} |= "[SEQ:" | regexp `\\[SEQ:(?P<seq>\\d+)\\]`'
        # 결과에서 seq 번호 파싱하여 set으로 반환

    def get_consumer_sequences(self, color: str, start: str, end: str) -> dict[int, int]:
        """Consumer 로그에서 시퀀스 번호 추출 (시퀀스 → 수신 횟수)"""
        logql = f'{{app="bg-test-consumer", color="{color}"}} |= "[CONSUMED][SEQ:" | regexp `\\[SEQ:(?P<seq>\\d+)\\]`'
        # 결과에서 seq 번호 파싱, Counter로 중복 횟수 계산
```

## Subtask 11-02: 시퀀스 검증 로직

```python
class SequenceValidator:
    def validate(self, produced: set[int], consumed: dict[int, int]) -> ValidationResult:
        consumed_set = set(consumed.keys())

        lost = produced - consumed_set          # 유실: produce됐지만 consume 안 됨
        unexpected = consumed_set - produced     # 예상 외: produce 안 됐지만 consume 됨
        duplicated = {seq: count for seq, count in consumed.items() if count > 1}

        return ValidationResult(
            total_produced=len(produced),
            total_consumed=sum(consumed.values()),
            unique_consumed=len(consumed_set),
            lost_sequences=sorted(lost),
            lost_count=len(lost),
            duplicate_sequences=duplicated,
            duplicate_count=sum(c - 1 for c in duplicated.values()),
            unexpected_sequences=sorted(unexpected),
        )
```

## Subtask 11-03: 파일 기반 검증 (Loki 대안)

Loki가 불가한 경우 파일 기반으로도 검증 가능하도록 한다.

```python
class FileLogParser:
    def parse_producer_log(self, log_file: str) -> set[int]:
        """Producer 로그 파일에서 시퀀스 번호 추출"""
        # [SEQ:12345] 패턴 매칭

    def parse_consumer_log(self, log_file: str) -> dict[int, int]:
        """Consumer 로그 파일에서 시퀀스 번호 추출"""
        # [CONSUMED][SEQ:12345] 패턴 매칭
```

## Subtask 11-04: 리포트 생성

```python
class ReportGenerator:
    def generate_markdown(self, result: ValidationResult, scenario: str) -> str:
        """Markdown 형식의 검증 결과 리포트 생성"""
        return f"""
## 시나리오: {scenario}

| 항목 | 값 |
|------|-----|
| 총 Produce 건수 | {result.total_produced} |
| 총 Consume 건수 | {result.total_consumed} |
| 유니크 Consume 건수 | {result.unique_consumed} |
| **유실 건수** | **{result.lost_count}** |
| **중복 건수** | **{result.duplicate_count}** |

### 유실 시퀀스 (상위 10건)
{result.lost_sequences[:10]}

### 중복 시퀀스 (상위 10건)
{list(result.duplicate_sequences.items())[:10]}
"""
```

## Subtask 11-05: CLI 인터페이스

```bash
# Loki 기반 검증
python validator.py \
  --loki-url http://localhost:3100 \
  --start "2026-02-18T10:00:00Z" \
  --end "2026-02-18T10:30:00Z" \
  --consumer-color green \
  --scenario "scenario-1-normal-switch" \
  --output report/scenario-1-result.md

# 파일 기반 검증
python validator.py \
  --producer-log /tmp/producer.log \
  --consumer-log /tmp/consumer-green.log \
  --scenario "scenario-1-normal-switch" \
  --output report/scenario-1-result.md
```

---

## 완료 기준

- [ ] Loki에서 Producer/Consumer 로그를 정상 쿼리 가능
- [ ] 시퀀스 번호 기반 유실/중복 건수 정확히 산출
- [ ] Markdown 리포트 자동 생성
- [ ] 파일 기반 검증 모드 동작
- [ ] TPS 100 기준 30분 분량(약 180,000건)의 시퀀스 검증 가능
