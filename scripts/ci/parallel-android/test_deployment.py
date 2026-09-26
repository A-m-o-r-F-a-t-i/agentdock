"""Synthetic deployment fault injection. No production node/Termux installation."""
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

MODULE = Path(__file__).resolve().parents[3] / 'mobile/android/termux/agentdock_workbench.py'
spec = importlib.util.spec_from_file_location('wb_deployment', MODULE)
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)

class SimulatedCrash(BaseException):
    pass

class FakeBackend(m.NodeBackend):
    """Models process I/O only; filesystem/journals/signatures/extraction remain real."""
    def __init__(self, root, config, state):
        super().__init__(root, config)
        self.state = state
    def environment(self):
        return {'missing': [], 'architecture': 'synthetic-arm64', 'arm64': True}
    def validate_environment(self):
        pass
    def owned_pid(self):
        if self.state.get('unknown_pid'):
            raise m.BridgeError('unknown_process', 'fixture unknown process')
        return 424242 if self.state.get('running') else None
    def health(self, expected_version=''):
        return self.state.get('running', False) and self.state.get('version') not in self.state.get('fail_versions', set()) and (not expected_version or self.state.get('version') == expected_version)
    def binary_version(self, directory):
        return (directory / 'bin/agentdock').read_text().strip()
    def stop(self):
        self.owned_pid()
        self.state['stops'] = self.state.get('stops', 0) + 1
        self.state['running'] = False
    def start(self, version):
        self.bearer()
        m.check(m.read_text(self.root / 'desired-state') == 'running', 'user_stopped', 'stop respected')
        self.state.setdefault('starts', []).append(version)
        self.state.update(running=True, version=version)
        (self.root / 'data/schema.txt').write_text(version)
        if version in self.state.get('fail_versions', set()):
            raise m.BridgeError('health_failed', 'synthetic startup failure')
    def fetch(self, url, target, maximum, resume=False):
        self.state.setdefault('downloads', []).append((url, resume))
        data = self.state['assets'][url]
        m.check(len(data) <= maximum, 'size_limit', 'download bound')
        if self.state.get('break_download') and url.endswith('/core.tar.gz'):
            target.write_bytes(data[:10])
            self.state['break_download'] = False
            raise m.BridgeError('download_failed', 'partial synthetic transfer')
        target.write_bytes(data)
    # Signature verification deliberately uses the real OpenSSL implementation.

class DeploymentTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='wb07-deployment-')
        self.home = Path(self.tmp.name)
        self.state = {'assets': {}, 'running': False, 'starts': [], 'fail_versions': set()}
        self.now = [1000.0]
        self.manager = self.make()
        self.manager.initialize()
        m.atomic_bytes(self.manager.root / 'auth-token', b'1' * 64)
        self.private_key = self.home / 'test-release-key.pem'
        key = self.manager.state / 'trust/release-ed25519-public.pem'
        key.parent.mkdir(parents=True)
        subprocess.run(['openssl', 'genpkey', '-algorithm', 'ED25519', '-out', str(self.private_key)], check=True, capture_output=True)
        subprocess.run(['openssl', 'pkey', '-in', str(self.private_key), '-pubout', '-out', str(key)], check=True, capture_output=True)
        (self.manager.root / 'workspace/project.txt').write_text('user project')
        (self.manager.root / 'data/config.txt').write_text('preserve settings')
    def tearDown(self):
        self.tmp.cleanup()
    def make(self, fault=lambda _: None):
        return m.Deployment(self.home, lambda root, config: FakeBackend(root, config, self.state), lambda: self.now[0], fault)
    def release(self, version='1.1.8', corrupt_digest=False):
        content = io.BytesIO()
        with tarfile.open(fileobj=content, mode='w:gz') as archive:
            data = version.encode()
            entry = tarfile.TarInfo('bin/agentdock'); entry.size = len(data); entry.mode = 0o755
            archive.addfile(entry, io.BytesIO(data))
        data = content.getvalue()
        digest = m.hashlib.sha256(data).hexdigest()
        manifest = {'schema_version': 1, 'platform': 'linux', 'arch': 'arm64', 'version': version,
                    'asset_url': 'https://release.example/core.tar.gz', 'sha256': '0' * 64 if corrupt_digest else digest,
                    'data_schema': 'fixture-' + version}
        source = self.home / 'manifest-to-sign.json'; source.write_bytes(m.json_bytes(manifest))
        signature = self.home / 'test.sig'
        subprocess.run(['openssl', 'pkeyutl', '-sign', '-inkey', str(self.private_key), '-rawin', '-in', str(source), '-out', str(signature)], check=True, capture_output=True)
        self.state['assets'] = {'https://release.example/manifest.json': source.read_bytes(),
                                'https://release.example/manifest.sig': signature.read_bytes(),
                                'https://release.example/core.tar.gz': data}
        return {'apk_version': version, 'manifest_url': 'https://release.example/manifest.json',
                'manifest_signature_url': 'https://release.example/manifest.sig', 'start_after_install': True}
    def seed(self, version='1.1.7', name='old'):
        path = self.manager.root / 'versions' / name
        (path / 'bin').mkdir(parents=True)
        (path / 'bin/agentdock').write_text(version); (path / 'bin/agentdock').chmod(0o755)
        metadata = {'version': version, 'platform': 'linux', 'arch': 'arm64', 'sha256': '2' * 64,
                    'binary_sha256': m.sha256(path / 'bin/agentdock'), 'data_schema': 'fixture-' + version}
        m.atomic_json(path / '.workbench-release.json', metadata)
        self.manager.set_pointer('current', name)
        self.manager.set_desired('running')
        self.state.update(running=True, version=version)
        (self.manager.root / 'data/schema.txt').write_text(version)
        return name
    def test_read_only_discovery_creates_nothing(self):
        with tempfile.TemporaryDirectory() as empty:
            manager = m.Deployment(Path(empty))
            manager.dispatch('probe', 'probe', {})
            manager.dispatch('status', 'status', {})
            self.assertEqual([], list(Path(empty).iterdir()))
    def test_missing_trust_downloads_nothing(self):
        (self.manager.state / 'trust/release-ed25519-public.pem').unlink()
        result = self.manager.dispatch('install', 'install_missing', self.release())
        self.assertEqual('pending_manifest', result['status'])
        self.assertFalse(self.state.get('downloads'))
        self.assertEqual('', self.manager.pointer('current'))
    def test_install_idempotency_and_query(self):
        payload = self.release()
        first = self.manager.dispatch('install', 'install_once', payload)
        self.assertEqual('installed', first['status'])
        starts = len(self.state['starts'])
        repeat = self.manager.dispatch('install', 'install_once', payload)
        self.assertEqual('ok', repeat['status']); self.assertEqual(starts, len(self.state['starts']))
        query = self.manager.dispatch('operation_query', 'read_once', {'target_operation_id': 'install_once'})
        self.assertEqual('succeeded', query['data']['status'])
        self.assertEqual('user project', (self.manager.root / 'workspace/project.txt').read_text())
        self.assertEqual('1' * 64, (self.manager.root / 'auth-token').read_text())
    def test_same_operation_different_payload_rejected(self):
        payload = self.release()
        self.manager.dispatch('install', 'bound_op', payload)
        with self.assertRaises(m.BridgeError):
            self.manager.dispatch('install', 'bound_op', dict(payload, apk_version='2.0.0'))
    def test_invalid_signature_never_stops_existing_core(self):
        self.seed(); payload = self.release()
        self.state['assets']['https://release.example/manifest.sig'] = b'wrong'
        result = self.manager.dispatch('update', 'bad_signature', payload)
        self.assertEqual('requires_user_action', result['status'])
        self.assertEqual('trust_failed', result['data']['failure_code'])
        self.assertTrue(self.state['running']); self.assertEqual('old', self.manager.pointer('current'))
    def test_digest_mismatch_never_switches_or_stops(self):
        self.seed()
        result = self.manager.dispatch('update', 'bad_digest', self.release(corrupt_digest=True))
        self.assertEqual('verification_failed', result['data']['failure_code'])
        self.assertEqual(0, self.state.get('stops', 0)); self.assertEqual('old', self.manager.pointer('current'))
    def test_apk_core_version_mismatch_blocks_before_asset(self):
        self.seed(); payload = self.release(); payload['apk_version'] = '9.9.9'
        result = self.manager.dispatch('update', 'bad_version', payload)
        self.assertEqual('version_mismatch', result['data']['failure_code'])
        self.assertFalse(any(url.endswith('/core.tar.gz') for url, _ in self.state.get('downloads', [])))
    def test_partial_download_resumes_same_operation(self):
        self.seed(); payload = self.release(); self.state['break_download'] = True
        first = self.manager.dispatch('update', 'partial', payload)
        self.assertEqual('requires_user_action', first['status'])
        result = self.make().dispatch('resume', 'resume_partial', {'target_operation_id': 'partial', 'confirm_start': True})
        self.assertEqual('updated', result['status'])
        self.assertTrue(any(resume for url, resume in self.state['downloads'] if url.endswith('/core.tar.gz')))
    def test_changed_manifest_requires_a_new_confirmation(self):
        self.seed(); payload = self.release(); self.state['break_download'] = True
        self.manager.dispatch('update', 'changing', payload)
        self.release('1.1.9')
        result = self.make().dispatch('resume', 'resume_changing', {'target_operation_id': 'changing'})
        self.assertEqual('manifest_changed', result['data']['failure_code'])
        self.assertEqual('old', self.manager.pointer('current'))
    def test_health_failure_restores_schema_and_keeps_new_data(self):
        self.seed(); self.state['fail_versions'].add('1.1.8')
        result = self.manager.dispatch('update', 'bad_health', self.release())
        self.assertEqual('failed', result['status']); self.assertEqual('old', self.manager.pointer('current'))
        self.assertEqual('1.1.7', (self.manager.root / 'data/schema.txt').read_text())
        self.assertEqual('1.1.8', (self.manager.root / 'quarantine/bad_health-failed-new-data/schema.txt').read_text())
        self.assertTrue(self.state['running'])
    def test_stop_intent_prevents_resume_start(self):
        self.seed()
        def fault(phase):
            if phase == 'start':
                self.manager.set_desired('stopped')
        result = self.make(fault).dispatch('update', 'stopped', self.release())
        self.assertEqual('requires_user_action', result['status'])
        self.assertFalse(self.state['running']); self.assertEqual('stopped', self.manager.desired())
        again = self.make().dispatch('resume', 'resume_stopped', {'target_operation_id': 'stopped'})
        self.assertEqual('requires_user_action', again['status']); self.assertFalse(self.state['running'])
    def test_cleanup_retains_only_current_and_verified_fallback(self):
        self.seed('1.1.6', 'obsolete')
        self.seed()
        result = self.manager.dispatch('update', 'retention', self.release())
        self.assertEqual('updated', result['status'])
        names = {p.name for p in (self.manager.root / 'versions').iterdir() if not p.name.startswith('.')}
        self.assertEqual({'old', self.manager.pointer('current')}, names)
        self.assertEqual('old', self.manager.pointer('previous'))
        self.assertEqual('preserve settings', (self.manager.root / 'backups/retention/data/config.txt').read_text())
    def test_explicit_rollback_restores_consistent_old_data(self):
        self.seed()
        self.manager.dispatch('update', 'upgrade', self.release())
        (self.manager.root / 'data/config.txt').write_text('post-upgrade configuration')
        result = self.manager.dispatch('rollback', 'rollback_confirmed', {'confirm_data_restore': True, 'start_after_install': True})
        self.assertEqual('rolled_back', result['status']); self.assertEqual('old', self.manager.pointer('current'))
        self.assertEqual('preserve settings', (self.manager.root / 'data/config.txt').read_text())
        self.assertEqual('post-upgrade configuration', (self.manager.root / 'quarantine/rollback_confirmed-pre-rollback-data/config.txt').read_text())
    def test_tampered_backup_never_restores(self):
        self.seed(); self.manager.dispatch('update', 'upgrade', self.release())
        (self.manager.root / 'backups/upgrade/data/config.txt').write_text('corrupted snapshot')
        result = self.manager.dispatch('rollback', 'rollback_corrupt', {'confirm_data_restore': True, 'start_after_install': True})
        self.assertIn(result['status'], {'failed', 'requires_user_action'})
        self.assertNotEqual('corrupted snapshot', (self.manager.root / 'data/config.txt').read_text())
    def test_unfinished_transaction_protects_refs_and_blocks_another(self):
        self.seed(); self.state['break_download'] = True
        payload = self.release(); self.manager.dispatch('update', 'pending', payload)
        with self.assertRaises(m.BridgeError):
            self.manager.dispatch('update', 'different', payload)
        self.manager.cleanup(False)
        self.assertTrue((self.manager.root / 'versions/old').is_dir())
    def test_cancel_before_switch_does_not_touch_running_source(self):
        self.seed(); self.state['break_download'] = True
        self.manager.dispatch('update', 'cancel_me', self.release())
        result = self.manager.dispatch('cancel_operation', 'cancel', {'target_operation_id': 'cancel_me', 'confirm_cancel': True})
        self.assertEqual('ok', result['status']); self.assertEqual('cancelled', result['data']['status'])
        self.assertTrue(self.state['running']); self.assertEqual('old', self.manager.pointer('current'))
    def test_runtime_restart_receipt_prevents_replay(self):
        self.seed()
        self.manager.dispatch('restart', 'restart_once', {})
        count = len(self.state['starts'])
        self.manager.dispatch('restart', 'restart_once', {})
        self.assertEqual(count, len(self.state['starts']))
        self.assertEqual('succeeded', self.manager.query('restart_once')['status'])
    def test_unknown_runtime_completion_is_not_replayed(self):
        self.seed()
        def fault(phase):
            if phase == 'running': raise SimulatedCrash()
        with self.assertRaises(SimulatedCrash): self.make(fault).dispatch('restart', 'unknown', {})
        result = self.make().dispatch('restart', 'unknown', {})
        self.assertEqual('requires_user_action', result['status']); self.assertEqual([], self.state['starts'])
    def test_recovery_backoff_and_circuit_persist(self):
        self.seed(); self.state.update(running=False, fail_versions={'1.1.7'})
        for index in range(3):
            result = self.make().dispatch('guardian_check', 'repair_' + str(index), {})
            self.assertEqual('failed', result['status'])
            stored = m.read_json(self.manager.root / 'recovery.json')
            self.assertEqual(index + 1, stored['failures'])
            self.now[0] = stored['next_attempt_at']
        calls = len(self.state['starts'])
        result = self.make().dispatch('guardian_check', 'after_circuit', {})
        self.assertEqual('requires_user_action', result['status']); self.assertEqual(calls, len(self.state['starts']))
        self.state['fail_versions'] = set()
        reset = self.make().dispatch('repair', 'manual_reset', {'confirm_reset': True})
        self.assertEqual('healthy', reset['status'])
        self.assertEqual(0, m.read_json(self.manager.root / 'recovery.json')['failures'])
    def test_guardian_does_not_restart_explicitly_stopped_node(self):
        self.seed(); self.manager.set_desired('stopped'); self.state['running'] = False
        result = self.manager.dispatch('guardian_check', 'intent_stop', {})
        self.assertEqual('stopped', result['status']); self.assertEqual([], self.state['starts'])
    def test_unknown_pid_is_never_stopped(self):
        self.seed(); self.state['unknown_pid'] = True
        result = self.manager.dispatch('stop', 'unknown_pid', {})
        self.assertEqual('requires_user_action', result['status']); self.assertEqual('stopped', self.manager.desired())
        self.assertEqual(0, self.state.get('stops', 0))
    def test_configuration_is_validated_and_requires_stopped_node(self):
        result = self.manager.dispatch('configure', 'cfg', {'node': {'port': 9876, 'distro': 'debian', 'node_name': 'phone'}})
        self.assertEqual('ok', result['status']); self.assertEqual(9876, self.make().config['port'])
        bad = self.manager.dispatch('configure', 'bad_cfg', {'node': {'port': 22}})
        self.assertEqual('failed', bad['status']); self.assertEqual(9876, self.make().config['port'])
    def test_secret_fields_and_unknown_arguments_rejected(self):
        for payload in ({'credentials': {'value': 'bad'}}, {'extra': True}):
            with self.assertRaises(m.BridgeError): self.manager.dispatch('start', 'bad_field', payload)
    def test_no_credentials_or_private_database_in_diagnostic(self):
        (self.manager.root / 'logs/core.log').write_text('Bearer secret-value\npassword=private-password\n' + 'a' * 64 + '\npublic information\n')
        result = self.manager.dispatch('export_diagnostics', 'diagnostic', {})
        value = (self.manager.root / result['data']['relative_path']).read_text()
        for private in ('secret-value', 'private-password', 'a' * 64, 'preserve settings', '1' * 64):
            self.assertNotIn(private, value)
    def test_log_cursor_does_not_expose_partial_secret(self):
        line = 'prefix Bearer ' + 's' * 12000 + '\nnext line\n'
        (self.manager.root / 'logs/core.log').write_text(line)
        result = self.manager.dispatch('logs', 'logs', {'offset': 100})
        self.assertNotIn('sss', result['data']['text']); self.assertIn('next line', result['data']['text'])
    def test_backup_link_escape_rejected(self):
        (self.manager.root / 'data/escape').symlink_to('../../outside')
        with self.assertRaises(m.BridgeError): m.snapshot_tree(self.manager.root / 'data', self.manager.root / 'backups/bad')
    def test_cleanup_requires_exact_preview(self):
        result = self.manager.dispatch('cleanup', 'clean_bad', {'confirm_cleanup': True, 'preview_digest': 'wrong'})
        self.assertEqual('failed', result['status'])
        preview = self.manager.dispatch('cleanup_preview', 'preview', {})
        result = self.manager.dispatch('cleanup', 'clean_good', {'confirm_cleanup': True, 'preview_digest': preview['data']['preview_digest']})
        self.assertEqual('ok', result['status'])

# Each phase is a distinct unittest case so a skipped phase cannot disappear from CI evidence.
def phase_case(phase):
    def test(self):
        self.seed(); payload = self.release(); fired = [False]
        def fault(current):
            if current == phase and not fired[0]:
                fired[0] = True
                raise SimulatedCrash()
        with self.assertRaises(SimulatedCrash):
            self.make(fault).dispatch('update', 'crash_case', payload)
        self.assertTrue(fired[0])
        result = self.make().dispatch('resume', 'resume_case', {'target_operation_id': 'crash_case', 'confirm_start': True})
        self.assertIn(result['status'], {'updated', 'ok'})
        self.assertEqual('1.1.8', self.manager.current_version())
        self.assertEqual('preserve settings', (self.manager.root / 'data/config.txt').read_text())
        self.assertEqual('user project', (self.manager.root / 'workspace/project.txt').read_text())
        self.assertEqual('succeeded', self.manager.query('crash_case')['status'])
    return test
for phase in ('created','manifest','download','verify','prepared','stop','backup','switch','pointer-published','start','health','commit','complete'):
    setattr(DeploymentTest, 'test_resume_after_' + phase.replace('-', '_'), phase_case(phase))

if __name__ == '__main__':
    suite = unittest.defaultTestLoader.loadTestsFromTestCase(DeploymentTest)
    result = unittest.TextTestRunner(verbosity=2).run(suite)
    output = Path(os.environ.get('WB07_DEPLOYMENT_RESULT', 'evidence/contract/deployment-result.json'))
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps({'scope':'synthetic filesystem and injected process backend; real OpenSSL signature checks',
        'tests': result.testsRun, 'failures':len(result.failures), 'errors':len(result.errors), 'skipped':len(result.skipped),
        'production_installed':False, 'source_sha':os.environ.get('GITHUB_SHA','unknown')}, indent=2) + '\n')
    raise SystemExit(0 if result.wasSuccessful() and result.testsRun >= 40 and not result.skipped else 1)
