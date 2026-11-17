import contextlib
import http.server
import json
from pathlib import Path
import subprocess
import threading
import time
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "check-health.sh"
HEALTHY = b'{"status":"ok","database":"ok"}'


@contextlib.contextmanager
def health_server(responses, delay=0):
    class Handler(http.server.BaseHTTPRequestHandler):
        requests = 0

        def do_GET(self):
            index = min(Handler.requests, len(responses) - 1)
            Handler.requests += 1
            status, body = responses[index]
            time.sleep(delay)
            self.send_response(status)
            self.end_headers()
            try:
                self.wfile.write(body)
            except BrokenPipeError:
                pass  # The timeout test deliberately closes the connection.

        def log_message(self, *args):
            pass

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield f"http://127.0.0.1:{server.server_port}/healthz", Handler
    finally:
        server.shutdown()
        server.server_close()
        thread.join()


def check(url, *args):
    return subprocess.run(
        ["bash", str(SCRIPT), url, *args],
        capture_output=True,
        text=True,
        timeout=100,
    )


class HealthCheckTests(unittest.TestCase):
    def test_default_window_outlasts_the_previous_six_second_check(self):
        with health_server([(503, b"Unavailable")] * 3 + [(200, HEALTHY)]) as (url, handler):
            start = time.monotonic()
            result = check(url)
            elapsed = time.monotonic() - start
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertGreaterEqual(elapsed, 8)
            self.assertEqual(handler.requests, 4)
            self.assertIn("Application and PostgreSQL are healthy", result.stdout)

    def test_already_healthy_exits_immediately_over_ipv4(self):
        with health_server([(200, HEALTHY)]) as (url, handler):
            start = time.monotonic()
            result = check(url, "4")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertLess(time.monotonic() - start, 3)
            self.assertEqual(handler.requests, 1)

    def test_permanent_http_error_fails_at_the_deadline(self):
        with health_server([(503, b"Unavailable")]) as (url, _):
            start = time.monotonic()
            result = check(url, "auto", "2")
            self.assertNotEqual(result.returncode, 0)
            self.assertLess(time.monotonic() - start, 3)
            self.assertIn("waiting up to 2 seconds", result.stderr)

    def test_unhealthy_or_invalid_json_never_counts_as_success(self):
        for body in [b"not JSON", b"[]", json.dumps({"status": "ok", "database": "unreachable"}).encode()]:
            with self.subTest(body=body), health_server([(200, body)]) as (url, _):
                result = check(url, "auto", "1")
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("are healthy", result.stdout)

    def test_slow_response_cannot_extend_the_deadline(self):
        with health_server([(200, HEALTHY)], delay=4) as (url, _):
            start = time.monotonic()
            result = check(url, "auto", "2")
            self.assertNotEqual(result.returncode, 0)
            self.assertLess(time.monotonic() - start, 3)


if __name__ == "__main__":
    unittest.main()
