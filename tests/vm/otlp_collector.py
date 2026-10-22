"""Bounded OTLP wire sink for the disposable VM test; protobuf fields are tested in Go."""
import hashlib
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
from pathlib import Path
import re
import time


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        self.connection.settimeout(2)
        size = int(self.headers.get("Content-Length", "0"))
        if self.path != "/v1/metrics" or not 0 < size <= 1048576:
            self.send_error(400)
            return
        data = self.rfile.read(size)
        node = re.search(rb"n_[0-9a-f]{32}", data)
        if node is None or b"host.id" not in data or b"system.memory.usage" not in data:
            self.send_error(422)
            return
        name = node.group().decode("ascii")
        Path("/run/nixmox-test-collector", name + ".json").write_text(json.dumps({
            "host_id": name, "observed_at": time.time(), "bytes": len(data),
            "sha256": hashlib.sha256(data).hexdigest(),
        }))
        self.send_response(200)
        self.send_header("Content-Type", "application/x-protobuf")
        self.send_header("Content-Length", "0")
        self.end_headers()


HTTPServer(("0.0.0.0", 63210), Handler).serve_forever()
