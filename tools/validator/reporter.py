"""Report generation for validation results.

Produces human-readable text reports for terminal output and Markdown
reports for inclusion in the project's report/ directory. Includes
PASS/FAIL verdict logic based on configurable tolerance thresholds.
"""

import logging
from datetime import datetime
from pathlib import Path
from typing import Any, Dict, Optional

logger = logging.getLogger(__name__)

# Type alias matching analyzer.AnalysisResult
AnalysisResult = Dict[str, Any]

# Strategy display names
STRATEGY_NAMES = {
    "B": "B (Separate Consumer Group + Offset Sync)",
    "C": "C (Pause/Resume Atomic Switch)",
    "E": "E (Kafka Connect REST API / Strimzi CRD)",
}

# Verdict thresholds
DUPLICATION_TOLERANCE_PERCENT = 0.1


def _verdict(analysis: AnalysisResult) -> str:
    """Determine PASS/CONDITIONAL PASS/FAIL verdict.

    Args:
        analysis: Overall analysis result dict.

    Returns:
        Verdict string with explanation.
    """
    missing = analysis["missing_count"]
    dup_rate = analysis["duplication_rate"]

    if missing > 0:
        return "FAIL (message loss detected)"
    if dup_rate > DUPLICATION_TOLERANCE_PERCENT:
        return f"CONDITIONAL PASS (loss=0, duplicates={dup_rate:.3f}% > {DUPLICATION_TOLERANCE_PERCENT}% tolerance)"
    return "PASS (loss=0, duplicates within tolerance)"


def _fmt_num(n: int) -> str:
    """Format an integer with thousands separators."""
    return f"{n:,}"


class ReportGenerator:
    """Generates text and Markdown reports from validation analysis results."""

    def generate_text_report(
        self,
        analysis: Dict[str, AnalysisResult],
        strategy: str,
        start: str,
        end: str,
        switch_start: Optional[str] = None,
        switch_end: Optional[str] = None,
    ) -> str:
        """Generate a formatted plain-text report for terminal output.

        Args:
            analysis: Phase analysis dict with 'overall', 'pre_switch',
                      'during_switch', 'post_switch' keys.
            strategy: Strategy identifier (B, C, or E).
            start: Test start time (ISO 8601 string).
            end: Test end time (ISO 8601 string).
            switch_start: Switch start time (ISO 8601 string), or None.
            switch_end: Switch end time (ISO 8601 string), or None.

        Returns:
            Formatted text report string.
        """
        overall = analysis["overall"]
        strategy_name = STRATEGY_NAMES.get(strategy, strategy)
        verdict = _verdict(overall)

        lines = [
            "=== Blue/Green Switch Validation Report ===",
            f"Period: {start} ~ {end}",
            f"Strategy: {strategy_name}",
            "",
        ]

        if switch_start and switch_end:
            lines.append(f"Switch Window: {switch_start} ~ {switch_end}")
            lines.append("")

        # Summary section
        lines.extend([
            "--- Summary ---",
            f"Total Produced:     {_fmt_num(overall['total_produced'])}",
            f"Total Consumed:     {_fmt_num(overall['total_consumed'])}",
            f"Missing (Loss):     {_fmt_num(overall['missing_count'])} ({overall['loss_rate']:.3f}%)",
            f"Duplicates:         {_fmt_num(overall['duplicate_count'])} ({overall['duplication_rate']:.3f}%)",
            f"Extra:              {_fmt_num(overall['extra_count'])}",
            "",
        ])

        # Phase analysis section
        if switch_start and switch_end:
            lines.append("--- Phase Analysis ---")
            for phase_key, phase_label in [
                ("pre_switch", "Pre-Switch"),
                ("during_switch", "During Switch"),
                ("post_switch", "Post-Switch"),
            ]:
                phase = analysis.get(phase_key)
                if phase is None:
                    continue
                lines.append(
                    f"{phase_label:15s} "
                    f"Produced={_fmt_num(phase['total_produced']):>7s}  "
                    f"Consumed={_fmt_num(phase['total_consumed']):>7s}  "
                    f"Loss={_fmt_num(phase['missing_count'])}  "
                    f"Dup={_fmt_num(phase['duplicate_count'])}"
                )
            lines.append("")

        # Duplicate details
        if overall["duplicate_sequences"]:
            lines.append("--- Duplicate Details ---")
            for dup in overall["duplicate_sequences"][:50]:  # Cap at 50 for readability
                lines.append(
                    f"Seq#{dup['seq']}: consumed {dup['count']} times "
                    f"(partitions: {dup['partitions']}, offsets: {dup['offsets']})"
                )
            if len(overall["duplicate_sequences"]) > 50:
                lines.append(
                    f"... and {len(overall['duplicate_sequences']) - 50} more"
                )
            lines.append("")

        # Missing details
        if overall["missing_sequences"]:
            lines.append("--- Missing Sequence Details ---")
            missing_to_show = overall["missing_sequences"][:100]
            lines.append(f"Sequences: {missing_to_show}")
            if len(overall["missing_sequences"]) > 100:
                lines.append(
                    f"... and {len(overall['missing_sequences']) - 100} more"
                )
            lines.append("")

        # Verdict
        lines.append(f"--- RESULT: {verdict} ---")
        lines.append("")

        return "\n".join(lines)

    def generate_markdown_report(
        self,
        analysis: Dict[str, AnalysisResult],
        strategy: str,
        start: str,
        end: str,
        switch_start: Optional[str] = None,
        switch_end: Optional[str] = None,
    ) -> str:
        """Generate a Markdown-formatted report for file output.

        Args:
            analysis: Phase analysis dict with 'overall', 'pre_switch',
                      'during_switch', 'post_switch' keys.
            strategy: Strategy identifier (B, C, or E).
            start: Test start time (ISO 8601 string).
            end: Test end time (ISO 8601 string).
            switch_start: Switch start time (ISO 8601 string), or None.
            switch_end: Switch end time (ISO 8601 string), or None.

        Returns:
            Markdown report string.
        """
        overall = analysis["overall"]
        strategy_name = STRATEGY_NAMES.get(strategy, strategy)
        verdict = _verdict(overall)
        now = datetime.utcnow().strftime("%Y-%m-%dT%H:%M:%SZ")

        lines = [
            f"# Validation Report: Strategy {strategy_name}",
            "",
            f"> Generated: {now}",
            "",
            "## Test Parameters",
            "",
            "| Parameter | Value |",
            "|-----------|-------|",
            f"| Strategy | {strategy_name} |",
            f"| Test Period | {start} ~ {end} |",
        ]

        if switch_start and switch_end:
            lines.append(f"| Switch Window | {switch_start} ~ {switch_end} |")

        lines.extend([
            "",
            "## Summary",
            "",
            "| Metric | Value |",
            "|--------|-------|",
            f"| Total Produced | {_fmt_num(overall['total_produced'])} |",
            f"| Total Consumed | {_fmt_num(overall['total_consumed'])} |",
            f"| Unique Consumed | {_fmt_num(overall['unique_consumed'])} |",
            f"| Missing (Loss) | {_fmt_num(overall['missing_count'])} ({overall['loss_rate']:.3f}%) |",
            f"| Duplicates | {_fmt_num(overall['duplicate_count'])} ({overall['duplication_rate']:.3f}%) |",
            f"| Extra | {_fmt_num(overall['extra_count'])} |",
            "",
            f"**Verdict: {verdict}**",
            "",
        ])

        # Phase analysis
        if switch_start and switch_end:
            lines.extend([
                "## Phase Analysis",
                "",
                "| Phase | Produced | Consumed | Loss | Duplicates | Loss Rate | Dup Rate |",
                "|-------|----------|----------|------|------------|-----------|----------|",
            ])

            for phase_key, phase_label in [
                ("pre_switch", "Pre-Switch"),
                ("during_switch", "During Switch"),
                ("post_switch", "Post-Switch"),
            ]:
                phase = analysis.get(phase_key)
                if phase is None:
                    continue
                lines.append(
                    f"| {phase_label} "
                    f"| {_fmt_num(phase['total_produced'])} "
                    f"| {_fmt_num(phase['total_consumed'])} "
                    f"| {_fmt_num(phase['missing_count'])} "
                    f"| {_fmt_num(phase['duplicate_count'])} "
                    f"| {phase['loss_rate']:.3f}% "
                    f"| {phase['duplication_rate']:.3f}% |"
                )

            lines.append("")

        # Duplicate details
        if overall["duplicate_sequences"]:
            lines.extend([
                "## Duplicate Details",
                "",
                "| Seq # | Count | Partitions | Offsets | Consumer Groups |",
                "|-------|-------|------------|---------|-----------------|",
            ])

            for dup in overall["duplicate_sequences"][:100]:
                lines.append(
                    f"| {dup['seq']} "
                    f"| {dup['count']} "
                    f"| {dup['partitions']} "
                    f"| {dup['offsets']} "
                    f"| {', '.join(dup['group_ids'])} |"
                )

            if len(overall["duplicate_sequences"]) > 100:
                lines.append(
                    f"| ... | {len(overall['duplicate_sequences']) - 100} more entries | | | |"
                )

            lines.append("")

        # Missing details
        if overall["missing_sequences"]:
            lines.extend([
                "## Missing Sequence Details",
                "",
                f"Total missing: {_fmt_num(overall['missing_count'])}",
                "",
            ])

            missing_to_show = overall["missing_sequences"][:200]
            # Format as comma-separated list wrapped in a code block
            lines.append("```")
            chunk_size = 20
            for i in range(0, len(missing_to_show), chunk_size):
                chunk = missing_to_show[i : i + chunk_size]
                lines.append(", ".join(str(s) for s in chunk))
            lines.append("```")

            if len(overall["missing_sequences"]) > 200:
                lines.append(
                    f"\n... and {len(overall['missing_sequences']) - 200} more"
                )

            lines.append("")

        # Extra sequence details
        if overall["extra_sequences"]:
            lines.extend([
                "## Extra Sequence Details",
                "",
                f"Total extra: {_fmt_num(overall['extra_count'])}",
                "",
            ])

            extra_to_show = overall["extra_sequences"][:200]
            lines.append("```")
            chunk_size = 20
            for i in range(0, len(extra_to_show), chunk_size):
                chunk = extra_to_show[i : i + chunk_size]
                lines.append(", ".join(str(s) for s in chunk))
            lines.append("```")

            if len(overall["extra_sequences"]) > 200:
                lines.append(
                    f"\n... and {len(overall['extra_sequences']) - 200} more"
                )

            lines.append("")

        return "\n".join(lines)

    def write_report(self, content: str, filepath: str) -> None:
        """Write report content to a file.

        Creates parent directories if they do not exist.

        Args:
            content: Report content string.
            filepath: Destination file path.

        Raises:
            OSError: If the file cannot be written.
        """
        path = Path(filepath)
        path.parent.mkdir(parents=True, exist_ok=True)

        with open(path, "w", encoding="utf-8") as fh:
            fh.write(content)

        logger.info("Report written to %s", filepath)
