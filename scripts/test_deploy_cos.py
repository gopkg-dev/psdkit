"""Deployment contract checks; no Tencent credentials or network access needed."""

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


spec = importlib.util.spec_from_file_location("deploy_cos", Path(__file__).with_name("deploy-cos.py"))
deploy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(deploy)


class RecordingCOS:
    def __init__(self, fail_key=None):
        self.objects = []
        self.fail_key = fail_key

    def put_object(self, **request):
        if request["Key"] == self.fail_key:
            raise RuntimeError("simulated upload failure")
        self.objects.append(dict(request, Body=request["Body"].read()))


class DeploymentContractTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.site = Path(self.temp.name)
        for key in deploy.REQUIRED_FILES:
            path = self.site / key
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(b"public build")

    def test_upload_contract_and_completion_record(self):
        compressed = self.site / "demo/samples/example.psd.gz"
        compressed.write_bytes(b"\x1f\x8braw-gzip-bytes")
        files = deploy.public_files(self.site)
        record = deploy.deployment_record(files, "a" * 40)
        client = RecordingCOS()
        deploy.publish(client, "test-123", files, record)
        keys = [item["Key"] for item in client.objects]
        self.assertEqual(keys[-1], "deployment.json")
        first_html = next(index for index, key in enumerate(keys) if key.endswith(".html"))
        self.assertTrue(all(key.endswith(".html") for key in keys[first_html:-1]))
        uploaded = next(item for item in client.objects if item["Key"].endswith(".psd.gz"))
        self.assertEqual(uploaded["Body"], compressed.read_bytes())
        self.assertEqual(uploaded["ContentType"], "application/gzip")
        self.assertNotIn("ContentEncoding", uploaded)
        self.assertEqual(json.loads(client.objects[-1]["Body"])["revision"], "a" * 40)
        self.assertEqual(record["files"], len(client.objects) - 1)

    def test_failed_upload_does_not_publish_completion_record(self):
        files = deploy.public_files(self.site)
        client = RecordingCOS(fail_key="demo/wasm/psdkit.wasm")
        with self.assertRaises(RuntimeError):
            deploy.publish(client, "test-123", files, deploy.deployment_record(files, "a" * 40))
        self.assertNotIn("deployment.json", [item["Key"] for item in client.objects])

    def test_fixed_runtime_paths_revalidate_and_hashed_assets_are_immutable(self):
        for key in ("demo/wasm/psdkit.wasm", "demo/wasm/worker.js", "index.html", "demo/samples/manifest.json"):
            self.assertEqual(deploy.object_headers(key)["CacheControl"], "no-cache")
        self.assertEqual(deploy.object_headers("demo/wasm/psdkit.wasm")["ContentType"], "application/wasm")
        self.assertIn("immutable", deploy.object_headers("demo/assets/index-B0k1cdef.js")["CacheControl"])

    def test_private_files_symlinks_and_incomplete_build_fail_before_upload(self):
        for name in (".env.production", "private.key"):
            path = self.site / name
            path.write_text("not public")
            with self.assertRaises(ValueError):
                deploy.public_files(self.site)
            path.unlink()
        link = self.site / "linked.js"
        link.symlink_to(self.site / "index.html")
        with self.assertRaises(ValueError):
            deploy.public_files(self.site)
        link.unlink()
        (self.site / "demo/wasm/psdkit.wasm").unlink()
        with self.assertRaises(ValueError):
            deploy.public_files(self.site)


if __name__ == "__main__":
    unittest.main()
