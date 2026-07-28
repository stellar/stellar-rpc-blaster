#!/usr/bin/env python3
"""Fixture tests for harvest.py. No network, no docker, no tx-load-test."""

import json
import os
import shutil
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import harvest  # noqa: E402

# --------------------------------------------------------------------------
# Fixtures
# --------------------------------------------------------------------------

# Six sendTransaction pairs, two getTransaction pairs that must be excluded, one
# finished line with no starting line, and one line supervisord wrote as plain
# text. Timestamps run 12:00:00 to 12:00:09; the window tests cut at 12:00:04.
LOG_LINES = [
    '{"time":"2026-07-27T12:00:00.100Z","level":"info","msg":"starting JSONRPC request",'
    '"subsys":"jsonrpc","req":"1","json_req":1,"method":"sendTransaction"}',
    '{"time":"2026-07-27T12:00:00.900Z","level":"info","msg":"finished JSONRPC request",'
    '"subsys":"jsonrpc","req":"1","json_req":"1","duration":"523.4µs","status":"ok"}',

    '{"time":"2026-07-27T12:00:01.100Z","level":"info","msg":"starting JSONRPC request",'
    '"subsys":"jsonrpc","req":"2","json_req":2,"method":"getTransaction"}',
    '{"time":"2026-07-27T12:00:01.900Z","level":"info","msg":"finished JSONRPC request",'
    '"subsys":"jsonrpc","req":"2","json_req":"2","duration":"12ms","status":"ok"}',

    '{"time":"2026-07-27T12:00:02.100Z","level":"info","msg":"starting JSONRPC request",'
    '"subsys":"jsonrpc","req":"3","json_req":3,"method":"sendTransaction"}',
    '{"time":"2026-07-27T12:00:02.900Z","level":"info","msg":"finished JSONRPC request",'
    '"subsys":"jsonrpc","req":"3","json_req":"3","duration":"1.5ms","status":"ok"}',

    "2026-07-27 12:00:03,000 INFO reaped unknown pid 412 (exit status 0)",

    '{"time":"2026-07-27T12:00:03.100Z","level":"info","msg":"starting JSONRPC request",'
    '"subsys":"jsonrpc","req":"4","json_req":4,"method":"sendTransaction"}',
    '{"time":"2026-07-27T12:00:03.900Z","level":"info","msg":"finished JSONRPC request",'
    '"subsys":"jsonrpc","req":"4","json_req":"4","duration":"2.5ms","status":"ok"}',

    '{"time":"2026-07-27T12:00:04.900Z","level":"info","msg":"finished JSONRPC request",'
    '"subsys":"jsonrpc","req":"999","json_req":"999","duration":"7ms","status":"ok"}',

    '{"time":"2026-07-27T12:00:05.100Z","level":"info","msg":"starting JSONRPC request",'
    '"subsys":"jsonrpc","req":"5","json_req":5,"method":"sendTransaction"}',
    '{"time":"2026-07-27T12:00:05.900Z","level":"info","msg":"finished JSONRPC request",'
    '"subsys":"jsonrpc","req":"5","json_req":"5","duration":"3.5ms","status":"ok"}',

    '{"time":"2026-07-27T12:00:06.100Z","level":"info","msg":"starting JSONRPC request",'
    '"subsys":"jsonrpc","req":"6","json_req":6,"method":"getTransaction"}',
    '{"time":"2026-07-27T12:00:06.900Z","level":"info","msg":"finished JSONRPC request",'
    '"subsys":"jsonrpc","req":"6","json_req":"6","duration":"9ms","status":"ok"}',

    '{"time":"2026-07-27T12:00:07.100Z","level":"info","msg":"starting JSONRPC request",'
    '"subsys":"jsonrpc","req":"7","json_req":7,"method":"sendTransaction"}',
    '{"time":"2026-07-27T12:00:07.900Z","level":"info","msg":"finished JSONRPC request",'
    '"subsys":"jsonrpc","req":"7","json_req":"7","duration":"4.5ms","status":"ok"}',

    '{"time":"2026-07-27T12:00:08.100Z","level":"info","msg":"starting JSONRPC request",'
    '"subsys":"jsonrpc","req":"8","json_req":8,"method":"sendTransaction"}',
    '{"time":"2026-07-27T12:00:08.900Z","level":"info","msg":"finished JSONRPC request",'
    '"subsys":"jsonrpc","req":"8","json_req":"8","duration":"1.5s","status":"error"}',
]
LOG_TEXT = "\n".join(LOG_LINES) + "\n"

# The six sendTransaction durations above, in nanoseconds.
ALL_DURATIONS_NS = [523400, 1500000, 2500000, 3500000, 4500000, 1500000000]

METRICS_TEXT = """\
# HELP soroban_rpc_txsub_submission_duration_seconds submission durations to Stellar-Core, sliding window = 10m
# TYPE soroban_rpc_txsub_submission_duration_seconds summary
soroban_rpc_txsub_submission_duration_seconds{status="PENDING",quantile="0.5"} 0.001
soroban_rpc_txsub_submission_duration_seconds{status="PENDING",quantile="0.9"} 0.002
soroban_rpc_txsub_submission_duration_seconds{status="PENDING",quantile="0.99"} 0.003
soroban_rpc_txsub_submission_duration_seconds_sum{status="PENDING"} 0.0055
soroban_rpc_txsub_submission_duration_seconds_count{status="PENDING"} 5
soroban_rpc_txsub_submission_duration_seconds{status="ERROR",quantile="0.5"} 0.004
soroban_rpc_txsub_submission_duration_seconds{status="ERROR",quantile="0.9"} 0.004
soroban_rpc_txsub_submission_duration_seconds{status="ERROR",quantile="0.99"} NaN
soroban_rpc_txsub_submission_duration_seconds_sum{status="ERROR"} 0.004
soroban_rpc_txsub_submission_duration_seconds_count{status="ERROR"} 1
# HELP soroban_rpc_json_rpc_request_duration_seconds JSON RPC request duration
# TYPE soroban_rpc_json_rpc_request_duration_seconds summary
soroban_rpc_json_rpc_request_duration_seconds{endpoint="sendTransaction",status="ok",quantile="0.5"} 0.0025
soroban_rpc_json_rpc_request_duration_seconds{endpoint="sendTransaction",status="ok",quantile="0.9"} 0.0045
soroban_rpc_json_rpc_request_duration_seconds{endpoint="sendTransaction",status="ok",quantile="0.99"} 0.0045
soroban_rpc_json_rpc_request_duration_seconds_sum{endpoint="sendTransaction",status="ok"} 0.0125234
soroban_rpc_json_rpc_request_duration_seconds_count{endpoint="sendTransaction",status="ok"} 5
soroban_rpc_json_rpc_request_duration_seconds{endpoint="sendTransaction",status="error",quantile="0.5"} 1.5
soroban_rpc_json_rpc_request_duration_seconds{endpoint="sendTransaction",status="error",quantile="0.9"} 1.5
soroban_rpc_json_rpc_request_duration_seconds{endpoint="sendTransaction",status="error",quantile="0.99"} 1.5
soroban_rpc_json_rpc_request_duration_seconds_sum{endpoint="sendTransaction",status="error"} 1.5
soroban_rpc_json_rpc_request_duration_seconds_count{endpoint="sendTransaction",status="error"} 1
soroban_rpc_json_rpc_request_duration_seconds{endpoint="getTransaction",status="ok",quantile="0.5"} 0.010
soroban_rpc_json_rpc_request_duration_seconds{endpoint="getTransaction",status="ok",quantile="0.9"} 0.011
soroban_rpc_json_rpc_request_duration_seconds{endpoint="getTransaction",status="ok",quantile="0.99"} 0.011
soroban_rpc_json_rpc_request_duration_seconds_sum{endpoint="getTransaction",status="ok"} 0.021
soroban_rpc_json_rpc_request_duration_seconds_count{endpoint="getTransaction",status="ok"} 2
"""


class TempBundle(unittest.TestCase):
    """Writes the fixtures to a temp dir and cleans up after itself."""

    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="harvest-test-")
        self.log_path = os.path.join(self.tmp, "rpc-log-sac-transfer.log")
        self.metrics_path = os.path.join(self.tmp, "metrics-sac-transfer.prom")
        self.out_path = os.path.join(self.tmp, "summary-sac-transfer.json")
        with open(self.log_path, "w") as handle:
            handle.write(LOG_TEXT)
        with open(self.metrics_path, "w") as handle:
            handle.write(METRICS_TEXT)

    def tearDown(self):
        shutil.rmtree(self.tmp, ignore_errors=True)

    def run_main(self, extra=None):
        argv = [
            "--log", self.log_path,
            "--metrics", self.metrics_path,
            "--mode", "sac-transfer",
            "--out", self.out_path,
        ]
        argv.extend(extra or [])
        self.assertEqual(harvest.main(argv), 0)
        with open(self.out_path) as handle:
            return json.load(handle)


class TestGoDuration(unittest.TestCase):
    def test_single_units(self):
        cases = {
            "150ns": 150,
            "523.4µs": 523400,      # MICRO SIGN, what Go emits
            "523.4μs": 523400,      # GREEK SMALL LETTER MU
            "523.4us": 523400,
            "1.23ms": 1230000,
            "2.5s": 2500000000,
            "0s": 0,
            "0": 0,
        }
        for text, expected in cases.items():
            self.assertEqual(harvest.parse_go_duration_ns(text), expected, text)

    def test_composite_units(self):
        self.assertEqual(harvest.parse_go_duration_ns("1m2.5s"), 62500000000)
        self.assertEqual(harvest.parse_go_duration_ns("1h2m3.5s"), 3723500000000)
        self.assertEqual(harvest.parse_go_duration_ns("2m0s"), 120000000000)

    def test_ms_is_not_minutes(self):
        self.assertEqual(harvest.parse_go_duration_ns("1ms"), 1000000)
        self.assertEqual(harvest.parse_go_duration_ns("1m"), 60000000000)

    def test_sign_and_fraction_precision(self):
        self.assertEqual(harvest.parse_go_duration_ns("-1.5ms"), -1500000)
        self.assertEqual(harvest.parse_go_duration_ns("3.456789123s"), 3456789123)

    def test_rejects_garbage(self):
        for text in ("", "abc", "12", "1.5x", None):
            self.assertRaises(ValueError, harvest.parse_go_duration_ns, text)


class TestNearestRank(unittest.TestCase):
    def test_hand_computed_ranks(self):
        samples = list(range(1, 101))  # 1..100
        # rank = ceil(p x n): 50, 90, 99 for n=100.
        self.assertEqual(harvest.nearest_rank(samples, "0.5"), 50)
        self.assertEqual(harvest.nearest_rank(samples, "0.9"), 90)
        self.assertEqual(harvest.nearest_rank(samples, "0.99"), 99)

    def test_small_sample_rounds_up(self):
        samples = [10, 20, 30, 40, 50, 60]  # n=6
        # ceil(0.5*6)=3, ceil(0.9*6)=6, ceil(0.99*6)=6
        self.assertEqual(harvest.nearest_rank(samples, "0.5"), 30)
        self.assertEqual(harvest.nearest_rank(samples, "0.9"), 60)
        self.assertEqual(harvest.nearest_rank(samples, "0.99"), 60)

    def test_single_sample(self):
        self.assertEqual(harvest.nearest_rank([7], "0.99"), 7)

    def test_empty(self):
        self.assertIsNone(harvest.nearest_rank([], "0.5"))

    def test_distribution_over_fixture_durations(self):
        stats = harvest.distribution(ALL_DURATIONS_NS)
        self.assertEqual(stats["min_ns"], 523400)
        self.assertEqual(stats["max_ns"], 1500000000)
        # sorted: 523400, 1500000, 2500000, 3500000, 4500000, 1500000000
        self.assertEqual(stats["p50_ns"], 2500000)
        self.assertEqual(stats["p90_ns"], 1500000000)
        self.assertEqual(stats["p99_ns"], 1500000000)
        self.assertEqual(stats["mean_ns"], int(round(sum(ALL_DURATIONS_NS) / 6.0)))


class TestRFC3339(unittest.TestCase):
    def test_zulu_and_offset_agree(self):
        zulu = harvest.parse_rfc3339("2026-07-27T12:00:00.100Z")
        offset = harvest.parse_rfc3339("2026-07-27T14:00:00.100+02:00")
        self.assertEqual(zulu, offset)

    def test_naive_is_utc(self):
        self.assertEqual(
            harvest.parse_rfc3339("2026-07-27T12:00:00"),
            harvest.parse_rfc3339("2026-07-27T12:00:00Z"),
        )

    def test_rejects_garbage(self):
        self.assertRaises(ValueError, harvest.parse_rfc3339, "yesterday")


class TestLogJoin(TempBundle):
    def test_join_keeps_only_send_transaction(self):
        samples, stats = harvest.parse_log(self.log_path, None, None)
        self.assertEqual([duration for duration, _ in samples], ALL_DURATIONS_NS)
        self.assertEqual(stats["send_transaction_pairs"], 6)
        self.assertEqual(stats["other_method_pairs"], 2)

    def test_unjoined_and_non_json_are_counted(self):
        _, stats = harvest.parse_log(self.log_path, None, None)
        self.assertEqual(stats["unjoined_finished_lines"], 1)
        self.assertEqual(stats["non_json_lines"], 1)
        self.assertEqual(stats["starting_lines"], 8)
        self.assertEqual(stats["finished_lines"], 9)

    def test_status_is_carried_from_the_finished_line(self):
        samples, _ = harvest.parse_log(self.log_path, None, None)
        self.assertEqual([status for _, status in samples][-1], "error")

    def test_req_reuse_across_a_restart_pairs_with_the_latest_start(self):
        # supervisord keeps one log file across `supervisorctl restart` and RPC's
        # request counter resets, so req "1" appears twice.
        path = os.path.join(self.tmp, "restart.log")
        with open(path, "w") as handle:
            handle.write("\n".join([
                '{"time":"2026-07-27T12:00:00.000Z","msg":"starting JSONRPC request",'
                '"req":"1","method":"getTransaction"}',
                '{"time":"2026-07-27T12:00:00.100Z","msg":"finished JSONRPC request",'
                '"req":"1","duration":"9ms","status":"ok"}',
                '{"time":"2026-07-27T12:05:00.000Z","msg":"starting JSONRPC request",'
                '"req":"1","method":"sendTransaction"}',
                '{"time":"2026-07-27T12:05:00.100Z","msg":"finished JSONRPC request",'
                '"req":"1","duration":"4ms","status":"ok"}',
            ]) + "\n")
        samples, stats = harvest.parse_log(path, None, None)
        self.assertEqual([duration for duration, _ in samples], [4000000])
        self.assertEqual(stats["unjoined_finished_lines"], 0)

    def test_window_filters_on_the_finished_timestamp(self):
        samples, stats = harvest.parse_log(
            self.log_path,
            harvest.parse_rfc3339("2026-07-27T12:00:04Z"),
            harvest.parse_rfc3339("2026-07-27T12:00:08Z"),
        )
        # Keeps only req 5 (12:00:05.900) and req 7 (12:00:07.900).
        self.assertEqual([duration for duration, _ in samples], [3500000, 4500000])
        self.assertEqual(stats["excluded_by_window"], 4)


class TestPrometheus(unittest.TestCase):
    def setUp(self):
        self.samples = []
        tmp = tempfile.mkdtemp(prefix="harvest-prom-")
        self.tmp = tmp
        self.path = os.path.join(tmp, "metrics.prom")
        with open(self.path, "w") as handle:
            handle.write(METRICS_TEXT)
        self.samples = harvest.parse_prometheus(self.path)

    def tearDown(self):
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_core_leg_family(self):
        warnings = []
        family = harvest.extract_summary(
            self.samples, harvest.CORE_LEG_METRIC, {}, warnings
        )
        self.assertTrue(family["present"])
        self.assertEqual(sorted(family["by_status"]), ["ERROR", "PENDING"])
        pending = family["by_status"]["PENDING"]
        self.assertEqual(pending["p50_ns"], 1000000)
        self.assertEqual(pending["p90_ns"], 2000000)
        self.assertEqual(pending["p99_ns"], 3000000)
        self.assertEqual(pending["sum_ns"], 5500000)
        self.assertEqual(pending["count"], 5)
        self.assertEqual(pending["mean_ns"], 1100000)
        self.assertEqual(family["total_count"], 6)
        self.assertEqual(family["total_sum_ns"], 9500000)

    def test_nan_quantile_becomes_null_and_warns(self):
        warnings = []
        family = harvest.extract_summary(
            self.samples, harvest.CORE_LEG_METRIC, {}, warnings
        )
        self.assertIsNone(family["by_status"]["ERROR"]["p99_ns"])
        self.assertTrue(any("NaN" in warning for warning in warnings))

    def test_json_rpc_family_is_filtered_by_endpoint(self):
        warnings = []
        family = harvest.extract_summary(
            self.samples,
            harvest.JSON_RPC_METRIC,
            {"endpoint": "sendTransaction"},
            warnings,
        )
        self.assertEqual(sorted(family["by_status"]), ["error", "ok"])
        self.assertEqual(family["total_count"], 6)
        self.assertEqual(family["by_status"]["ok"]["p50_ns"], 2500000)

    def test_absent_family_warns(self):
        warnings = []
        family = harvest.extract_summary(self.samples, "no_such_metric", {}, warnings)
        self.assertFalse(family["present"])
        self.assertEqual(family["total_count"], 0)
        self.assertEqual(len(warnings), 1)


class TestEndToEnd(TempBundle):
    def test_summary_shape_and_values(self):
        summary = self.run_main(["--expected-count", "6"])

        self.assertEqual(
            list(summary.keys()),
            [
                "schema_version",
                "generated_at",
                "run",
                "log_parse",
                "handler",
                "core_leg",
                "json_rpc_cross_check",
                "mean_residue",
                "warnings",
            ],
        )
        self.assertEqual(summary["run"]["mode"], "sac-transfer")
        self.assertEqual(summary["run"]["expected_count"], 6)

        handler = summary["handler"]
        self.assertEqual(handler["count"], 6)
        self.assertEqual(handler["by_status"], {"ok": 5, "error": 1})
        self.assertEqual(handler["p50_ns"], 2500000)

        residue = summary["mean_residue"]
        self.assertEqual(
            residue["residue_mean_ns"],
            residue["handler_mean_ns"] - residue["core_leg_mean_ns"],
        )
        # No quantile is ever subtracted.
        self.assertNotIn("residue_p99_ns", json.dumps(summary))

    def test_warnings_cover_every_anomaly(self):
        summary = self.run_main(["--expected-count", "99"])
        joined = " | ".join(summary["warnings"])
        self.assertIn("non-JSON", joined)
        self.assertIn("no matching starting line", joined)
        self.assertIn("expected count 99", joined)
        self.assertIn("handler status 'error'", joined)
        self.assertIn("NaN", joined)
        self.assertIn("core answered 'ERROR'", joined)

    def test_count_matching_the_metric_raises_no_count_warning(self):
        summary = self.run_main(["--expected-count", "6"])
        joined = " | ".join(summary["warnings"])
        self.assertNotIn("does not match the expected count", joined)
        self.assertNotIn("_count", joined)

    def test_window_narrows_the_sample_set(self):
        summary = self.run_main([
            "--window-start", "2026-07-27T12:00:04Z",
            "--window-end", "2026-07-27T12:00:08Z",
        ])
        self.assertEqual(summary["handler"]["count"], 2)
        self.assertEqual(summary["handler"]["min_ns"], 3500000)
        self.assertEqual(summary["handler"]["max_ns"], 4500000)
        self.assertIn(
            "does not match",
            " | ".join(summary["warnings"]),
        )

    def test_empty_window_is_fatal(self):
        self.assertRaises(
            harvest.Fatal,
            harvest.main,
            [
                "--log", self.log_path,
                "--metrics", self.metrics_path,
                "--mode", "sac-transfer",
                "--window-start", "2027-01-01T00:00:00Z",
                "--out", self.out_path,
            ],
        )

    def test_text_format_log_is_fatal(self):
        path = os.path.join(self.tmp, "text.log")
        with open(path, "w") as handle:
            handle.write(
                'time="2026-07-27T12:00:00.100Z" level=info '
                'msg="starting JSONRPC request" method=sendTransaction\n'
                'time="2026-07-27T12:00:00.900Z" level=info '
                'msg="finished JSONRPC request" duration=523.4µs\n'
            )
        with self.assertRaises(harvest.Fatal) as caught:
            harvest.main([
                "--log", path,
                "--metrics", self.metrics_path,
                "--mode", "sac-transfer",
                "--out", self.out_path,
            ])
        self.assertIn("LOG_FORMAT", str(caught.exception))

    def test_missing_log_is_fatal(self):
        self.assertRaises(
            harvest.Fatal,
            harvest.main,
            [
                "--log", os.path.join(self.tmp, "absent.log"),
                "--metrics", self.metrics_path,
                "--mode", "sac-transfer",
                "--out", self.out_path,
            ],
        )


if __name__ == "__main__":
    unittest.main()
