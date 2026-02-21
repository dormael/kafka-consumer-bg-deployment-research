"""Sequence analysis engine for detecting message loss and duplication.

Compares producer-published sequences against consumer-received sequences
to identify missing messages (loss), duplicated messages, and unexpected
extra messages. Supports phase-based analysis to pinpoint when issues
occurred relative to the blue/green switch window.
"""

import logging
from collections import Counter
from datetime import datetime
from typing import Any, Dict, List, Optional, Union

from loki_client import ConsumerRecord, ProducerRecord

logger = logging.getLogger(__name__)

# Type alias for analysis result dictionaries.
AnalysisResult = Dict[str, Any]


class SequenceAnalyzer:
    """Analyzes producer and consumer sequences to detect loss and duplication.

    Uses set operations for O(1) lookups and Counter for efficient duplicate
    detection, suitable for datasets with tens of thousands of records.
    """

    def analyze(
        self,
        produced: List[ProducerRecord],
        consumed: List[ConsumerRecord],
    ) -> AnalysisResult:
        """Compare produced and consumed sequences to detect anomalies.

        Args:
            produced: List of producer records.
            consumed: List of consumer records.

        Returns:
            Dictionary containing:
                - total_produced: Count of produced messages.
                - total_consumed: Count of consumed messages (including duplicates).
                - unique_consumed: Count of unique consumed sequence numbers.
                - missing_count: Number of produced-but-not-consumed sequences.
                - missing_sequences: Sorted list of missing sequence numbers.
                - duplicate_count: Total number of extra duplicate deliveries.
                - duplicate_sequences: List of dicts with seq, count, partitions, offsets.
                - extra_count: Number of consumed-but-not-produced sequences.
                - extra_sequences: Sorted list of extra sequence numbers.
                - loss_rate: Percentage of messages lost (0.0 - 100.0).
                - duplication_rate: Percentage of duplicate deliveries (0.0 - 100.0).
        """
        produced_seqs: set = {r.seq_number for r in produced}
        consumed_seq_list: list = [r.seq_number for r in consumed]
        consumed_seqs: set = set(consumed_seq_list)

        # Missing: produced but never consumed.
        missing = produced_seqs - consumed_seqs
        # Extra: consumed but never produced.
        extra = consumed_seqs - produced_seqs

        # Duplicates: consumed more than once.
        seq_counter = Counter(consumed_seq_list)
        duplicate_details = []
        total_extra_deliveries = 0

        for seq_num, count in sorted(seq_counter.items()):
            if count > 1:
                # Gather partition and offset info for each occurrence.
                occurrences = [r for r in consumed if r.seq_number == seq_num]
                partitions = [r.partition for r in occurrences]
                offsets = [r.offset for r in occurrences]
                group_ids = list({r.group_id for r in occurrences})

                duplicate_details.append({
                    "seq": seq_num,
                    "count": count,
                    "partitions": partitions,
                    "offsets": offsets,
                    "group_ids": group_ids,
                })
                total_extra_deliveries += count - 1

        total_produced = len(produced_seqs)
        total_consumed = len(consumed_seq_list)

        loss_rate = (
            (len(missing) / total_produced * 100.0) if total_produced > 0 else 0.0
        )
        duplication_rate = (
            (total_extra_deliveries / total_consumed * 100.0)
            if total_consumed > 0
            else 0.0
        )

        result: AnalysisResult = {
            "total_produced": total_produced,
            "total_consumed": total_consumed,
            "unique_consumed": len(consumed_seqs),
            "missing_count": len(missing),
            "missing_sequences": sorted(missing),
            "duplicate_count": total_extra_deliveries,
            "duplicate_sequences": duplicate_details,
            "extra_count": len(extra),
            "extra_sequences": sorted(extra),
            "loss_rate": loss_rate,
            "duplication_rate": duplication_rate,
        }

        logger.info(
            "Analysis: produced=%d, consumed=%d, missing=%d, duplicates=%d, extra=%d",
            total_produced,
            total_consumed,
            len(missing),
            total_extra_deliveries,
            len(extra),
        )

        return result

    def _filter_by_time(
        self,
        records: Union[List[ProducerRecord], List[ConsumerRecord]],
        start: Optional[datetime],
        end: Optional[datetime],
    ) -> list:
        """Filter records to those within a time range (inclusive).

        Args:
            records: List of records to filter.
            start: Inclusive start time, or None for no lower bound.
            end: Inclusive end time, or None for no upper bound.

        Returns:
            Filtered list of records.
        """
        filtered = records
        if start is not None:
            filtered = [r for r in filtered if r.timestamp >= start]
        if end is not None:
            filtered = [r for r in filtered if r.timestamp <= end]
        return filtered

    def analyze_by_phase(
        self,
        produced: List[ProducerRecord],
        consumed: List[ConsumerRecord],
        switch_start: datetime,
        switch_end: datetime,
    ) -> Dict[str, AnalysisResult]:
        """Perform phase-based analysis: pre-switch, during-switch, post-switch.

        Splits records into three phases based on their timestamps relative
        to the switch window, then runs analyze() on each phase independently.

        Args:
            produced: Complete list of producer records.
            consumed: Complete list of consumer records.
            switch_start: Start of the blue/green switch window.
            switch_end: End of the blue/green switch window.

        Returns:
            Dictionary with keys:
                - pre_switch: Analysis for records before switch_start.
                - during_switch: Analysis for records within [switch_start, switch_end].
                - post_switch: Analysis for records after switch_end.
                - overall: Analysis for all records combined.
        """
        # Pre-switch: timestamp < switch_start
        pre_produced = [r for r in produced if r.timestamp < switch_start]
        pre_consumed = [r for r in consumed if r.timestamp < switch_start]

        # During switch: switch_start <= timestamp <= switch_end
        during_produced = self._filter_by_time(produced, switch_start, switch_end)
        during_consumed = self._filter_by_time(consumed, switch_start, switch_end)

        # Post-switch: timestamp > switch_end
        post_produced = [r for r in produced if r.timestamp > switch_end]
        post_consumed = [r for r in consumed if r.timestamp > switch_end]

        result = {
            "pre_switch": self.analyze(pre_produced, pre_consumed),
            "during_switch": self.analyze(during_produced, during_consumed),
            "post_switch": self.analyze(post_produced, post_consumed),
            "overall": self.analyze(produced, consumed),
        }

        logger.info(
            "Phase analysis complete: pre=%d/%d, during=%d/%d, post=%d/%d records",
            len(pre_produced),
            len(pre_consumed),
            len(during_produced),
            len(during_consumed),
            len(post_produced),
            len(post_consumed),
        )

        return result
