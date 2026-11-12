"""Offline launcher process tests: no Docker, GitLab or model requests."""
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import tempfile
import time
import unittest


SCRIPT = Path(__file__).with_name('run-local-pilot.py')
FAKE_SERVER = '''
import http.server, json, os, pathlib, sys
if '--version' in sys.argv:
    print('v22.23.4'); sys.exit(0)
frontend = '--port' in sys.argv
state = pathlib.Path(__file__).parent.parent
name = 'frontend' if frontend else 'api'
(state / (name + '.json')).write_text(json.dumps({'pid':os.getpid(), 'env':dict(os.environ)}))
if frontend and (state / 'fail-frontend').exists(): sys.exit(2)
port = int(sys.argv[sys.argv.index('--port')+1]) if frontend else int(os.environ['AGENT_PLATFORM_API_ADDR'].split(':')[-1])
class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200); self.end_headers(); self.wfile.write(b'ok')
    def log_message(self, *args): pass
http.server.HTTPServer(('127.0.0.1',port), Handler).serve_forever()
'''


class LocalPilotTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='pilot-launcher-test-')
        self.addCleanup(self.temp.cleanup)
        self.state = Path(self.temp.name)
        (self.state / 'bin').mkdir()
        (self.state / 'auth').mkdir()
        self.write_json('auth/api-auth.json', {})
        self.write_json('gitlab.json', {'token': 'test-gitlab-secret', 'scopes': ['read_api', 'read_repository'], 'expiresAt': '2099-01-01'})
        self.write_json('model.json', {'token': 'test-model-secret'})
        self.executable('bin/node', FAKE_SERVER)
        self.executable('bin/go', "import pathlib,sys\np=pathlib.Path(sys.argv[sys.argv.index('-o')+1]); p.write_text(" + repr('#!' + sys.executable + '\n' + FAKE_SERVER) + "); p.chmod(0o700)\n")
        with socket.socket() as first, socket.socket() as second:
            first.bind(('127.0.0.1', 0)); second.bind(('127.0.0.1', 0))
            ports = [first.getsockname()[1], second.getsockname()[1]]
        self.config = {
            'stateDir': str(self.state), 'nodeBinary': str(self.state / 'bin/node'), 'goBinary': str(self.state / 'bin/go'),
            'apiPort': ports[0], 'frontendPort': ports[1], 'gitlabCredentialFile': str(self.state / 'gitlab.json'),
            'gitlabURL': 'https://localhost:8929', 'gitlabCAFile': str(self.state / 'ca.pem'),
            'dockerHost': 'unix:///test/docker.sock', 'workerImage': 'sha256:' + 'a'*64,
            'model': 'test-model', 'modelURL': 'https://model.invalid', 'servicePrincipal': 'pilot-test',
            'modelCredentialFile': str(self.state / 'model.json'),
        }
        self.write_json('pilot.json', self.config)

    def executable(self, name, body):
        path = self.state / name
        path.write_text('#!' + sys.executable + '\n' + body)
        path.chmod(0o700)

    def write_json(self, name, value):
        path = self.state / name
        path.write_text(json.dumps(value))
        path.chmod(0o600)

    def launch(self, *args):
        env = dict(os.environ, VITE_SECRET='parent-secret', AGENT_PLATFORM_GITLAB_TOKEN='parent-gitlab', OPENAI_API_KEY='parent-model')
        process = subprocess.Popen([sys.executable, str(SCRIPT), '--config', str(self.state / 'pilot.json'), *args], env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        def cleanup():
            if process.poll() is None:
                process.send_signal(signal.SIGTERM)
            process.communicate(timeout=10)
        self.addCleanup(cleanup)
        return process

    def wait_for(self, name, process):
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            path = self.state / name
            if path.exists() and path.stat().st_size:
                return json.loads(path.read_text())
            if process.poll() is not None:
                self.fail(process.communicate()[0])
            time.sleep(0.05)
        self.fail('child failed to start')

    def assert_child_gone(self, record):
        with self.assertRaises(ProcessLookupError):
            os.kill(record['pid'], 0)

    def test_startup_environment_isolation_and_graceful_shutdown(self):
        process = self.launch()
        api = self.wait_for('api.json', process)
        frontend = self.wait_for('frontend.json', process)
        self.assertEqual(api['env']['AGENT_PLATFORM_GITLAB_TOKEN'], 'test-gitlab-secret')
        self.assertEqual(api['env']['AGENT_PLATFORM_API_ADDR'], f"127.0.0.1:{self.config['apiPort']}")
        self.assertEqual(frontend['env']['AGENT_PLATFORM_DEV_API_URL'], f"http://127.0.0.1:{self.config['apiPort']}")
        self.assertNotIn('AGENT_PLATFORM_GITLAB_TOKEN', frontend['env'])
        self.assertNotIn('AGENT_PLATFORM_MODEL_CREDENTIAL_FILE', frontend['env'])
        for record in (api, frontend):
            self.assertNotIn('OPENAI_API_KEY', record['env'])
            self.assertNotIn('VITE_SECRET', record['env'])
            self.assertNotIn('test-model-secret', json.dumps(record))
        process.send_signal(signal.SIGTERM)
        output = process.communicate(timeout=10)[0]
        self.assertEqual(process.returncode, 0, output)
        self.assert_child_gone(api)
        self.assert_child_gone(frontend)
        self.assertNotIn('secret', output)

    def test_occupied_port_keeps_existing_listener_and_starts_nothing(self):
        with socket.socket() as existing:
            existing.bind(('127.0.0.1', self.config['frontendPort']))
            existing.listen()
            process = self.launch()
            output = process.communicate(timeout=10)[0]
            self.assertEqual(process.returncode, 1)
            self.assertIn('port is occupied', output)
            self.assertFalse((self.state / 'bin/api').exists())
            with socket.create_connection(existing.getsockname(), timeout=1):
                pass

    def test_frontend_startup_failure_stops_api(self):
        (self.state / 'fail-frontend').touch()
        process = self.launch()
        api = self.wait_for('api.json', process)
        output = process.communicate(timeout=10)[0]
        self.assertEqual(process.returncode, 1, output)
        self.assert_child_gone(api)

    def test_expired_or_world_readable_credentials_fail_closed(self):
        self.write_json('gitlab.json', {'token': 'test-secret', 'scopes': ['read_api', 'read_repository'], 'expiresAt': '2020-01-01'})
        process = self.launch('--check')
        output = process.communicate(timeout=10)[0]
        self.assertIn('expired', output)
        self.assertEqual(process.returncode, 1)
        (self.state / 'gitlab.json').chmod(0o644)
        process = self.launch('--check')
        output = process.communicate(timeout=10)[0]
        self.assertIn('0600', output)
        self.assertEqual(process.returncode, 1)
        self.assertNotIn('test-secret', output)
        self.assertFalse((self.state / 'bin/api').exists())


if __name__ == '__main__':
    unittest.main()
