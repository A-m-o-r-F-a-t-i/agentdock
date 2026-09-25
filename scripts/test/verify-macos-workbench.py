#!/usr/bin/env python3
from __future__ import annotations
import hashlib
import json
from pathlib import Path
import plistlib
import re
import subprocess
import sys
import zipfile


def run(*args: str) -> str:
    return subprocess.check_output(args,text=True).strip()


def digest(path: Path) -> str:
    with path.open('rb') as stream:
        return hashlib.file_digest(stream,'sha256').hexdigest()


def main() -> None:
    if len(sys.argv)!=4:
        raise SystemExit('usage: verify-macos-workbench.py DIRECTORY VERSION COMMIT')
    directory=Path(sys.argv[1]).resolve();version,commit=sys.argv[2:]
    if not re.fullmatch('[a-f0-9]{40}',commit):raise ValueError('Expected immutable SHA')
    app=directory/'AgentDock.app'
    info=plistlib.loads((app/'Contents/Info.plist').read_bytes())
    if any(info.get(key)!='AgentDock Workbench' for key in ['CFBundleDisplayName','CFBundleName']):
        raise RuntimeError('Incorrect macOS display identity')
    if info.get('CFBundleShortVersionString')!=version or info.get('CFBundleIdentifier')!='com.uvwt.agentdock':
        raise RuntimeError('Version or stable application identity changed')
    for path in ['Contents/MacOS/AgentDock','Contents/Helpers/agentdock','Contents/Helpers/cloudflared','Contents/Helpers/agentdock-arbiter','Contents/Helpers/AgentDockLoginHelper']:
        binary=app/path
        if set(run('lipo','-archs',str(binary)).split())!={'arm64','x86_64'}:
            raise RuntimeError(f'Missing universal architecture: {path}')
    run('codesign','--verify','--deep','--strict',str(app))
    core=json.loads(run(str(app/'Contents/Helpers/agentdock'),'version','--json'))
    if (core.get('product_name'),core['version'],core['commit'])!=('AgentDock Workbench',version,commit[:12]):
        raise RuntimeError('Embedded Core was not built from the verified source')
    dmg=directory/'AgentDock-macos-universal.dmg'
    archive=directory/'AgentDock-macos-universal.zip'
    run('hdiutil','verify',str(dmg))
    with zipfile.ZipFile(archive) as package:
        if package.testzip() is not None:raise RuntimeError('Invalid update ZIP')
        if 'AgentDock.app/Contents/Info.plist' not in package.namelist():raise RuntimeError('Update ZIP missing the stable bundle path')
    for asset in [dmg,archive]:
        expected=f'{digest(asset)}  {asset.name}'
        if asset.with_name(asset.name+'.sha256').read_text().strip()!=expected:
            raise RuntimeError(f'Checksum mismatch: {asset.name}')
    report={'product_name':'AgentDock Workbench','version':version,'commit':commit,'platform':'darwin/universal',
            'architectures':['amd64','arm64'],'bundle_verification':'passed','embedded_core':'passed',
            'codesign':'ad-hoc','notarization':'not_run_no_developer_identity',
            'assets':{asset.name:digest(asset) for asset in [dmg,archive]}}
    (directory/'verification-macos-app.json').write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
    print(json.dumps(report,ensure_ascii=False))

if __name__=='__main__':main()
