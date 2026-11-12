# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Exercise the real Helm namespace lookup against a local fake Kubernetes API."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import threading
import unittest
from urllib.parse import urlsplit


class NamespaceUpgradeTest(unittest.TestCase):
    def test_namespace_ownership(self):
        owned = {
            "meta.helm.sh/release-name": "telemetry",
            "meta.helm.sh/release-namespace": "telemetry-system",
        }
        scenarios = [
            ("owned namespace cannot be dropped", owned, False, True, 200, "Refusing namespace.create=false"),
            ("owned namespace can be retained", owned, True, True, 200, None),
            ("external namespace", {}, False, True, 200, None),
            ("another release", {**owned, "meta.helm.sh/release-name": "other"}, False, True, 200, None),
            ("another release namespace", {**owned, "meta.helm.sh/release-namespace": "other"}, False, True, 200, None),
            ("missing namespace", {}, False, True, 404, None),
            ("lookup denied stops upgrade", {}, False, True, 403, "forbidden"),
            ("install does not check ownership", owned, False, False, 403, None),
        ]
        for name, annotations, create, upgrade, status, error in scenarios:
            with self.subTest(name=name):
                self.check_render(annotations, create, upgrade, status, error)

    def check_render(self, annotations, create, upgrade, status, error):
        requests = []

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def do_GET(self):
                path = urlsplit(self.path).path
                requests.append(path)
                response_status = 200
                if path == "/version":
                    body = {"major": "1", "minor": "34", "gitVersion": "v1.34.0"}
                elif path == "/api":
                    body = {"kind": "APIVersions", "apiVersion": "v1", "versions": ["v1"]}
                elif path == "/apis":
                    body = {"kind": "APIGroupList", "apiVersion": "v1", "groups": []}
                elif path == "/api/v1":
                    body = {
                        "kind": "APIResourceList", "apiVersion": "v1", "groupVersion": "v1",
                        "resources": [{"name": "namespaces", "kind": "Namespace", "namespaced": False, "verbs": ["get"]}],
                    }
                elif path == "/api/v1/namespaces/telemetry-system":
                    response_status = status
                    body = {
                        "apiVersion": "v1", "kind": "Namespace",
                        "metadata": {"name": "telemetry-system", "annotations": annotations},
                    }
                else:
                    response_status = 404
                if response_status != 200:
                    body = {
                        "apiVersion": "v1", "kind": "Status", "status": "Failure",
                        "reason": "Forbidden" if response_status == 403 else "NotFound",
                        "message": "forbidden" if response_status == 403 else "not found",
                        "code": response_status,
                    }
                self.send_response(response_status)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps(body).encode())

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            with tempfile.TemporaryDirectory(prefix="nktp-namespace-upgrade-") as scratch:
                chart = Path(scratch)
                (chart / "templates").mkdir()
                (chart / "Chart.yaml").write_text("apiVersion: v2\nname: namespace-test\nversion: 0.1.0\n")
                shutil.copyfile(Path(__file__).resolve().parents[1] / "templates/namespace.yaml", chart / "templates/namespace.yaml")
                config = chart / "kubeconfig"
                config.write_text(json.dumps({
                    "apiVersion": "v1", "kind": "Config", "current-context": "test",
                    "clusters": [{"name": "test", "cluster": {"server": f"http://127.0.0.1:{server.server_port}"}}],
                    "contexts": [{"name": "test", "context": {"cluster": "test", "user": "test"}}],
                    "users": [{"name": "test", "user": {}}],
                }))
                config.chmod(0o600)
                command = [
                    "helm", "template", "telemetry", str(chart), "--namespace", "telemetry-system",
                    "--kubeconfig", str(config), "--dry-run=server", "--disable-openapi-validation",
                    "--set", f"namespace.create={str(create).lower()}",
                ]
                if upgrade:
                    command.append("--is-upgrade")
                result = subprocess.run(command, capture_output=True, text=True, timeout=20)
                self.assertEqual("/api/v1/namespaces/telemetry-system" in requests, upgrade and not create, requests)
                if error:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn(error, result.stderr)
                else:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual("kind: Namespace" in result.stdout, create)
        finally:
            server.shutdown()
            server.server_close()
            thread.join()


if __name__ == "__main__":
    unittest.main()
