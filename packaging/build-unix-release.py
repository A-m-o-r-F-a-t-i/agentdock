#!/usr/bin/env python3
"""Build native Unix release payloads; never start services or alter the host install."""
from __future__ import annotations
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import tarfile
import tempfile
from datetime import datetime, timezone

ROOT = Path(__file__).resolve().parents[1]
PRODUCT = "AgentDock Workbench"
LICENSE_ID = "Apache-2.0"


def run(*args: str, env: dict[str, str] | None = None, cwd: Path = ROOT) -> str:
    return subprocess.check_output(args, cwd=cwd, env=env, text=True, stderr=None).strip()


def digest(path: Path) -> str:
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def checksum(path: Path) -> None:
    path.with_name(path.name+'.sha256').write_text(f'{digest(path)}  {path.name}\n', encoding='utf-8')


def linux_packages(stage: Path, output: Path, version: str, arch: str) -> list[Path]:
    with tempfile.TemporaryDirectory(prefix='workbench-packages-') as temporary:
        temp = Path(temporary)
        package = temp/'package'
        (package/'usr/bin').mkdir(parents=True)
        shutil.copy2(stage/'bin/agentdock', package/'usr/bin/agentdock')
        shutil.copytree(stage/'share/agentdock', package/'usr/share/agentdock')
        documentation = package/'usr/share/doc/agentdock-workbench'
        documentation.mkdir(parents=True)
        shutil.copy2(ROOT/'LICENSE', documentation/'copyright')
        (documentation/'README').write_text(
            'AgentDock Workbench\n\nThe package installs the runtime and core Skills. '
            'It does not start a service or change existing user configuration.\n'
            'Run agentdock --version to inspect the build. Use the release install.sh '
            'for interactive service/tunnel configuration. Existing ~/.agentdock data is retained.\n', encoding='utf-8')
        control = package/'DEBIAN'
        control.mkdir()
        (control/'control').write_text(
            f'Package: agentdock-workbench\nVersion: {version}\nArchitecture: {arch}\n'
            'Maintainer: AgentDock Workbench contributors\nSection: devel\nPriority: optional\n'
            'Homepage: https://github.com/A-m-o-r-F-a-t-i/AgentDock-Workbench\n'
            'Description: AgentDock Workbench MCP runtime\n Local tool runtime with tasks, approvals and recoverable execution.\n', encoding='utf-8')
        deb = output/f'agentdock-workbench_{version}_{arch}.deb'
        run('dpkg-deb','--build','--root-owner-group',str(package),str(deb))
        if run('dpkg-deb','--field',str(deb),'Architecture') != arch:
            raise RuntimeError('Debian package architecture mismatch')
        extracted = temp/'deb-extracted'
        run('dpkg-deb','--extract',str(deb),str(extracted))
        if digest(extracted/'usr/bin/agentdock') != digest(stage/'bin/agentdock'):
            raise RuntimeError('Debian package changed the verified runtime')
        rpmarch = 'x86_64' if arch == 'amd64' else 'aarch64'
        rpmtop = temp/'rpm'
        for directory in ['BUILD','BUILDROOT','RPMS','SOURCES','SPECS','SRPMS']:
            (rpmtop/directory).mkdir(parents=True)
        spec = rpmtop/'SPECS/agentdock-workbench.spec'
        spec.write_text(f'''%global debug_package %{{nil}}
%global __os_install_post %{{nil}}
Name: agentdock-workbench
Version: {version}
Release: 1
Summary: AgentDock Workbench MCP runtime
License: {LICENSE_ID}
URL: https://github.com/A-m-o-r-F-a-t-i/AgentDock-Workbench
BuildArch: {rpmarch}
AutoReqProv: no
%description
AgentDock Workbench runtime and core Skills. No service is started automatically.
%install
mkdir -p "%{{buildroot}}/usr"
cp -a "{package}/usr/." "%{{buildroot}}/usr/"
%files
%defattr(-,root,root,-)
/usr/bin/agentdock
/usr/share/agentdock
/usr/share/doc/agentdock-workbench
''', encoding='utf-8')
        run('rpmbuild','-bb','--define',f'_topdir {rpmtop}','--target',rpmarch,str(spec))
        built = list((rpmtop/'RPMS').rglob('*.rpm'))
        if len(built) != 1:
            raise RuntimeError('RPM builder returned an unexpected asset set')
        rpm = output/f'agentdock-workbench-{version}-1.{rpmarch}.rpm'
        shutil.copy2(built[0],rpm)
        if run('rpm','-qp','--queryformat','%{NAME} %{VERSION} %{ARCH} %{LICENSE}',str(rpm)) != f'agentdock-workbench {version} {rpmarch} {LICENSE_ID}':
            raise RuntimeError('RPM metadata mismatch')
        return [deb,rpm]


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument('--os', choices=['linux','darwin'], required=True)
    parser.add_argument('--arch', choices=['amd64','arm64'], required=True)
    parser.add_argument('--commit', required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    native_os = 'darwin' if platform.system() == 'Darwin' else platform.system().lower()
    native_arch = {'x86_64':'amd64','aarch64':'arm64','arm64':'arm64'}.get(platform.machine().lower())
    if (native_os,native_arch) != (args.os,args.arch):
        raise RuntimeError('Native payload validation requires the matching OS and architecture')
    commit = run('git','rev-parse','HEAD')
    if not re.fullmatch('[a-f0-9]{40}',args.commit) or commit != args.commit:
        raise RuntimeError('Build checkout is not the resolved source commit')
    if run('git','status','--porcelain'):
        raise RuntimeError('Release requires a clean source checkout')
    version = run('go','run','./tools/release','version')
    output = args.output.resolve()
    output.mkdir(parents=True,exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='workbench-unix-') as temporary:
        stage = Path(temporary)/'stage'
        (stage/'bin').mkdir(parents=True)
        environment = dict(os.environ,CGO_ENABLED='0',GOOS=args.os,GOARCH=args.arch)
        date = datetime.now(timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
        flags = f'-s -w -X github.com/uvwt/agentdock/internal/buildinfo.Commit={commit} -X github.com/uvwt/agentdock/internal/buildinfo.BuildDate={date}'
        binary = stage/'bin/agentdock'
        run('go','build','-trimpath','-ldflags',flags,'-o',str(binary),'./cmd/agentdock',env=environment)
        run('python3',str(ROOT/'packaging/build-core-skill-bundle.py'),'--output',str(stage/'share/agentdock/core-skills'))
        shutil.copy2(ROOT/'LICENSE',stage/'share/agentdock/LICENSE')
        if digest(stage/'share/agentdock/LICENSE') != digest(ROOT/'LICENSE'):
            raise RuntimeError('License was not preserved in the package')
        info = json.loads(run(str(binary),'version','--json'))
        if (info.get('product_name'),info['version'],info['commit'],info['platform']) != (PRODUCT,version,commit[:12],f'{args.os}/{args.arch}'):
            raise RuntimeError(f'Actual packaged runtime identity mismatch: {info}')
        home = Path(temporary)/'home';home.mkdir()
        env = dict(environment,AGENTDOCK_HOME=str(home),AGENTDOCK_DEFAULT_DIR=str(home))
        for _ in range(2):
            run(str(binary),'skill','bootstrap','--bundle',str(stage/'share/agentdock/core-skills'),env=env)
        archive = output/f'agentdock_{args.os}_{args.arch}.tar.gz'
        with tarfile.open(archive,'w:gz') as tar:
            tar.add(stage/'bin',arcname='bin')
            tar.add(stage/'share',arcname='share')
        assets = [archive]
        if args.os == 'linux':
            assets.extend(linux_packages(stage,output,version,args.arch))
        for asset in assets:checksum(asset)
        report = {'product_name':PRODUCT,'version':version,'commit':commit,'platform':f'{args.os}/{args.arch}',
                  'native_execution':'passed','core_skill_bootstrap':'passed','package_installation':'not_run','license':LICENSE_ID,
                  'assets':{asset.name:digest(asset) for asset in assets}}
        (output/f'verification-{args.os}-{args.arch}.json').write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
        print(json.dumps(report,ensure_ascii=False))

if __name__ == '__main__':
    main()
