#!/usr/bin/env python3
"""Exercise the release gate with isolated synthetic payloads and no network writes."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('workbench_release',ROOT/'scripts/release/publish-workbench.py')
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)

class ReleaseGate(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root=Path(self.temp.name); self.inputs=self.root/'input'; self.inputs.mkdir()
        self.dist=self.root/'dist';self.version='1.1.7';self.commit='a'*40
        (self.inputs/'windows').mkdir()
        self.payloads=release.expected_payloads(self.version)
        for name in self.payloads:
            if name=='install.sh':continue
            path=self.inputs/name;path.write_bytes(('fixture:'+name).encode())
            path.with_name(name+'.sha256').write_text(f'{release.digest(path)}  {name}\n')
        base={'version':self.version,'commit':self.commit,'product_name':release.PRODUCT}
        self.reports={}
        for system in ['linux','darwin']:
            for arch in ['amd64','arm64']:
                self.reports[f'verification-{system}-{arch}.json']=dict(base,platform=f'{system}/{arch}',native_execution='passed',assets={})
        self.reports['verification-macos-app.json']=dict(base,bundle_verification='passed',embedded_core='passed',architectures=['amd64','arm64'],codesign='ad-hoc',notarization='not_run',assets={})
        self.reports['windows/build-report.json']=dict(base,channel='release',source_dirty=False,platforms=['windows/amd64','windows/arm64'],agentdock_authenticode='unsigned')
        self.reports['windows/verification.json']=[dict(base,channel='release',platform=f'windows/{arch}',native_execution=arch=='amd64',assets={}) for arch in ['amd64','arm64']]
        self.reports['windows/verification-scope.json']=dict(base,resolved_commit=self.commit,linux_tested_commit=self.commit)
        self.flush()
        self.mock=patch.object(release,'run',side_effect=lambda *args:self.commit if args[:2]==('git','rev-parse') else self.version if args==('go','run','./tools/release','version') else '')
        self.mock.start();self.addCleanup(self.mock.stop)
    def flush(self):
        for name,value in self.reports.items():(self.inputs/name).write_text(json.dumps(value))
    def assemble(self):return release.assemble(self.inputs,self.dist,self.version,self.commit)
    def test_complete_and_repeat(self):
        result=self.assemble();self.assertEqual(len(result['assets']),16)
        self.assertEqual(len(list(self.dist.iterdir())),34)
        self.assertEqual(self.assemble()['commit'],self.commit)
    def test_missing_target_asset(self):
        (self.inputs/'AgentDockSetup-arm64.exe').unlink()
        with self.assertRaisesRegex(RuntimeError,'Missing verified build input'):self.assemble()
    def test_modified_payload(self):
        (self.inputs/'agentdock_linux_arm64.tar.gz').write_text('changed')
        with self.assertRaisesRegex(RuntimeError,'checksum'):self.assemble()
    def test_mixed_source(self):
        self.reports['verification-darwin-amd64.json']['commit']='b'*40;self.flush()
        with self.assertRaisesRegex(RuntimeError,'different source'):self.assemble()
    def test_conflicting_copies(self):
        other=self.inputs/'other';other.mkdir();(other/'AgentDockSetup-amd64.exe').write_text('different')
        with self.assertRaisesRegex(RuntimeError,'Conflicting artifact'):self.assemble()
    def test_report_hash_is_authoritative(self):
        self.reports['verification-linux-amd64.json']['assets']={'agentdock_linux_amd64.tar.gz':'f'*64};self.flush()
        with self.assertRaisesRegex(RuntimeError,'differs from target'):self.assemble()
    def test_missing_windows_architecture(self):
        self.reports['windows/verification.json'].pop();self.flush()
        with self.assertRaisesRegex(RuntimeError,'per-architecture'):self.assemble()
    def test_unexpected_staged_file(self):
        self.dist.mkdir();(self.dist/'foreign.exe').write_text('not in release')
        with self.assertRaisesRegex(RuntimeError,'unexpected artifacts'):self.assemble()

if __name__=='__main__':unittest.main()
