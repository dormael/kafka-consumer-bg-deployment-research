"""Loki HTTP API client for collecting Producer/Consumer log sequences.

Queries Loki's query_range endpoint to retrieve structured log entries
from bg-producer and bg-consumer applications, extracting sequence numbers,
timestamps, partition info, and offsets.
"""

import json
import logging
from datetime import datetime
from typing import List, NamedTuple, Optional

import requests

logger = logging.getLogger(__name__)


class ProducerRecord(NamedTuple):
    """A single producer log entry."""
    seq_number: int
    timestamp: datetime
    partition: int
    offset: int


class ConsumerRecord(NamedTuple):
    """A single consumer log entry."""
    seq_number: int
    timestamp: datetime
    partition: int
    offset: int
    group_id: str


class LokiClient:
    """Client for querying Loki HTTP API to collect message sequences.

    Handles pagination for large result sets and provides structured
    access to producer and consumer log entries.

    Args:
        base_url: Loki endpoint URL (e.g., http://localhost:3100).
        timeout: Request timeout in seconds.
        limit: Maximum number of entries per query request.
    """

    QUERY_RANGE_PATH = "/loki/api/v1/query_range"
    DEFAULT_LIMIT = 5000

    def __init__(
        self,
        base_url: str = "http://localhost:3100",
        timeout: int = 30,
        limit: int = DEFAULT_LIMIT,
    ) -> None:
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self.limit = limit
        self._session = requests.Session()

    def _to_nanoseconds(self, dt: datetime) -> str:
        """Convert a datetime to Loki-compatible nanosecond-epoch string."""
        epoch_seconds = dt.timestamp()
        return str(int(epoch_seconds * 1_000_000_000))

    def query_range(
        self,
        query: str,
        start: datetime,
        end: datetime,
    ) -> List[dict]:
        """Execute a query_range request against Loki, handling pagination.

        Loki returns entries in pages; this method iterates until all entries
        in the time range are collected.

        Args:
            query: LogQL query string.
            start: Range start time.
            end: Range end time.

        Returns:
            List of log entry dicts with 'timestamp_ns' and 'line' keys.

        Raises:
            ConnectionError: If Loki is unreachable.
            RuntimeError: If the Loki API returns an error response.
        """
        all_entries: List[dict] = []
        current_start = start

        while True:
            params = {
                "query": query,
                "start": self._to_nanoseconds(current_start),
                "end": self._to_nanoseconds(end),
                "limit": self.limit,
                "direction": "forward",
            }

            try:
                resp = self._session.get(
                    f"{self.base_url}{self.QUERY_RANGE_PATH}",
                    params=params,
                    timeout=self.timeout,
                )
            except requests.exceptions.ConnectionError as exc:
                raise ConnectionError(
                    f"Failed to connect to Loki at {self.base_url}: {exc}"
                ) from exc
            except requests.exceptions.Timeout as exc:
                raise ConnectionError(
                    f"Loki request timed out after {self.timeout}s: {exc}"
                ) from exc

            if resp.status_code != 200:
                raise RuntimeError(
                    f"Loki query failed (HTTP {resp.status_code}): {resp.text}"
                )

            data = resp.json()
            if data.get("status") != "success":
                raise RuntimeError(
                    f"Loki query returned non-success status: {data}"
                )

            result = data.get("data", {}).get("result", [])
            page_entries: List[dict] = []

            for stream in result:
                for ts_ns, line in stream.get("values", []):
                    page_entries.append({
                        "timestamp_ns": ts_ns,
                        "line": line,
                    })

            if not page_entries:
                break

            all_entries.extend(page_entries)

            # If we got fewer entries than the limit, we have all data.
            if len(page_entries) < self.limit:
                break

            # Advance the start time past the last entry to paginate.
            last_ns = int(page_entries[-1]["timestamp_ns"])
            # Add 1 nanosecond to avoid re-fetching the last entry.
            current_start_ns = last_ns + 1
            current_start = datetime.fromtimestamp(
                current_start_ns / 1_000_000_000,
                tz=start.tzinfo,
            )

            logger.debug(
                "Fetched %d entries in this page, %d total so far",
                len(page_entries),
                len(all_entries),
            )

        logger.info("Collected %d total entries from Loki", len(all_entries))
        return all_entries

    def _parse_timestamp(self, ts_ns: str) -> datetime:
        """Parse a nanosecond-epoch timestamp string to a datetime."""
        return datetime.utcfromtimestamp(int(ts_ns) / 1_000_000_000)

    def _parse_json_line(self, line: str) -> Optional[dict]:
        """Attempt to parse a log line as JSON.

        Args:
            line: Raw log line string.

        Returns:
            Parsed dict, or None if parsing fails.
        """
        try:
            return json.loads(line)
        except (json.JSONDecodeError, TypeError):
            logger.debug("Skipping non-JSON log line: %.100s...", line)
            return None

    def get_producer_sequences(
        self,
        start: datetime,
        end: datetime,
    ) -> List[ProducerRecord]:
        """Collect producer message sequences from Loki.

        Queries for log entries from the bg-producer app that contain
        "Message sent", parses JSON fields, and returns structured records.

        Args:
            start: Query start time.
            end: Query end time.

        Returns:
            List of ProducerRecord tuples sorted by sequence number.
        """
        query = '{app="bg-producer"} |= "Message sent" | json'
        entries = self.query_range(query, start, end)

        records: List[ProducerRecord] = []
        for entry in entries:
            parsed = self._parse_json_line(entry["line"])
            if parsed is None:
                continue

            try:
                record = ProducerRecord(
                    seq_number=int(parsed["seq"]),
                    timestamp=self._parse_timestamp(entry["timestamp_ns"]),
                    partition=int(parsed.get("partition", -1)),
                    offset=int(parsed.get("offset", -1)),
                )
                records.append(record)
            except (KeyError, ValueError, TypeError) as exc:
                logger.warning(
                    "Skipping malformed producer entry: %s (error: %s)",
                    entry["line"][:200],
                    exc,
                )

        records.sort(key=lambda r: r.seq_number)
        logger.info("Parsed %d producer records", len(records))
        return records

    def get_consumer_sequences(
        self,
        start: datetime,
        end: datetime,
    ) -> List[ConsumerRecord]:
        """Collect consumer message sequences from Loki.

        Queries for log entries from the bg-consumer app that contain
        "Message consumed", parses JSON fields, and returns structured records.

        Args:
            start: Query start time.
            end: Query end time.

        Returns:
            List of ConsumerRecord tuples sorted by sequence number.
        """
        query = '{app="bg-consumer"} |= "Message consumed" | json'
        entries = self.query_range(query, start, end)

        records: List[ConsumerRecord] = []
        for entry in entries:
            parsed = self._parse_json_line(entry["line"])
            if parsed is None:
                continue

            try:
                record = ConsumerRecord(
                    seq_number=int(parsed["seq"]),
                    timestamp=self._parse_timestamp(entry["timestamp_ns"]),
                    partition=int(parsed.get("partition", -1)),
                    offset=int(parsed.get("offset", -1)),
                    group_id=str(parsed.get("group_id", "unknown")),
                )
                records.append(record)
            except (KeyError, ValueError, TypeError) as exc:
                logger.warning(
                    "Skipping malformed consumer entry: %s (error: %s)",
                    entry["line"][:200],
                    exc,
                )

        records.sort(key=lambda r: r.seq_number)
        logger.info("Parsed %d consumer records", len(records))
        return records

    def close(self) -> None:
        """Close the underlying HTTP session."""
        self._session.close()

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc_val, exc_tb):
        self.close()
        return False
