"""核对 GoReleaser 五平台产物、安装器命名和软件源校验和的一致性。"""
import hashlib
import json
from pathlib import Path
import re
import sys
import tarfile
import zipfile


def main():
    dist = Path(sys.argv[1] if len(sys.argv) > 1 else 'dist')
    artifacts = json.loads((dist / 'artifacts.json').read_text(encoding='utf-8'))
    checksums = {}
    for line in (dist / 'checksums.txt').read_text().splitlines():
        digest, name = line.split('  ', 1)
        assert name not in checksums, f'重复校验条目: {name}'
        checksums[name] = digest
    targets = ['linux-amd64', 'linux-arm64', 'darwin-amd64', 'darwin-arm64', 'windows-amd64']
    for target in targets:
        name = 'relay-' + target + ('.zip' if target.startswith('windows') else '.tar.gz')
        artifact = next(a for a in artifacts if a['name'] == name)
        path = Path(artifact['path'])
        assert hashlib.sha256(path.read_bytes()).hexdigest() == checksums[name], f'校验不一致: {name}'
        if name.endswith('.zip'):
            with zipfile.ZipFile(path) as archive:
                assert 'relay.exe' in archive.namelist()
        else:
            with tarfile.open(path) as archive:
                assert 'relay' in archive.getnames()
    windows = next(a for a in artifacts if a['name'] == 'relay-windows-amd64.exe')
    assert hashlib.sha256(Path(windows['path']).read_bytes()).hexdigest() == checksums[windows['name']]
    scoop = json.loads((dist / 'scoop/relay.json').read_text(encoding='utf-8'))
    assert scoop['architecture']['64bit']['hash'] == checksums['relay-windows-amd64.zip']
    assert scoop['architecture']['64bit']['url'].endswith('/relay-windows-amd64.zip')
    assert 'skill install' in '\n'.join(scoop['post_install'])
    formula = (dist / 'homebrew/Formula/relay.rb').read_text(encoding='utf-8')
    pairs = re.findall(r'url "[^"\n]+/(relay-[^"/]+)"\s+sha256 "([0-9a-f]+)"', formula)
    assert len(pairs) == 4, 'Homebrew 缺少平台'
    for name, digest in pairs:
        assert checksums[name] == digest, f'Homebrew 校验不一致: {name}'
    assert '"skill", "install"' in formula
    print('五平台归档、Windows 裸二进制、SHA-256、Homebrew 与 Scoop 清单全部一致。')


if __name__ == '__main__':
    main()
