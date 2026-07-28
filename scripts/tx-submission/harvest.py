#!/usr/bin/env python3
"""Harvest sendTransaction latency for one benchmark mode.

Inputs are the two server-side instruments of a single stellar-rpc process:

  * the RPC log file, in logrus JSON format (LOG_FORMAT="json"), which carries one
    "starting JSONRPC request" line and one "finished JSONRPC request" line per
    request. Only the starting line carries `method`; only the finished line
    carries `duration` and `status`. The two are joined on `req`.
  * a Prometheus text-format scrape of the admin endpoint, taken right after the
    run, holding `soroban_rpc_txsub_submission_duration_seconds` (the RPC->Core
    leg) and `soroban_rpc_json_rpc_request_duration_seconds` (the full handler,
    used here only as a cross-check of the log-derived numbers).

Output is one summary JSON per mode. Every duration is an integer count of
nanoseconds and every such key ends in `_ns`.

See .claude/docs/869-tx-submission-plan.md for the measurement method.
"""

import argparse
import json
import re
import sys
from datetime import datetime, timedelta, timezone
from decimal import ROUND_CEILING, ROUND_HALF_UP, Decimal

SCHEMA_VERSION = 1

TARGET_METHOD = "sendTransaction"
START_MSG = "starting JSONRPC request"
FINISH_MSG = "finished JSONRPC request"

CORE_LEG_METRIC = "soroban_rpc_txsub_submission_duration_seconds"
JSON_RPC_METRIC = "soroban_rpc_json_rpc_request_duration_seconds"

# The quantile objectives both Summaries are registered with.
QUANTILE_KEYS = {"0.5": "p50_ns", "0.9": "p90_ns", "0.99": "p99_ns"}

# The handler status label for a successful request.
OK_HANDLER_STATUS = "ok"
# Core's answer to a freshly submitted transaction. Anything else means the run
# is degraded: DUPLICATE, TRY_AGAIN_LATER, ERROR, request_error, exception.
OK_CORE_STATUS = "PENDING"


class Fatal(Exception):
    """Input the script cannot work with. Reported on stderr, non-zero exit."""


# --------------------------------------------------------------------------
# Go duration strings
# --------------------------------------------------------------------------

_NS = Decimal(1)
_DURATION_UNITS_NS = {
    "ns": _NS,
    "us": _NS * 1000,
    "µs": _NS * 1000,  # MICRO SIGN, what Go emits
    "μs": _NS * 1000,  # GREEK SMALL LETTER MU, accepted for tolerance
    "ms": _NS * 1000000,
    "s": _NS * 1000000000,
    "m": _NS * 60 * 1000000000,
    "h": _NS * 3600 * 1000000000,
}

# Longer unit spellings first: "ms" must win over "m", "ns"/"us" over "s".
_DURATION_TOKEN = re.compile(
    r"(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|h|m|s)"
)


def parse_go_duration_ns(text):
    """Convert a Go time.Duration.String() value to integer nanoseconds.

    Handles the single-unit forms Go emits below one second (`150ns`, `523.4us`,
    `1.23ms`), the composite forms it emits above one second (`2.5s`, `1m2.5s`,
    `1h2m3.5s`), and a leading sign.
    """
    if text is None:
        raise ValueError("duration is absent")
    body = str(text).strip()
    if not body:
        raise ValueError("duration is empty")

    sign = 1
    if body[0] in "+-":
        if body[0] == "-":
            sign = -1
        body = body[1:]

    if body == "0":
        return 0

    total = Decimal(0)
    pos = 0
    while pos < len(body):
        match = _DURATION_TOKEN.match(body, pos)
        if match is None:
            raise ValueError("cannot parse Go duration %r" % (text,))
        total += Decimal(match.group(1)) * _DURATION_UNITS_NS[match.group(2)]
        pos = match.end()

    return sign * int(total.to_integral_value(rounding=ROUND_HALF_UP))


# --------------------------------------------------------------------------
# Timestamps
# --------------------------------------------------------------------------

_RFC3339 = re.compile(
    r"^(\d{4})-(\d{2})-(\d{2})[Tt ](\d{2}):(\d{2}):(\d{2})(\.\d+)?"
    r"(?:([Zz])|([+-])(\d{2}):?(\d{2}))?$"
)


def parse_rfc3339(text):
    """Parse an RFC3339 timestamp into an aware datetime. Naive input is UTC.

    Written out rather than delegated to datetime.fromisoformat, which rejects
    the trailing "Z" before Python 3.11 and accepts only 3 or 6 fractional
    digits. stellar-rpc logs 3 digits and a "Z" or a numeric offset.
    """
    match = _RFC3339.match(str(text).strip())
    if match is None:
        raise ValueError("cannot parse RFC3339 timestamp %r" % (text,))

    year, month, day, hour, minute, second = (int(match.group(i)) for i in range(1, 7))

    microsecond = 0
    fraction = match.group(7)
    if fraction:
        microsecond = int((fraction[1:] + "000000")[:6])

    if match.group(9):
        offset = timedelta(hours=int(match.group(10)), minutes=int(match.group(11)))
        if match.group(9) == "-":
            offset = -offset
        tzinfo = timezone(offset)
    else:
        tzinfo = timezone.utc

    return datetime(year, month, day, hour, minute, second, microsecond, tzinfo)


# --------------------------------------------------------------------------
# Statistics
# --------------------------------------------------------------------------


def nearest_rank(sorted_samples, percentile):
    """Nearest-rank percentile: the value at 1-indexed rank ceil(p x n).

    `percentile` is a decimal string so the rank is exact; 0.99 x 100 in binary
    floating point is 99.00000000000001, which would round the rank up to 100.
    """
    count = len(sorted_samples)
    if count == 0:
        return None
    rank = int((Decimal(percentile) * count).to_integral_value(rounding=ROUND_CEILING))
    rank = max(1, min(rank, count))
    return sorted_samples[rank - 1]


def distribution(samples_ns):
    """min / mean / p50 / p90 / p99 / max over a list of nanosecond durations."""
    if not samples_ns:
        return {
            "min_ns": None,
            "mean_ns": None,
            "p50_ns": None,
            "p90_ns": None,
            "p99_ns": None,
            "max_ns": None,
        }
    ordered = sorted(samples_ns)
    return {
        "min_ns": ordered[0],
        "mean_ns": int(round(float(sum(ordered)) / len(ordered))),
        "p50_ns": nearest_rank(ordered, "0.5"),
        "p90_ns": nearest_rank(ordered, "0.9"),
        "p99_ns": nearest_rank(ordered, "0.99"),
        "max_ns": ordered[-1],
    }


# --------------------------------------------------------------------------
# RPC log
# --------------------------------------------------------------------------


def parse_log(path, window_start, window_end):
    """Join starting/finished lines on `req` and keep the sendTransaction pairs.

    A starting line is consumed by the first finished line that quotes its `req`.
    supervisord keeps one stdout file across `supervisorctl restart`, and RPC's
    request counter restarts with the process, so `req` values repeat inside a
    single file. Consuming on match pairs each finished line with the most recent
    matching starting line, which is the correct pairing in that case.
    """
    stats = {
        "non_empty_lines": 0,
        "non_json_lines": 0,
        "starting_lines": 0,
        "finished_lines": 0,
        "unjoined_finished_lines": 0,
        "other_method_pairs": 0,
        "send_transaction_pairs": 0,
        "excluded_by_window": 0,
        "unparsable_durations": 0,
        "unparsable_timestamps": 0,
    }
    pending = {}
    samples = []

    try:
        handle = open(path, "r", encoding="utf-8", errors="replace")
    except IOError as err:
        raise Fatal("cannot read log file %s: %s" % (path, err))

    with handle:
        for raw in handle:
            line = raw.strip()
            if not line:
                continue
            stats["non_empty_lines"] += 1

            try:
                record = json.loads(line)
            except ValueError:
                stats["non_json_lines"] += 1
                continue
            if not isinstance(record, dict):
                stats["non_json_lines"] += 1
                continue

            message = record.get("msg")
            if message == START_MSG:
                stats["starting_lines"] += 1
                req = record.get("req")
                if req is not None:
                    pending[str(req)] = record.get("method")
                continue
            if message != FINISH_MSG:
                continue

            stats["finished_lines"] += 1
            req = record.get("req")
            key = None if req is None else str(req)
            if key is None or key not in pending:
                stats["unjoined_finished_lines"] += 1
                continue

            method = pending.pop(key)
            if method != TARGET_METHOD:
                stats["other_method_pairs"] += 1
                continue
            stats["send_transaction_pairs"] += 1

            timestamp = None
            if record.get("time") is not None:
                try:
                    timestamp = parse_rfc3339(record["time"])
                except ValueError:
                    stats["unparsable_timestamps"] += 1

            if window_start is not None or window_end is not None:
                if timestamp is None:
                    stats["excluded_by_window"] += 1
                    continue
                if window_start is not None and timestamp < window_start:
                    stats["excluded_by_window"] += 1
                    continue
                if window_end is not None and timestamp > window_end:
                    stats["excluded_by_window"] += 1
                    continue

            try:
                duration_ns = parse_go_duration_ns(record.get("duration"))
            except ValueError:
                stats["unparsable_durations"] += 1
                continue

            samples.append((duration_ns, record.get("status")))

    return samples, stats


# --------------------------------------------------------------------------
# Prometheus scrape
# --------------------------------------------------------------------------

_SAMPLE_LINE = re.compile(
    r"^(?P<name>[a-zA-Z_:][a-zA-Z0-9_:]*)"
    r"(?:\{(?P<labels>.*)\})?"
    r"[ \t]+(?P<value>[^ \t]+)"
    r"(?:[ \t]+[0-9]+)?[ \t]*$"
)
_LABEL_PAIR = re.compile(r'([a-zA-Z_][a-zA-Z0-9_]*)="((?:[^"\\]|\\.)*)"')
_LABEL_ESCAPE = re.compile(r"\\(.)")
_ESCAPES = {"n": "\n", '"': '"', "\\": "\\"}


def _unescape_label(value):
    return _LABEL_ESCAPE.sub(lambda m: _ESCAPES.get(m.group(1), m.group(1)), value)


def parse_metric_value(token):
    """Prometheus sample value. NaN becomes None; +Inf/-Inf stay as floats."""
    if token in ("NaN", "nan", "+NaN", "-NaN"):
        return None
    if token in ("+Inf", "Inf", "inf", "+inf"):
        return float("inf")
    if token in ("-Inf", "-inf"):
        return float("-inf")
    return float(token)


def parse_prometheus(path):
    """Read a Prometheus text-format scrape into (name, labels, value) triples."""
    try:
        handle = open(path, "r", encoding="utf-8", errors="replace")
    except IOError as err:
        raise Fatal("cannot read metrics file %s: %s" % (path, err))

    samples = []
    with handle:
        for raw in handle:
            line = raw.strip()
            if not line or line.startswith("#"):
                continue
            match = _SAMPLE_LINE.match(line)
            if match is None:
                continue
            labels = {}
            if match.group("labels"):
                for name, value in _LABEL_PAIR.findall(match.group("labels")):
                    labels[name] = _unescape_label(value)
            samples.append((match.group("name"), labels, match.group("value")))
    return samples


def _seconds_to_ns(value):
    if value is None:
        return None
    return int(round(value * 1e9))


def extract_summary(samples, base_name, label_filter, warnings):
    """Pull one Prometheus Summary family out of a scrape, grouped by `status`.

    Returns quantiles, `_sum` and `_count` per status label, plus the totals over
    all statuses. `label_filter` restricts the family to a subset of its label
    space (the json_rpc family is per endpoint).
    """
    by_status = {}
    for name, labels, raw_value in samples:
        if name == base_name:
            kind = "quantile"
        elif name == base_name + "_sum":
            kind = "sum"
        elif name == base_name + "_count":
            kind = "count"
        else:
            continue
        if any(labels.get(key) != value for key, value in label_filter.items()):
            continue

        status = labels.get("status", "")
        group = by_status.setdefault(
            status,
            {
                "p50_ns": None,
                "p90_ns": None,
                "p99_ns": None,
                "sum_ns": None,
                "count": None,
                "mean_ns": None,
            },
        )
        value = parse_metric_value(raw_value)

        if kind == "quantile":
            field = QUANTILE_KEYS.get(labels.get("quantile", ""))
            if field is None:
                continue
            if value is None:
                warnings.append(
                    "%s{status=%s} quantile %s is NaN - no observations in the "
                    "sliding window" % (base_name, status, labels.get("quantile"))
                )
            group[field] = _seconds_to_ns(value)
        elif kind == "sum":
            group["sum_ns"] = _seconds_to_ns(value)
        else:
            group["count"] = None if value is None else int(value)

    total_count = 0
    total_sum_ns = 0
    for group in by_status.values():
        if group["count"]:
            total_count += group["count"]
        if group["sum_ns"]:
            total_sum_ns += group["sum_ns"]
        if group["count"] and group["sum_ns"] is not None:
            group["mean_ns"] = int(round(float(group["sum_ns"]) / group["count"]))

    result = {
        "metric": base_name,
        "label_filter": dict(label_filter),
        "present": bool(by_status),
        "total_count": total_count,
        "total_sum_ns": total_sum_ns if by_status else None,
        "mean_ns": (
            int(round(float(total_sum_ns) / total_count)) if total_count else None
        ),
        "by_status": by_status,
    }
    if not by_status:
        warnings.append(
            "%s%s is absent from the scrape"
            % (
                base_name,
                "" if not label_filter else " with %s" % (label_filter,),
            )
        )
    return result


# --------------------------------------------------------------------------
# Summary assembly
# --------------------------------------------------------------------------


def build_summary(args, samples, log_stats, metric_samples):
    warnings = []

    if log_stats["non_json_lines"]:
        warnings.append(
            "skipped %d non-JSON line(s) in the log - supervisord process noise"
            % log_stats["non_json_lines"]
        )
    if log_stats["unjoined_finished_lines"]:
        warnings.append(
            "%d finished JSONRPC request line(s) had no matching starting line "
            "and were dropped - the log probably begins mid-request"
            % log_stats["unjoined_finished_lines"]
        )
    if log_stats["unparsable_durations"]:
        warnings.append(
            "%d finished line(s) had an unparsable duration and were dropped"
            % log_stats["unparsable_durations"]
        )
    if log_stats["unparsable_timestamps"]:
        warnings.append(
            "%d finished line(s) had an unparsable timestamp"
            % log_stats["unparsable_timestamps"]
        )

    durations = [duration for duration, _ in samples]
    status_counts = {}
    for _, status in samples:
        key = "unknown" if status is None else str(status)
        status_counts[key] = status_counts.get(key, 0) + 1

    for status in sorted(status_counts):
        if status != OK_HANDLER_STATUS:
            warnings.append(
                "%d sendTransaction request(s) finished with handler status %r"
                % (status_counts[status], status)
            )

    handler = {
        "source": "%r log lines joined to %r on req" % (FINISH_MSG, START_MSG),
        "count": len(durations),
        "by_status": status_counts,
    }
    handler.update(distribution(durations))

    core_leg = extract_summary(metric_samples, CORE_LEG_METRIC, {}, warnings)
    json_rpc = extract_summary(
        metric_samples,
        JSON_RPC_METRIC,
        {"endpoint": TARGET_METHOD},
        warnings,
    )

    for status in sorted(core_leg["by_status"]):
        if status != OK_CORE_STATUS:
            warnings.append(
                "core answered %r on %s submission(s) - expected only %r"
                % (
                    status,
                    core_leg["by_status"][status]["count"],
                    OK_CORE_STATUS,
                )
            )
    for status in sorted(json_rpc["by_status"]):
        if status != OK_HANDLER_STATUS:
            warnings.append(
                "%s{endpoint=%s} reports status %r"
                % (JSON_RPC_METRIC, TARGET_METHOD, status)
            )

    if args.expected_count is not None and len(durations) != args.expected_count:
        warnings.append(
            "log-derived sendTransaction count %d does not match the expected "
            "count %d from the client metrics"
            % (len(durations), args.expected_count)
        )
    if core_leg["present"] and core_leg["total_count"] != len(durations):
        warnings.append(
            "log-derived sendTransaction count %d does not match %s _count %d"
            % (len(durations), CORE_LEG_METRIC, core_leg["total_count"])
        )
    if json_rpc["present"] and json_rpc["total_count"] != len(durations):
        warnings.append(
            "log-derived sendTransaction count %d does not match %s _count %d "
            "for endpoint=%s"
            % (
                len(durations),
                JSON_RPC_METRIC,
                json_rpc["total_count"],
                TARGET_METHOD,
            )
        )

    handler_mean = handler["mean_ns"]
    core_mean = core_leg["mean_ns"]
    residue = None
    if handler_mean is not None and core_mean is not None:
        residue = handler_mean - core_mean
        if residue < 0:
            warnings.append(
                "mean residue is negative (%d ns) - the log window and the "
                "metric window do not cover the same requests" % residue
            )

    return {
        "schema_version": SCHEMA_VERSION,
        "generated_at": datetime.now(timezone.utc)
        .replace(microsecond=0)
        .isoformat()
        .replace("+00:00", "Z"),
        "run": {
            "mode": args.mode,
            "window_start": args.window_start,
            "window_end": args.window_end,
            "expected_count": args.expected_count,
            "log_file": args.log,
            "metrics_file": args.metrics,
        },
        "log_parse": log_stats,
        "handler": handler,
        "core_leg": core_leg,
        "json_rpc_cross_check": json_rpc,
        "mean_residue": {
            "handler_mean_ns": handler_mean,
            "core_leg_mean_ns": core_mean,
            "residue_mean_ns": residue,
            "note": "means only - quantiles never subtract",
        },
        "warnings": warnings,
    }


def parse_args(argv):
    parser = argparse.ArgumentParser(
        prog="harvest.py",
        description=(
            "Summarise in-RPC sendTransaction latency from a stellar-rpc JSON log "
            "and a Prometheus scrape."
        ),
    )
    parser.add_argument("--log", required=True, help="RPC log file (logrus JSON lines)")
    parser.add_argument(
        "--metrics", required=True, help="Prometheus text-format scrape file"
    )
    parser.add_argument(
        "--mode",
        required=True,
        help="benchmark mode: sac-transfer, oz-transfer or soroswap",
    )
    parser.add_argument("--window-start", help="RFC3339 lower bound, inclusive")
    parser.add_argument("--window-end", help="RFC3339 upper bound, inclusive")
    parser.add_argument(
        "--expected-count",
        type=int,
        help="submission_submitted from the client NDJSON; mismatches are warned about",
    )
    parser.add_argument("--out", required=True, help="summary JSON to write")
    return parser.parse_args(argv)


def main(argv=None):
    args = parse_args(sys.argv[1:] if argv is None else argv)

    window_start = None
    window_end = None
    try:
        if args.window_start:
            window_start = parse_rfc3339(args.window_start)
        if args.window_end:
            window_end = parse_rfc3339(args.window_end)
    except ValueError as err:
        raise Fatal(str(err))

    samples, log_stats = parse_log(args.log, window_start, window_end)

    if log_stats["non_empty_lines"] and (
        log_stats["non_json_lines"] * 2 > log_stats["non_empty_lines"]
    ):
        raise Fatal(
            "%d of %d non-empty lines in %s are not JSON - LOG_FORMAT=\"json\" is "
            "probably not set on the RPC process (see the plan, section 5)"
            % (log_stats["non_json_lines"], log_stats["non_empty_lines"], args.log)
        )
    if not samples:
        raise Fatal(
            "no sendTransaction request pairs found in %s%s"
            % (
                args.log,
                " inside the given window" if (window_start or window_end) else "",
            )
        )

    metric_samples = parse_prometheus(args.metrics)
    summary = build_summary(args, samples, log_stats, metric_samples)

    try:
        with open(args.out, "w") as handle:
            json.dump(summary, handle, indent=2)
            handle.write("\n")
    except IOError as err:
        raise Fatal("cannot write %s: %s" % (args.out, err))

    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Fatal as error:
        sys.stderr.write("harvest.py: %s\n" % (error,))
        sys.exit(2)
