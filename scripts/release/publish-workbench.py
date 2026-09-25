#!/usr/bin/env python3
"""Assemble one verified source generation, then optionally publish its complete release."""
from __future__ import annotations
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[2]
REPOSITORY = 'A-m-o-r-F-a-t-i/agentdock'
PRODUCT = 'AgentDock Workbench'


def run(*args: str) -> str:
    return subprocess.check_output(args,cwd=ROOT,text=True).strip()


def digest(path: Path) -> str:
    with path.open('rb') as stream:
        return hashlib.file_digest(stream,'sha256').hexdigest()


def locate(directory: Path, name: str) -> Path:
    paths = sorted(directory.rglob(name))
    paths = [path for path in paths if path.is_file() and not path.is_symlink()]
    if not paths:
        raise RuntimeError(f'Missing verified build input: {name}')
    if len({digest(path) for path in paths}) != 1:
        raise RuntimeError(f'Conflicting artifact copies: {name}')
    return paths[0]


def read_report(directory: Path,name: str) -> dict | list:
    return json.loads(locate(directory,name).read_text(encoding='utf-8-sig'))


def identity(report: dict,version: str,commit: str) -> None:
    if report.get('version')!=version or report.get('commit')!=commit:
        raise RuntimeError('Validation/build reports refer to different source generations')


def expected_payloads(version: str) -> list[str]:
    return [f'agentdock_{platform}_{arch}.tar.gz' for platform in ['linux','darwin'] for arch in ['amd64','arm64']] + [
        f'agentdock_windows_{arch}.zip' for arch in ['amd64','arm64']] + [
        f'AgentDockSetup-{arch}.exe' for arch in ['amd64','arm64']] + [
        'AgentDock-macos-universal.dmg','AgentDock-macos-universal.zip',
        f'agentdock-workbench_{version}_amd64.deb',f'agentdock-workbench_{version}_arm64.deb',
        f'agentdock-workbench-{version}-1.x86_64.rpm',f'agentdock-workbench-{version}-1.aarch64.rpm',
        'install.sh','install.ps1']


def assemble(inputs: Path,dist: Path,version: str,commit: str) -> dict:
    if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+',version) or not re.fullmatch(r'[a-f0-9]{40}',commit):
        raise RuntimeError('Invalid release identity')
    if run('git','rev-parse','HEAD')!=commit or run('go','run','./tools/release','version')!=version:
        raise RuntimeError('Publisher checkout does not match build evidence')
    reports=[]
    for platform in ['linux','darwin']:
        for arch in ['amd64','arm64']:
            report=read_report(inputs,f'verification-{platform}-{arch}.json')
            identity(report,version,commit)
            if report.get('platform')!=f'{platform}/{arch}' or report.get('native_execution')!='passed' or report.get('product_name')!=PRODUCT:
                raise RuntimeError('Missing native Unix execution/branding evidence')
            reports.append(report)
    mac=read_report(inputs,'verification-macos-app.json');identity(mac,version,commit)
    if mac.get('bundle_verification')!='passed' or mac.get('embedded_core')!='passed' or set(mac.get('architectures',[]))!={'amd64','arm64'}:
        raise RuntimeError('macOS universal app verification incomplete')
    reports.append(mac)
    windows=read_report(inputs/'windows','build-report.json');identity(windows,version,commit)
    if windows.get('source_dirty') or windows.get('channel')!='release' or set(windows.get('platforms',[]))!={'windows/amd64','windows/arm64'}:
        raise RuntimeError('Windows build was not a clean dual-architecture release build')
    verified=read_report(inputs/'windows','verification.json')
    if not isinstance(verified,list) or len(verified)!=2:
        raise RuntimeError('Missing per-architecture Windows verification')
    if {item.get('platform') for item in verified}!={'windows/amd64','windows/arm64'}:
        raise RuntimeError('Windows verification architecture mismatch')
    for item in verified:
        identity(item,version,commit)
        if item.get('product_name')!=PRODUCT or item.get('channel')!='release':
            raise RuntimeError('Windows verified product/channel mismatch')
        if item['platform']=='windows/amd64' and item.get('native_execution') is not True:
            raise RuntimeError('Windows x64 packaged execution was not verified')
    reports.extend(verified)
    scope=read_report(inputs/'windows','verification-scope.json');identity(scope,version,commit)
    if scope.get('resolved_commit')!=commit or scope.get('linux_tested_commit')!=commit:
        raise RuntimeError('Windows workflow lost its immutable validation source')
    dist.mkdir(parents=True,exist_ok=True)
    payloads=expected_payloads(version)
    for name in payloads:
        if name=='install.sh':
            source=ROOT/'scripts/install/install.sh'
            shutil.copy2(source,dist/name)
            (dist/(name+'.sha256')).write_text(f'{digest(source)}  {name}\n',encoding='utf-8')
        else:
            source=locate(inputs,name)
            expected=f'{digest(source)}  {name}'
            if locate(inputs,name+'.sha256').read_text(encoding='utf-8-sig').strip()!=expected:
                raise RuntimeError(f'Invalid published checksum: {name}')
            shutil.copy2(source,dist/name)
            shutil.copy2(locate(inputs,name+'.sha256'),dist/(name+'.sha256'))
    actual={name:digest(dist/name) for name in payloads}
    for report in reports:
        for name,checksum in report['assets'].items():
            if name not in actual or actual[name]!=checksum:
                raise RuntimeError(f'Artifact differs from target-platform verification: {name}')
    run('go','run','./tools/release','verify-dist',str(dist))
    manifest={'schema_version':1,'product_name':PRODUCT,'version':version,'commit':commit,
              'platforms':[f'{platform}/{arch}' for platform in ['windows','linux','darwin'] for arch in ['amd64','arm64']],
              'signing':{'windows':windows['agentdock_authenticode'],'macos':mac['codesign'],'macos_notarization':mac['notarization']},
              'validation':reports,'windows_verification_scope':scope,
              'assets':[{'name':name,'bytes':(dist/name).stat().st_size,'sha256':actual[name]} for name in sorted(payloads)]}
    (dist/'release-manifest.json').write_text(json.dumps(manifest,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
    (dist/'SHA256SUMS').write_text(''.join(f'{digest(dist/name)}  {name}\n' for name in sorted(payloads+['release-manifest.json'])),encoding='utf-8')
    allowed=set(payloads+[name+'.sha256' for name in payloads]+['release-manifest.json','SHA256SUMS'])
    if {path.name for path in dist.iterdir()}!=allowed:
        raise RuntimeError('Staging directory contains unexpected artifacts')
    return manifest


def publish(dist: Path,version: str,commit: str) -> None:
    if os.environ.get('GITHUB_REPOSITORY')!=REPOSITORY:
        raise RuntimeError('Publication is restricted to the user fork')
    tag='v'+version
    notes=ROOT/f'docs/releases/{tag}.md'
    if not notes.is_file():raise RuntimeError('Final release notes are missing')
    remote=run('git','ls-remote','--tags','origin',f'refs/tags/{tag}',f'refs/tags/{tag}^{{}}')
    if remote:
        refs={line.split()[1]:line.split()[0] for line in remote.splitlines()}
        resolved=refs.get(f'refs/tags/{tag}^{{}}',refs.get(f'refs/tags/{tag}'))
        if resolved!=commit:raise RuntimeError('Remote release tag does not match the verified source')
    else:
        subprocess.run(['git','tag',tag,commit],cwd=ROOT,check=True)
        subprocess.run(['git','push','origin',f'refs/tags/{tag}'],cwd=ROOT,check=True)
    result=subprocess.run(['gh','api',f'repos/{REPOSITORY}/releases/tags/{tag}'],text=True,capture_output=True)
    if result.returncode==0:
        if not json.loads(result.stdout).get('draft'):
            raise RuntimeError('Refusing to replace an already published release')
    elif '404' in result.stderr or 'Not Found' in result.stderr:
        run('gh','release','create',tag,'--repo',REPOSITORY,'--verify-tag','--target',commit,'--draft','--title',f'{PRODUCT} {version}','--notes-file',str(notes))
    else:
        raise RuntimeError('Could not determine existing release status: '+result.stderr)
    files=sorted(path for path in dist.iterdir() if path.is_file())
    run('gh','release','upload',tag,'--repo',REPOSITORY,'--clobber',*(str(path) for path in files))
    record=json.loads(run('gh','api',f'repos/{REPOSITORY}/releases/tags/{tag}'))
    assets={asset['name']:asset for asset in record['assets']}
    if set(assets)!={path.name for path in files}:raise RuntimeError('Remote draft asset set differs from the verified release')
    for path in files:
        asset=assets[path.name]
        if asset['size']!=path.stat().st_size or asset.get('digest')!='sha256:'+digest(path):
            raise RuntimeError(f'Remote asset integrity mismatch: {path.name}')
    run('gh','release','edit',tag,'--repo',REPOSITORY,'--draft=false','--prerelease=false','--latest','--title',f'{PRODUCT} {version}','--notes-file',str(notes))
    latest=json.loads(run('gh','api',f'repos/{REPOSITORY}/releases/latest'))
    if latest['tag_name']!=tag or latest['draft'] or latest['prerelease'] or latest['name']!=f'{PRODUCT} {version}':
        raise RuntimeError('Published release identity/Latest status mismatch')
    print(latest['html_url'])
    if summary:=os.environ.get('GITHUB_STEP_SUMMARY'):
        with open(summary,'a',encoding='utf-8') as stream:stream.write(f'## {PRODUCT} {version}\n\n{latest["html_url"]}\n\nVerified source: `{commit}`\n')


def main() -> None:
    parser=argparse.ArgumentParser()
    parser.add_argument('--input',type=Path,required=True)
    parser.add_argument('--dist',type=Path,required=True)
    parser.add_argument('--version',required=True)
    parser.add_argument('--commit',required=True)
    parser.add_argument('--publish',action='store_true')
    args=parser.parse_args()
    manifest=assemble(args.input.resolve(),args.dist.resolve(),args.version,args.commit)
    print(f'Verified {len(manifest["assets"])} payloads for all six target platforms')
    if args.publish:publish(args.dist.resolve(),args.version,args.commit)

if __name__=='__main__':main()
