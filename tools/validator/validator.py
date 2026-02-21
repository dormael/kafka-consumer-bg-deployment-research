#!/usr/bin/env python3
"""Kafka Consumer Blue/Green Deployment Validator.

Compares Producer publish logs and Consumer receive logs to automatically
calculate message loss and duplication sequences. Supports two data sources:
Loki HTTP API queries and local log files.

Usage examples:

    # Loki-based analysis with phase breakdown
    python validator.py \\
        --source loki \\
        --loki-url http://localhost:3100 \\
        --start "2026-02-20T10:00:00Z" \\
        --end "2026-02-20T10:10:00Z" \\
        --switch-start "2026-02-20T10:05:00Z" \\
        --switch-end "2026-02-20T10:05:05Z" \\
        --strategy C \\
        --output report/validation-strategy-c-scenario1.md

    # File-based analysis
    python validator.py \\
        --source file \\
        --producer-log /data/producer-sequences.log \\
        --consumer-log /data/consumer-sequences.log \\
        --strategy C \\
        --output report/validation-strategy-c-scenario1.md
"""

import argparse
import logging
import sys
from datetime import datetime, timezone
from typing import Optional

from analyzer import SequenceAnalyzer
from file_reader import FileReader
from loki_client import LokiClient
from reporter import ReportGenerator

logger = logging.getLogger(__name__)


def parse_iso8601(value: str) -> datetime:
    """Parse an ISO 8601 timestamp string into a timezone-aware datetime.

    Args:
        value: ISO 8601 formatted string (e.g., "2026-02-20T10:00:00Z").

    Returns:
        Timezone-aware datetime object.

    Raises:
        argparse.ArgumentTypeError: If the string cannot be parsed.
    """
    try:
        if value.endswith("Z"):
            value = value[:-1] + "+00:00"
        dt = datetime.fromisoformat(value)
        # Ensure timezone-aware; assume UTC if naive.
        if dt.tzinfo is None:
            dt = dt.replace(tzinfo=timezone.utc)
        return dt
    except ValueError as exc:
        raise argparse.ArgumentTypeError(
            f"Invalid ISO 8601 timestamp: {value!r} ({exc})"
        ) from exc


def build_parser() -> argparse.ArgumentParser:
    """Build and return the CLI argument parser."""
    parser = argparse.ArgumentParser(
        description="Kafka Consumer Blue/Green Deployment Validator",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=(
            "Examples:\n"
            "  # Loki source\n"
            "  python validator.py --source loki --loki-url http://localhost:3100 \\\n"
            '    --start "2026-02-20T10:00:00Z" --end "2026-02-20T10:10:00Z" \\\n'
            '    --switch-start "2026-02-20T10:05:00Z" --switch-end "2026-02-20T10:05:05Z" \\\n'
            "    --strategy C --output report/validation-strategy-c.md\n"
            "\n"
            "  # File source\n"
            "  python validator.py --source file \\\n"
            "    --producer-log /data/producer.log --consumer-log /data/consumer.log \\\n"
            "    --strategy C --output report/validation-strategy-c.md\n"
        ),
    )

    parser.add_argument(
        "--source",
        choices=["loki", "file"],
        required=True,
        help="Data source: 'loki' for Loki HTTP API, 'file' for local log files.",
    )
    parser.add_argument(
        "--loki-url",
        default="http://localhost:3100",
        help="Loki endpoint URL (default: http://localhost:3100).",
    )
    parser.add_argument(
        "--start",
        type=parse_iso8601,
        help="Test start time (ISO 8601). Required for Loki source.",
    )
    parser.add_argument(
        "--end",
        type=parse_iso8601,
        help="Test end time (ISO 8601). Required for Loki source.",
    )
    parser.add_argument(
        "--switch-start",
        type=parse_iso8601,
        help="Switch start time (ISO 8601). Optional for phase analysis.",
    )
    parser.add_argument(
        "--switch-end",
        type=parse_iso8601,
        help="Switch end time (ISO 8601). Optional for phase analysis.",
    )
    parser.add_argument(
        "--strategy",
        choices=["B", "C", "E"],
        required=True,
        help="Strategy name: B, C, or E.",
    )
    parser.add_argument(
        "--output",
        help="Output file path for Markdown report. If omitted, only text is printed.",
    )
    parser.add_argument(
        "--producer-log",
        help="Producer log file path (required for file source).",
    )
    parser.add_argument(
        "--consumer-log",
        help="Consumer log file path (required for file source).",
    )
    parser.add_argument(
        "--verbose",
        "-v",
        action="store_true",
        help="Enable verbose (DEBUG) logging.",
    )

    return parser


def validate_args(args: argparse.Namespace) -> Optional[str]:
    """Validate argument combinations.

    Args:
        args: Parsed arguments.

    Returns:
        Error message string, or None if valid.
    """
    if args.source == "loki":
        if args.start is None or args.end is None:
            return "--start and --end are required when --source is 'loki'."
    elif args.source == "file":
        if args.producer_log is None or args.consumer_log is None:
            return (
                "--producer-log and --consumer-log are required "
                "when --source is 'file'."
            )

    # switch-start and switch-end must both be provided or both omitted
    if (args.switch_start is None) != (args.switch_end is None):
        return "--switch-start and --switch-end must both be provided or both omitted."

    if args.switch_start and args.switch_end:
        if args.switch_start >= args.switch_end:
            return "--switch-start must be before --switch-end."

    if args.start and args.end:
        if args.start >= args.end:
            return "--start must be before --end."

    return None


def collect_from_loki(args: argparse.Namespace):
    """Collect sequences from Loki.

    Args:
        args: Parsed CLI arguments.

    Returns:
        Tuple of (produced_records, consumed_records).

    Raises:
        SystemExit: On Loki connection failure.
    """
    logger.info("Collecting sequences from Loki at %s", args.loki_url)
    try:
        with LokiClient(base_url=args.loki_url) as client:
            produced = client.get_producer_sequences(args.start, args.end)
            consumed = client.get_consumer_sequences(args.start, args.end)
    except ConnectionError as exc:
        logger.error("Failed to connect to Loki: %s", exc)
        print(f"ERROR: {exc}", file=sys.stderr)
        sys.exit(1)
    except RuntimeError as exc:
        logger.error("Loki query error: %s", exc)
        print(f"ERROR: {exc}", file=sys.stderr)
        sys.exit(1)

    return produced, consumed


def collect_from_file(args: argparse.Namespace):
    """Collect sequences from local log files.

    Args:
        args: Parsed CLI arguments.

    Returns:
        Tuple of (produced_records, consumed_records).

    Raises:
        SystemExit: On file read failure.
    """
    logger.info(
        "Reading sequences from files: producer=%s, consumer=%s",
        args.producer_log,
        args.consumer_log,
    )
    reader = FileReader()
    try:
        produced = reader.read_producer_sequences(args.producer_log)
        consumed = reader.read_consumer_sequences(args.consumer_log)
    except FileNotFoundError as exc:
        logger.error("File not found: %s", exc)
        print(f"ERROR: {exc}", file=sys.stderr)
        sys.exit(1)
    except PermissionError as exc:
        logger.error("Permission denied: %s", exc)
        print(f"ERROR: {exc}", file=sys.stderr)
        sys.exit(1)

    return produced, consumed


def main() -> None:
    """Main entry point: parse args, collect, analyze, report."""
    parser = build_parser()
    args = parser.parse_args()

    # Configure logging
    log_level = logging.DEBUG if args.verbose else logging.INFO
    logging.basicConfig(
        level=log_level,
        format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
        datefmt="%Y-%m-%dT%H:%M:%S",
    )

    # Validate arguments
    error = validate_args(args)
    if error:
        parser.error(error)

    # Collect sequences
    if args.source == "loki":
        produced, consumed = collect_from_loki(args)
    else:
        produced, consumed = collect_from_file(args)

    if not produced:
        print("WARNING: No producer records found.", file=sys.stderr)
    if not consumed:
        print("WARNING: No consumer records found.", file=sys.stderr)

    # Analyze
    analyzer = SequenceAnalyzer()

    has_phases = args.switch_start is not None and args.switch_end is not None

    if has_phases:
        analysis = analyzer.analyze_by_phase(
            produced, consumed, args.switch_start, args.switch_end
        )
    else:
        analysis = {
            "overall": analyzer.analyze(produced, consumed),
        }

    # Generate reports
    reporter = ReportGenerator()

    # Format time strings for display
    start_str = args.start.isoformat() if args.start else "N/A"
    end_str = args.end.isoformat() if args.end else "N/A"
    switch_start_str = args.switch_start.isoformat() if args.switch_start else None
    switch_end_str = args.switch_end.isoformat() if args.switch_end else None

    # Always print text report to stdout
    text_report = reporter.generate_text_report(
        analysis=analysis,
        strategy=args.strategy,
        start=start_str,
        end=end_str,
        switch_start=switch_start_str,
        switch_end=switch_end_str,
    )
    print(text_report)

    # Optionally write Markdown report to file
    if args.output:
        md_report = reporter.generate_markdown_report(
            analysis=analysis,
            strategy=args.strategy,
            start=start_str,
            end=end_str,
            switch_start=switch_start_str,
            switch_end=switch_end_str,
        )
        reporter.write_report(md_report, args.output)
        print(f"Markdown report written to: {args.output}")


if __name__ == "__main__":
    main()
