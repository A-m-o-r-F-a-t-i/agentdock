#!/usr/bin/env python3
"""Check display branding without renaming stable upgrade or protocol identities."""
from pathlib import Path
import re
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parents[2]
PRODUCT = 'AgentDock Workbench'

def text(path: str) -> str:
    return (ROOT/path).read_text(encoding='utf-8-sig')

def main() -> None:
    build = text('internal/buildinfo/buildinfo.go')
    version = re.search(r'const Version = "([^"]+)"',build).group(1)
    assert tuple(map(int,version.split('.'))) >= (1,1,7)
    assert f'const ProductName = "{PRODUCT}"' in build
    panel = ET.fromstring(text('desktop/windows/control-panel/AgentDock.ControlPanel.csproj'))
    for tag in ['Product','AssemblyTitle']:
        assert panel.findtext(f'.//{tag}') == PRODUCT, tag
    assert panel.findtext('.//Version') == version
    assert panel.findtext('.//AssemblyName') == 'agentdock-tray'
    assert f'Title="{PRODUCT}"' in text('desktop/windows/control-panel/ExecutionWindow.xaml')
    assert f'Text="{PRODUCT}"' in text('desktop/windows/control-panel/MainWindow.xaml')
    setup = text('packaging/windows/AgentDock.iss')
    assert f'AppName={PRODUCT}\n' in setup
    assert 'D6788C7A-4104-48D4-B5C3-F4858B5606EA' in setup
    assert 'DefaultDirName={localappdata}\\AgentDock' in setup
    assert 'AgentDockSetup-amd64' in setup and 'AgentDockSetup-arm64' in setup
    mac = text('packaging/macos/build-app.sh')
    for key in ['CFBundleName','CFBundleDisplayName']:
        assert re.search(rf'<key>{key}</key>\s*<string>{PRODUCT}</string>',mac), key
    assert 'BUNDLE_ID="com.uvwt.agentdock"' in mac
    assert 'APP_DIR="$OUTPUT_DIR/AgentDock.app"' in mac
    for path in ['internal/httpx/status_page.html','internal/httpx/oauth_authorize_page.html']:
        assert PRODUCT in text(path)
    for language in ['en','zh-Hans']:
        assert PRODUCT in text(f'desktop/macos/AgentDockApp/Resources/{language}.lproj/Localizable.strings')
    for path in ['README.md','README.zh-CN.md']:
        assert re.search(r'^# AgentDock Workbench\b',text(path),re.MULTILINE), path
    assert 'A-m-o-r-F-a-t-i/agentdock/releases' in text('scripts/install/install.sh')
    assert 'module github.com/uvwt/agentdock' in text('go.mod')
    print(f'{PRODUCT} {version}: display branding and stable upgrade identities verified')

if __name__ == '__main__':
    main()
