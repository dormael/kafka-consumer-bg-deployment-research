"""File-based sequence reader for Producer/Consumer logs.

Provides a fallback data source when Loki is unavailable. Reads structured
JSON log lines from local files, streaming line-by-line to handle large
datasets efficiently.
"""

import json
import logging
from datetime import datetime
from pathlib import Path
from typing import List, Optional

from loki_client import ConsumerRecord, ProducerRecord

logger = logging.getLogger(__name__)


class FileReader:
    """Reads Producer and Consumer sequence logs from local files.

    Log files are expected to contain one JSON object per line, with fields
    matching the structured log output of the bg-producer and bg-consumer
    applications.

    Expected producer log line format:
        {"seq": 12345, "timestamp": "2026-02-20T10:00:01Z", "partition": 3, "offset": 567, "message": "Message sent"}

    Expected consumer log line format:
        {"seq": 12345, "timestamp": "2026-02-20T10:00:01Z", "partition": 3, "offset": 567, "group_id": "bg-consumer-group", "message": "Message consumed"}
    """

    def _parse_timestamp(self, ts_str: str) -> datetime:
        """Parse an ISO 8601 timestamp string to a datetime.

        Handles both formats with and without the trailing 'Z', as well as
        timezone offset formats.

        Args:
            ts_str: ISO 8601 timestamp string.

        Returns:
            Parsed datetime object.
        """
        # Strip trailing 'Z' and replace with +00:00 for fromisoformat
        if ts_str.endswith("Z"):
            ts_str = ts_str[:-1] + "+00:00"
        try:
            return datetime.fromisoformat(ts_str)
        except ValueError:
            # Fallback: try parsing without timezone info
            return datetime.strptime(
                ts_str.rstrip("Z"), "%Y-%m-%dT%H:%M:%S"
            )

    def _parse_json_line(self, line: str, line_number: int, filepath: str) -> Optional[dict]:
        """Parse a single JSON log line.

        Args:
            line: Raw line from the log file.
            line_number: Line number for error reporting.
            filepath: File path for error reporting.

        Returns:
            Parsed dict, or None if the line is empty or malformed.
        """
        line = line.strip()
        if not line:
            return None

        try:
            return json.loads(line)
        except json.JSONDecodeError as exc:
            logger.warning(
                "Skipping malformed JSON at %s:%d: %s (error: %s)",
                filepath,
                line_number,
                line[:200],
                exc,
            )
            return None

    def read_producer_sequences(self, filepath: str) -> List[ProducerRecord]:
        """Read producer sequence records from a log file.

        Streams the file line-by-line to handle large files efficiently.
        Lines that are not valid JSON or lack required fields are skipped
        with a warning.

        Args:
            filepath: Path to the producer log file.

        Returns:
            List of ProducerRecord tuples sorted by sequence number.

        Raises:
            FileNotFoundError: If the file does not exist.
            PermissionError: If the file cannot be read.
        """
        path = Path(filepath)
        if not path.exists():
            raise FileNotFoundError(f"Producer log file not found: {filepath}")
        if not path.is_file():
            raise ValueError(f"Path is not a regular file: {filepath}")

        records: List[ProducerRecord] = []
        skipped = 0

        with open(path, "r", encoding="utf-8") as fh:
            for line_number, line in enumerate(fh, start=1):
                parsed = self._parse_json_line(line, line_number, filepath)
                if parsed is None:
                    continue

                try:
                    record = ProducerRecord(
                        seq_number=int(parsed["seq"]),
                        timestamp=self._parse_timestamp(
                            parsed.get("timestamp", "1970-01-01T00:00:00Z")
                        ),
                        partition=int(parsed.get("partition", -1)),
                        offset=int(parsed.get("offset", -1)),
                    )
                    records.append(record)
                except (KeyError, ValueError, TypeError) as exc:
                    skipped += 1
                    logger.warning(
                        "Skipping producer entry at %s:%d: %s",
                        filepath,
                        line_number,
                        exc,
                    )

        records.sort(key=lambda r: r.seq_number)
        logger.info(
            "Read %d producer records from %s (%d skipped)",
            len(records),
            filepath,
            skipped,
        )
        return records

    def read_consumer_sequences(self, filepath: str) -> List[ConsumerRecord]:
        """Read consumer sequence records from a log file.

        Streams the file line-by-line to handle large files efficiently.
        Lines that are not valid JSON or lack required fields are skipped
        with a warning.

        Args:
            filepath: Path to the consumer log file.

        Returns:
            List of ConsumerRecord tuples sorted by sequence number.

        Raises:
            FileNotFoundError: If the file does not exist.
            PermissionError: If the file cannot be read.
        """
        path = Path(filepath)
        if not path.exists():
            raise FileNotFoundError(f"Consumer log file not found: {filepath}")
        if not path.is_file():
            raise ValueError(f"Path is not a regular file: {filepath}")

        records: List[ConsumerRecord] = []
        skipped = 0

        with open(path, "r", encoding="utf-8") as fh:
            for line_number, line in enumerate(fh, start=1):
                parsed = self._parse_json_line(line, line_number, filepath)
                if parsed is None:
                    continue

                try:
                    record = ConsumerRecord(
                        seq_number=int(parsed["seq"]),
                        timestamp=self._parse_timestamp(
                            parsed.get("timestamp", "1970-01-01T00:00:00Z")
                        ),
                        partition=int(parsed.get("partition", -1)),
                        offset=int(parsed.get("offset", -1)),
                        group_id=str(parsed.get("group_id", "unknown")),
                    )
                    records.append(record)
                except (KeyError, ValueError, TypeError) as exc:
                    skipped += 1
                    logger.warning(
                        "Skipping consumer entry at %s:%d: %s",
                        filepath,
                        line_number,
                        exc,
                    )

        records.sort(key=lambda r: r.seq_number)
        logger.info(
            "Read %d consumer records from %s (%d skipped)",
            len(records),
            filepath,
            skipped,
        )
        return records
