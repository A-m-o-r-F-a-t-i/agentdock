#!/usr/bin/env python3
"""Exercise real package managers only inside an explicitly enabled hosted runner."""
from __future__ import annotations
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import tempfile

ROOT=Path(__file__).resolve().parents[2]
PACKAGE='agentdock-workbench'

def command(*args: str, check: bool=True) -> subprocess.CompletedProcess:
    return subprocess.run(args,cwd=ROOT,text=True,encoding='utf-8',stdout=subprocess.PIPE,stderr=subprocess.PIPE,check=check,timeout=180)

def sha256(path: Path) -> str:
    with path.open('rb') as stream:return hashlib.file_digest(stream,'sha256').hexdigest()

def inspect_core(binary: Path,version: str,commit: str,arch: str) -> None:
    data=json.loads(command(str(binary),'version','--json').stdout)
    if (data.get('product_name'),data.get('version'),data.get('commit'),data.get('platform'))!=('AgentDock Workbench',version,commit[:12],'linux/'+arch):
        raise RuntimeError('Installed runtime does not match verified native build')

def main() -> None:
    parser=argparse.ArgumentParser()
    parser.add_argument('--directory',type=Path,required=True)
    parser.add_argument('--version',required=True)
    parser.add_argument('--commit',required=True)
    parser.add_argument('--arch',choices=['amd64','arm64'],required=True)
    args=parser.parse_args()
    if os.environ.get('GITHUB_ACTIONS')!='true' or os.environ.get('AGENTDOCK_NATIVE_ACCEPTANCE')!='1' or platform.system()!='Linux':
        raise RuntimeError('Native package installation is restricted to explicitly enabled isolated GitHub Linux runners')
    native={'x86_64':'amd64','aarch64':'arm64'}.get(platform.machine())
    if native!=args.arch:raise RuntimeError('Native architecture mismatch')
    report_path=args.directory/f'verification-linux-{args.arch}.json'
    report=json.loads(report_path.read_text())
    if (report['version'],report['commit'])!=(args.version,args.commit):raise RuntimeError('Source identity differs from built payload')
    directory=args.directory.resolve()
    deb=directory/f'{PACKAGE}_{args.version}_{args.arch}.deb'
    rpm=directory/f'{PACKAGE}-{args.version}-1.{"x86_64" if args.arch=="amd64" else "aarch64"}.rpm'
    for path in [deb,rpm]:
        if report['assets'].get(path.name)!=sha256(path):raise RuntimeError('Package changed after native build')
    existing=command('dpkg-query','-W','-f=${db:Status-Abbrev}',PACKAGE,check=False)
    if existing.returncode==0 and existing.stdout.startswith('ii'):raise RuntimeError('Refuse to replace an existing package')
    attempted=False
    try:
        attempted=True
        command('sudo','-n','dpkg','--install',str(deb))
        metadata=command('dpkg-query','-W','-f=${Version} ${Architecture}',PACKAGE).stdout.strip()
        if metadata!=args.version+' '+args.arch:raise RuntimeError('Installed Debian metadata mismatch')
        inspect_core(Path('/usr/bin/agentdock'),args.version,args.commit,args.arch)
        if sha256(Path('/usr/share/agentdock/LICENSE'))!=sha256(ROOT/'LICENSE'):raise RuntimeError('Installed Debian license missing')
        command('sudo','-n','dpkg','--remove',PACKAGE)
        attempted=False
        if Path('/usr/bin/agentdock').exists():raise RuntimeError('Debian uninstall left its binary behind')
    finally:
        if attempted:command('sudo','-n','dpkg','--remove',PACKAGE,check=False)
    # RPM's real transaction engine runs against a disposable root/database on
    # the matching CPU. This is not described as a complete Fedora/RHEL VM test.
    with tempfile.TemporaryDirectory(prefix='workbench-rpm-',dir=os.environ['RUNNER_TEMP']) as temporary:
        root=Path(temporary)/'root';root.mkdir()
        prefix=['sudo','-n','rpm','--root',str(root),'--dbpath','/var/lib/rpm']
        attempted=False
        try:
            command(*prefix,'--initdb')
            attempted=True
            command(*prefix,'--install','--nodeps',str(rpm))
            installed=command(*prefix,'-q','--queryformat','%{VERSION} %{ARCH}',PACKAGE).stdout.strip()
            expected=args.version+' '+('x86_64' if args.arch=='amd64' else 'aarch64')
            if installed!=expected:raise RuntimeError('Installed RPM metadata mismatch')
            inspect_core(root/'usr/bin/agentdock',args.version,args.commit,args.arch)
            if sha256(root/'usr/share/agentdock/LICENSE')!=sha256(ROOT/'LICENSE'):raise RuntimeError('Installed RPM license missing')
            command(*prefix,'--erase',PACKAGE)
            attempted=False
            if (root/'usr/bin/agentdock').exists():raise RuntimeError('RPM uninstall left its binary behind')
        finally:
            if attempted:command(*prefix,'--erase',PACKAGE,check=False)
            command('sudo','-n','chown','-R',f'{os.getuid()}:{os.getgid()}',temporary)
    report['package_installation']={'deb':'native_installed_verified_removed','rpm':'isolated_root_installed_verified_removed','services_started':False}
    report_path.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
    print(json.dumps(report['package_installation']))

if __name__=='__main__':main()
