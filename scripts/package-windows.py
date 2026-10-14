#!/usr/bin/env python3
"""Package Windows builds with the corresponding source and dependency licenses."""
import argparse
import hashlib
import json
import os
import pathlib
import re
import shutil
import struct
import subprocess
import tarfile
import tempfile
import urllib.request
import zipfile

TAGS = 'desktop,production,with_quic,with_utls,with_gvisor'
AGENT_TAGS = 'agent,with_quic,with_utls,with_gvisor'
NOTICE_NAME = re.compile(r'^(license|licence|copying|copyright|notice|patents)(?:[._-].*)?$', re.I)


def run(*args, env=None):
    return subprocess.check_output(args, text=True, env=env).strip()


def digest(file):
    checksum = hashlib.sha256()
    with file.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024*1024), b''):
            checksum.update(chunk)
    return checksum.hexdigest()


def copy(file, destination):
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(file, destination)


def modules():
    raw = run('go', 'list', '-m', '-json', 'all')
    decoder = json.JSONDecoder()
    result = {}
    while raw.strip():
        module, end = decoder.raw_decode(raw.lstrip())
        raw = raw.lstrip()[end:]
        result[module['Path']] = module
    return result


def used_modules():
    result = set()
    for system, arch, tags in [('windows', 'amd64', TAGS), ('windows', 'arm64', TAGS),
                               ('linux', 'amd64', AGENT_TAGS), ('linux', 'arm64', AGENT_TAGS),
                               ('darwin', 'amd64', AGENT_TAGS), ('darwin', 'arm64', AGENT_TAGS)]:
        env = dict(os.environ, GOOS=system, GOARCH=arch, CGO_ENABLED='0')
        result.update(run('go', 'list', '-deps', '-test', '-tags', tags,
                          '-f', '{{if .Module}}{{.Module.Path}}{{end}}', './...', env=env).split())
    result.discard('appforce')
    return result


def validate_binary(file, architecture, commit, manifest, module_map):
    data = file.read_bytes()
    assert data[:2] == b'MZ', f'{file}: not a Windows executable'
    offset = struct.unpack_from('<I', data, 0x3c)[0]
    assert data[offset:offset+4] == b'PE\0\0'
    assert struct.unpack_from('<H', data, offset+4)[0] == {'amd64': 0x8664, 'arm64': 0xaa64}[architecture]
    assert struct.unpack_from('<H', data, offset+24)[0] == 0x20b
    assert struct.unpack_from('<H', data, offset+24+68)[0] == 2, 'GUI subsystem required'
    assert b'requestedExecutionLevel level="requireAdministrator"' in data
    assert b'>permonitorv2,system<' in data
    info = run('go', 'version', '-m', str(file))
    for setting in [f'GOARCH={architecture}', 'GOOS=windows', f'-tags={TAGS}', f'vcs.revision={commit}']:
        assert setting in info, f'{file}: missing {setting}'
    assert manifest.read_bytes() in data
    for agent in json.loads(manifest.read_text()):
        assert (manifest.parent/agent['file']).read_bytes() in data, agent['file']
    for file in pathlib.Path('internal/web/dist').rglob('*'):
        if file.is_file():
            assert file.read_bytes() in data, f'Frontend asset not embedded: {file}'
    wintun = pathlib.Path(module_map['github.com/sagernet/sing-tun']['Dir'])/'internal/wintun'
    assert (wintun/architecture/'wintun.dll').read_bytes() in data, 'Wintun not embedded'
    print(f'Validated Windows {architecture}: {len(data)/1048576:.1f} MiB', flush=True)


def collect_licenses(destination, used, module_map):
    inventory = []
    for name in sorted(used):
        module = module_map[name]
        directory = pathlib.Path(module['Dir'])
        found = []
        for file in directory.rglob('*'):
            if file.is_file() and NOTICE_NAME.match(file.name):
                relative = file.relative_to(directory)
                copy(file, destination/'go'/(name+'@'+module['Version'])/relative)
                found.append(str(relative))
        assert found, f'Missing dependency license: {name}'
        inventory.append({'module': name, 'version': module['Version'], 'sum': module.get('Sum', ''), 'license_files': found})
    (destination/'go-modules.json').write_text(json.dumps(inventory, indent=2)+'\n')
    for name in ('LICENSE', 'PATENTS'):
        file = pathlib.Path(run('go', 'env', 'GOROOT'))/name
        if file.is_file():
            copy(file, destination/'go-runtime'/name)
    frontend = []
    npm_root = pathlib.Path('frontend/node_modules')
    for package in sorted(npm_root.rglob('package.json')):
        parts = package.relative_to(npm_root).parts
        if not (len(parts) == 2 or len(parts) == 3 and parts[0].startswith('@')):
            continue
        metadata = json.loads(package.read_text())
        found = []
        package_out = destination/'frontend'/(metadata['name']+'@'+metadata['version'])
        for file in package.parent.rglob('*'):
            if file.is_file() and NOTICE_NAME.match(file.name):
                relative = file.relative_to(package.parent)
                copy(file, package_out/relative)
                found.append(str(relative))
        if not found and metadata['name'].startswith(('@esbuild/', '@rollup/')):
            parent = npm_root/('esbuild' if metadata['name'].startswith('@esbuild/') else 'rollup')
            for file in parent.glob('LICENSE*'):
                copy(file, package_out/file.name)
                found.append('parent-package/'+file.name)
        assert found, f'Missing frontend license: {metadata["name"]}'
        frontend.append({'package': metadata['name'], 'version': metadata['version'],
                         'license': metadata.get('license'), 'license_files': found})
    (destination/'frontend-packages.json').write_text(json.dumps(frontend, indent=2)+'\n')
    wintun = pathlib.Path(module_map['github.com/sagernet/sing-tun']['Dir'])/'internal/wintun'
    version = re.search(r'DLL version:\s*([0-9]+\.[0-9]+\.[0-9]+)', (wintun/'README.md').read_text()).group(1)
    with urllib.request.urlopen(f'https://www.wintun.net/builds/wintun-{version}.zip', timeout=60) as response:
        data = response.read(16*1024*1024+1)
    assert len(data) <= 16*1024*1024
    with tempfile.TemporaryFile() as temporary:
        temporary.write(data); temporary.seek(0)
        with zipfile.ZipFile(temporary) as archive:
            for arch in ('amd64', 'arm64'):
                assert archive.read(f'wintun/bin/{arch}/wintun.dll') == (wintun/arch/'wintun.dll').read_bytes()
            (destination/'Wintun').mkdir(exist_ok=True)
            (destination/'Wintun/LICENSE.txt').write_bytes(archive.read('wintun/LICENSE.txt'))
    copy(wintun/'README.md', destination/'Wintun/UPSTREAM.md')
    print(f'Licenses collected: {len(inventory)} Go modules, {len(frontend)} frontend packages', flush=True)


def prepare_source(source, output, used, licenses, commit, tag):
    source.mkdir(parents=True)
    with tempfile.TemporaryFile() as temporary:
        subprocess.run(['git', 'archive', commit], stdout=temporary, check=True)
        temporary.seek(0)
        with tarfile.open(fileobj=temporary) as archive:
            # This archive is created from the already trusted local Git tree.
            # Reject paths outside the staging directory before extracting.
            for member in archive.getmembers():
                assert not pathlib.PurePosixPath(member.name).is_absolute()
                assert '..' not in pathlib.PurePosixPath(member.name).parts
            archive.extractall(source)
    subprocess.run(['go', 'mod', 'vendor', '-o', str(source/'vendor')], check=True)
    # Exclude unused optional features such as the large Cronet engine; keep
    # all modules required by both Windows targets, embedded Agents and tests.
    paths = []
    for line in (source/'vendor/modules.txt').read_text().splitlines():
        if line.startswith('# ') and len(line.split()) >= 3 and line.split()[2].startswith('v'):
            paths.append(line.split()[1])
    for module in sorted(set(paths), key=lambda value: -len(value)):
        if module not in used and not any(name.startswith(module+'/') for name in used):
            directory = source/'vendor'/module
            if directory.exists():
                shutil.rmtree(directory)
    shutil.copytree(licenses, source/'THIRD_PARTY_LICENSES')
    shutil.copytree('frontend/node_modules', source/'frontend/node_modules', symlinks=True)
    (source/'RELEASE-BUILD.md').write_text(f'''# AppForce {tag} corresponding source\n\nSource commit: {commit}\n\nIncludes repository source, build scripts, lockfiles, required Go vendor source, frontend dependency source and licenses. Optional features not used by the release are excluded from vendor. Their versions remain pinned in go.mod/go.sum.\n\nRequires Go >= 1.25.5, Node.js >= 22.12 and Python >= 3.9 for release packaging. Run make windows on macOS/Linux, or scripts/build-windows.ps1 on Windows. npm ci reinstalls build dependencies for the target host.\n\nBuilds regenerate Windows resources and four embedded Agents. See README.md and .github/workflows/release-windows.yml for the complete pipeline.\n''')


def package(tag, output):
    assert re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+', tag), 'Expected version tag vX.Y.Z'
    version = tag[1:]
    assert re.search(r'const Version = "([^"]+)"', pathlib.Path('internal/model/model.go').read_text()).group(1) == version
    assert json.loads(pathlib.Path('frontend/package.json').read_text())['version'] == version
    assert not run('git', 'diff', '--name-only'), 'Tracked source must be clean'
    assert not run('git', 'diff', '--cached', '--name-only'), 'Staging area must be clean'
    commit = run('git', 'rev-parse', 'HEAD')
    assert not output.exists(), f'Refusing to overwrite an existing output directory: {output}'
    output.mkdir(parents=True)
    assets = output/'assets'; assets.mkdir()
    module_map = modules()
    manifest = pathlib.Path('internal/agentbundle/payloads/manifest.json')
    for file, architecture in [('AppForce.exe', 'amd64'), ('AppForce-arm64.exe', 'arm64')]:
        validate_binary(pathlib.Path('bin')/file, architecture, commit, manifest, module_map)
    used = used_modules()
    licenses = output/'THIRD_PARTY_LICENSES'; licenses.mkdir()
    collect_licenses(licenses, used, module_map)
    build_info = {'version': version, 'source_commit': commit, 'go': run('go', 'version'),
                  'node': run('node', '--version'), 'tags': TAGS.split(','), 'target_os': 'windows',
                  'architectures': ['amd64', 'arm64'], 'embedded_agents': json.loads(manifest.read_text()),
                  'not_tested': ['Windows GUI/tray runtime', 'Windows login task scheduler', 'Windows administrator TUN/networking']}
    (output/'BUILD-INFO.json').write_text(json.dumps(build_info, indent=2)+'\n')
    for arch, binary in [('x64', 'AppForce.exe'), ('arm64', 'AppForce-arm64.exe')]:
        stage = output/f'AppForce-{tag}-windows-{arch}'; stage.mkdir()
        copy(pathlib.Path('bin')/binary, stage/'AppForce.exe')
        for name in ('LICENSE', 'NOTICE.md'):
            copy(pathlib.Path(name), stage/name)
        copy(output/'BUILD-INFO.json', stage/'BUILD-INFO.json')
        shutil.copytree(licenses, stage/'THIRD_PARTY_LICENSES')
        (stage/'README.txt').write_text(f'''AppForce {tag} · Windows {arch}\n\n解压到固定目录，双击 AppForce.exe 并允许管理员授权。导入订阅、选择节点、添加程序，然后启动代理。关闭窗口后留在托盘，通过托盘退出应用。\n\n支持 Windows 10/11，需要 Microsoft WebView2 Runtime。内置代理核心、Wintun 和 Linux/macOS Agent，无需额外 DLL。Windows 包未做代码签名。\n\n配置与历史位于 %APPDATA%\\AppForce，升级时保留。随 Windows 登录启动默认关闭，可在代理设置开启；移动 EXE 后重新关闭、开启以更新路径。\n\n本发布通过宿主机测试和 Windows 交叉编译，Windows 真机托盘、自启动及管理员 TUN 网络行为仍需验证。\n\n源码提交：{commit}\n许可及对应源码见同一发布页：\nhttps://github.com/wuhy80/app_force/releases/tag/{tag}\n''', encoding='utf-8-sig')
        archive_file = assets/f'{stage.name}.zip'
        with zipfile.ZipFile(archive_file, 'w', compression=zipfile.ZIP_DEFLATED, compresslevel=6) as archive:
            for file in sorted(stage.rglob('*')):
                if file.is_file():
                    archive.write(file, file.relative_to(stage))
        with zipfile.ZipFile(archive_file) as archive:
            assert archive.testzip() is None
            assert hashlib.sha256(archive.read('AppForce.exe')).hexdigest() == digest(pathlib.Path('bin')/binary)
        print(f'Packaged {archive_file.name}', flush=True)
    source = output/'source'
    prepare_source(source, output, used, licenses, commit, tag)
    with tarfile.open(assets/f'AppForce-{tag}-source.tar.gz', 'w:gz', compresslevel=6) as archive:
        archive.add(source, arcname=f'AppForce-{tag}-source')
    (assets/'SHA256SUMS.txt').write_text(''.join(f'{digest(file)}  {file.name}\n' for file in sorted(assets.iterdir())))
    (output/'release-notes.md').write_text(f'''AppForce {tag} Windows 桌面版，支持 Windows 10 / 11 的 x64 与 ARM64。\n\n## 下载与使用\n\n- `AppForce-{tag}-windows-x64.zip`：Intel / AMD Windows 电脑。\n- `AppForce-{tag}-windows-arm64.zip`：ARM64 Windows 电脑。\n- `AppForce-{tag}-source.tar.gz`：对应源码、构建脚本、Go 与前端依赖源码及许可。\n- `SHA256SUMS.txt`：下载文件的 SHA-256 校验值。\n\n解压后双击 `AppForce.exe`，允许管理员授权。需要 Microsoft WebView2 Runtime；Windows 11 通常已安装。内置界面、代理核心、Wintun 和四个平台的 Linux/macOS Agent，无需另放 DLL。Windows 包未做代码签名。\n\n导入订阅 → 选择节点 → 添加程序 → 启动代理。关闭窗口后留在托盘，通过托盘退出。配置与历史位于 `%APPDATA%\\AppForce`。\n\n## 功能\n\n- 订阅导入更新、常见节点协议与真实节点测速。\n- 按程序路径选择代理范围与独立节点，透明代理及本地 SOCKS / HTTP 模式。\n- 真实连接、日志、实时速率与持久化流量，今天按小时展示。\n- 托盘启停、关闭窗口后常驻；可选 Windows 登录启动，默认关闭。\n- SSH 自动部署与管理 Linux/macOS 设备。\n\n## 验证范围\n\n前端类型检查与构建、Linux 上完整 Go race 测试及静态检查、Windows x64 / ARM64 桌面及测试代码编译通过。Windows 测试代码未在 Windows 机器执行。已验证 EXE 架构、GUI / 管理员 / DPI 清单、内置资源，以及四个 Agent 的架构和 SHA-256。\n\nWindows 真机托盘、登录任务计划、管理员 TUN、DNS / IPv6、换网与异常恢复仍需验证。遇到透明代理影响其他程序时，可先使用本地代理模式。\n\n源码提交：`{commit}`。AppForce 按 GPL-3.0-or-later 分发，包内保留 LICENSE、NOTICE.md 和第三方许可证。\n''')
    print(f'All release assets prepared in {assets}', flush=True)


def verify_remote(remote_file, output):
    release = json.loads(remote_file.read_text())
    remote = {asset['name']: asset for asset in release['assets']}
    files = sorted((output/'assets').iterdir())
    assert set(remote) == {file.name for file in files}, 'Remote asset list does not match local files'
    for file in files:
        asset = remote[file.name]
        assert asset['state'] == 'uploaded' and asset['size'] == file.stat().st_size, file.name
        assert asset.get('digest') == 'sha256:'+digest(file), f'Remote SHA-256 mismatch: {file.name}'
    print('Remote asset names, sizes and SHA-256 verified', flush=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version')
    parser.add_argument('--output', type=pathlib.Path, required=True)
    parser.add_argument('--verify-remote', type=pathlib.Path)
    options = parser.parse_args()
    if options.verify_remote:
        verify_remote(options.verify_remote, options.output)
    else:
        parser.error('--version is required') if not options.version else package(options.version, options.output)
