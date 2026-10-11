#!/usr/bin/env python3
"""Live source-verified Go/Rust PDF adapter differential; no golden manufacture.

Run inside the assigned build slot. All raw process observations survive failure.
Fake external tools test adapter contracts, not real PDF semantics.
"""
import argparse

import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import signal
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'scripts/rust-port'))
from history_live import source_manifest

TOOLS = ('PDFInfo', 'PDFSeparate', 'PDFUnite', 'QPDF')
NAMES = ('pdfinfo', 'pdfseparate', 'pdfunite', 'qpdf')


def corpus():
    cases = []
    def add(id, operation, **kw):
        cases.append(dict(id=id, operation=operation, **kw))
    for i, pages in enumerate(('', ' ', '5,1-3', '1', ' 1 , 2 ', '+1,+3', '1-+3', '0', '-1', '3-1', '1,1', '1-3,2', '1--2', '1,,2', 'a', '1.0', '1\x00', '1,\u0085', '9223372036854775808', '1-0', '01,2')):
        add(f'parse-{i:02}', 'parse', pages=pages)
    for layer in ('tools', 'api'):
        base = dict(layer=layer, input='a.pdf', inputs=['a.pdf', 'b.pdf'], output='out/result.pdf')
        def op(id, kind, **kw): add(f'{layer}-{id}', kind, **(base | kw))
        op('split', 'split', pages='3,1', output='out/parts')
        op('split-range', 'split', pages='1-3', output='out/parts')
        op('split-bad-selector', 'split', pages='1,1', output='out/parts')
        op('split-outside', 'split', pages='4', output='out/parts')
        op('split-missing-input-precedence', 'split', input='missing.pdf', pages='', output='out/parts')
        op('split-missing-tool-precedence', 'split', pages='', missing='PDFInfo', output='out/parts')
        op('split-bad-count', 'split', pages='1', info='Pages: 0\nPages: invalid\n', output='out/parts')
        op('split-count-recovery', 'split', pages='1', info='Pages: 0\n Pages : +4\n', output='out/parts')
        op('split-separate-failure', 'split', pages='1', mode='error', tool='pdfseparate', output='out/parts')
        op('split-absent-page', 'split', pages='1', mode='no-output', tool='pdfseparate', output='out/parts')
        op('split-unite-failure', 'split', pages='1', mode='quiet-error', tool='pdfunite', output='out/parts')
        op('split-output-file', 'split', pages='1', output='a.pdf')
        op('merge', 'merge')
        op('merge-order', 'merge', inputs=['b.pdf', 'a.pdf'])
        op('merge-bare-output', 'merge', output='merged.pdf')
        op('merge-clean-paths', 'merge', inputs=['sub/../a.pdf', './b.pdf'], output='out/../merged.pdf')
        op('merge-input-count', 'merge', inputs=['missing.pdf'], missing='PDFUnite')
        op('merge-missing-input', 'merge', inputs=['missing.pdf', 'b.pdf'])
        op('merge-directory-input', 'merge', inputs=['sub', 'b.pdf'])
        op('merge-missing-tool', 'merge', missing='PDFUnite')
        op('merge-unconfigured-tool', 'merge', empty='PDFUnite')
        op('merge-missing-output', 'merge', mode='no-output', tool='pdfunite')
        op('merge-tool-error', 'merge', mode='error', tool='pdfunite')
        op('merge-precancelled', 'merge', cancel=True)
        op('merge-cancel-running', 'merge', mode='cancel', tool='pdfunite', cancel_marker='ready')
        for degrees in (-270, -180, -90, 90, 180, 270, 0, 45, 360):
            op(f'rotate-{degrees}', 'rotate', degrees=degrees, pages='')
        op('rotate-pages', 'rotate', degrees=90, pages=' 3, +1 ')
        op('rotate-whitespace', 'rotate', degrees=90, pages=' ')
        op('rotate-duplicate', 'rotate', degrees=90, pages='1-2,2')
        op('rotate-missing-precedence', 'rotate', degrees=0, input='missing.pdf')
        op('rotate-tool-missing', 'rotate', degrees=90, missing='QPDF')
        op('rotate-empty-tool', 'rotate', degrees=90, empty='QPDF')
        op('rotate-quiet-error', 'rotate', degrees=90, mode='quiet-error', tool='qpdf')
        op('rotate-error', 'rotate', degrees=90, mode='error', tool='qpdf')
        op('rotate-no-output', 'rotate', degrees=90, mode='no-output', tool='qpdf')
        op('rotate-existing', 'rotate', degrees=90, existing=True)
        op('rotate-cancel-running', 'rotate', degrees=90, mode='cancel', tool='qpdf', cancel_marker='ready')
    assert len({c['id'] for c in cases}) == len(cases) and cases
    return cases


def digest(path): return hashlib.sha256(path.read_bytes()).hexdigest()


def normalized(value, root):
    # Only isolated case roots and random MkdirTemp basenames are variable.
    # Output bytes, ordering, status, errors and ordinary paths stay untouched.
    raw = json.dumps(value, ensure_ascii=False).replace(str(root), '<ROOT>')
    raw = re.sub(r'<ROOT>/tmp/symingest-pdf-split-[^/\\" ]+', '<ROOT>/tmp/symingest-pdf-split-<TEMP>', raw)
    return json.loads(raw)


def snapshot(root):
    result = {}
    for path in sorted(root.rglob('*')):
        relative = path.relative_to(root).as_posix()
        if relative in ('trace.jsonl', 'ready'): continue
        mode = path.lstat().st_mode & 0o777 if os.name != 'nt' else None
        result[relative] = {'type':'directory' if path.is_dir() else 'file', 'mode':mode}
        if path.is_file(): result[relative]['sha256'] = digest(path)
    return result


def compare(expected, actual):
    if type(expected) is not type(actual): raise AssertionError('JSON type mismatch')
    if isinstance(expected, dict):
        if expected.keys() != actual.keys(): raise AssertionError('JSON fields mismatch')
        for key in expected: compare(expected[key], actual[key])
    elif isinstance(expected, list):
        if len(expected) != len(actual): raise AssertionError('JSON list length mismatch')
        for left, right in zip(expected, actual): compare(left, right)
    elif expected != actual: raise AssertionError(f'value mismatch: {expected!r} != {actual!r}')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--runtime-root', required=True, type=Path)
    parser.add_argument('--build-launcher', type=Path)
    args = parser.parse_args()
    output, runtime = args.output.resolve(), args.runtime_root.resolve()
    output.mkdir(parents=True, exist_ok=False)
    runtime.mkdir(parents=True, exist_ok=False)
    env = {k:v for k,v in os.environ.items() if k in {'PATH','SystemRoot','WINDIR','PATHEXT','DEVELOPER_DIR','SDKROOT','CARGO_HOME','RUSTUP_HOME','CARGO_TARGET_DIR','GOCACHE','GOMODCACHE'}}
    home = Path.home()
    env.setdefault('CARGO_HOME', str(home / '.cargo'))
    env.setdefault('RUSTUP_HOME', str(home / '.rustup'))
    env.setdefault('GOCACHE', str(home / 'Library/Caches/go-build'))
    env.setdefault('GOMODCACHE', str(home / 'go/pkg/mod'))
    env.setdefault('CARGO_TARGET_DIR', str(runtime.parent / 'target'))
    for key in ('HOME','USERPROFILE','XDG_CONFIG_HOME','XDG_DATA_HOME','XDG_STATE_HOME','XDG_CACHE_HOME','TMPDIR','TMP','TEMP','GOTMPDIR'):
        path = runtime / 'build-env' / key
        path.mkdir(parents=True, mode=0o700)
        env[key] = str(path)
    env.update(CARGO_BUILD_JOBS='2', GOMAXPROCS='2', GOFLAGS='-mod=readonly -p=2', GOTOOLCHAIN='go1.26.9', GOWORK='off', GOENV='off', CGO_ENABLED='0', TZ='UTC', LC_ALL='C', PYTHONDONTWRITEBYTECODE='1')
    report = {'passed':False, 'root':str(ROOT), 'head':subprocess.check_output(['git','rev-parse','HEAD'],cwd=ROOT,text=True).strip(), 'dirty':subprocess.check_output(['git','status','--porcelain=v1'],cwd=ROOT,text=True), 'native_os':platform.system(), 'native_arch':platform.machine(), 'sources_before':source_manifest(), 'commands':[], 'cases':[]}
    def run(command, label, cwd=ROOT, variables=env, stdin=None, build=False):
        original = command
        if build and args.build_launcher:
            subprocess.run([str(args.build_launcher), '--status'], check=True)
            # Launcher runs with the operator's storage configuration; the actual
            # compiler/test process receives only the isolated environment below.
            command = [str(args.build_launcher), 'env', '-i', *[f'{k}={v}' for k,v in variables.items()], *command]
            variables = os.environ.copy()
        record = {'argv':original, 'cwd':str(cwd), 'exit':None, 'timed_out':False, 'label':label}
        report['commands'].append(record)
        with (output / f'{label}.stdout').open('xb') as out, (output / f'{label}.stderr').open('xb') as err:
            process = subprocess.Popen(command,cwd=cwd,env=variables,stdin=subprocess.PIPE if stdin is not None else subprocess.DEVNULL,stdout=out,stderr=err,start_new_session=os.name!='nt')
            try:
                process.communicate(stdin,timeout=900 if build else 20)
            except subprocess.TimeoutExpired:
                record['timed_out'] = True
                if os.name != 'nt': os.killpg(process.pid, signal.SIGKILL)
                else: process.kill()
                process.wait(timeout=5)
                raise
            finally: record['exit']=process.returncode
        if record['exit']: raise RuntimeError(f'{label}: exit {record["exit"]}; see raw logs')
        return (output / f'{label}.stdout').read_bytes()
    try:
        suffix = '.exe' if os.name == 'nt' else ''
        oracle = runtime / ('pdfops-go'+suffix)
        assert run(['go','version'],'go-version',build=True).startswith(b'go version go1.26.9 ')
        assert run(['rustc','--version'],'rust-version',build=True).startswith(b'rustc 1.98.0 ')
        metadata = json.loads(run(['cargo','metadata','--manifest-path',str(ROOT/'Cargo.toml'),'--no-deps','--format-version','1'],'cargo-metadata',build=True))
        assert Path(metadata['workspace_root']).resolve() == ROOT
        package = next(p for p in metadata['packages'] if p['name']=='symdesk-render')
        assert Path(package['manifest_path']).resolve() == ROOT/'crates/symdesk-render/Cargo.toml'
        assert all(Path(t['src_path']).resolve().is_relative_to(ROOT/'crates/symdesk-render') for t in package['targets'])
        run(['go','build','-o',str(oracle),'./internal/ingest/testdata/portpdfops'],'build-go',build=True)
        run(['cargo','build','--manifest-path',str(ROOT/'Cargo.toml'),'--locked','-p','symdesk-render','--examples'],'build-rust',build=True)
        identity = json.loads(run([str(oracle),'--identity'],'oracle-identity'))
        report['oracle'] = identity
        assert identity['compiler'] == 'go1.26.9'
        binary_root = Path(metadata['target_directory']) / 'debug/examples'
        candidate = binary_root / ('pdfops_probe'+suffix)
        stub = binary_root / ('pdf_tool_stub'+suffix)
        report['binaries'] = {str(p):digest(p) for p in (oracle,candidate,stub)}
        bin_dir = runtime / 'tools'
        bin_dir.mkdir()
        for name in NAMES: shutil.copy2(stub, bin_dir/(name+suffix))
        observations = []
        for case in corpus():
            observations = []
            for language,binary in (('go',oracle),('rust',candidate)):
                world = runtime / case['id'] / language
                world.mkdir(parents=True, mode=0o700)
                (world/'sub').mkdir(mode=0o700)
                for name in ('a.pdf','b.pdf'):
                    (world/name).write_bytes(f'%PDF-fixture-{name}\n'.encode()); (world/name).chmod(0o600)
                for key in ('home','tmp','config','data','cache','state'): (world/key).mkdir(mode=0o700)
                if case.get('existing'):
                    (world/'out').mkdir(mode=0o755)
                    (world/'out/result.pdf').write_text('previous'); (world/'out/result.pdf').chmod(0o640)
                variables = {k:v for k,v in env.items() if k in {'SystemRoot','WINDIR','PATHEXT','TZ','LC_ALL'}}
                variables.update(PATH=str(bin_dir),HOME=str(world/'home'),USERPROFILE=str(world/'home'),TMPDIR=str(world/'tmp'),TMP=str(world/'tmp'),TEMP=str(world/'tmp'),XDG_CONFIG_HOME=str(world/'config'),XDG_DATA_HOME=str(world/'data'),XDG_CACHE_HOME=str(world/'cache'),XDG_STATE_HOME=str(world/'state'))
                variables.update(PDF_STUB_MODE=case.get('mode',''),PDF_STUB_TOOL=case.get('tool',''),PDF_STUB_INFO=case.get('info','Title: fixture\nPages: 4\n'))
                request = {k:v for k,v in case.items() if k in {'operation','layer','input','inputs','output','pages','degrees','cancel','cancel_marker'}}
                paths: dict[str, str] = dict(zip(TOOLS,NAMES))
                request['tools'] = paths
                if case.get('missing'): request['tools'][case['missing']]='missing-tool'
                if case.get('empty'): request['tools'][case['empty']]=''
                before = snapshot(world)
                raw = run([str(binary)],f'{case["id"]}-{language}',cwd=world,variables=variables,stdin=json.dumps(request).encode())
                assert not (output/f'{case["id"]}-{language}.stderr').read_bytes(), 'unexpected driver stderr'
                response = json.loads(raw)
                trace = [json.loads(line) for line in (world/'trace.jsonl').read_bytes().split(b'\n') if line] if (world/'trace.jsonl').exists() else []
                after = snapshot(world)
                for name in ('a.pdf','b.pdf'): assert before[name] == after[name], 'input changed'
                assert not list((world/'tmp').iterdir()), 'workspace leaked'
                if (world/'ready').exists() and os.name != 'nt':
                    pid = int((world/'ready').read_text())
                    try: os.kill(pid,0)
                    except ProcessLookupError: pass
                    else: raise AssertionError('cancelled tool still alive')
                observations.append(normalized({'response':response,'trace':trace,'before':before,'after':after},world))
            (output/f'{case["id"]}.json').write_text(json.dumps(observations,indent=2)+'\n')
            try:
                compare(*observations)
                record={'id':case['id'],'passed':True}
            except AssertionError as error:
                record={'id':case['id'],'passed':False,'error':str(error)}
            report['cases'].append(record)
            print(f'{case["id"]}: {record["passed"]}',flush=True)
        assert [c['id'] for c in report['cases']] == [c['id'] for c in corpus()]
        assert all(c['passed'] is True for c in report['cases']), 'PDF differential mismatch'
        # Feed mutations into the same recursive comparator after its valid case.
        good=observations[0]
        for bad in (dict(good,extra=True),dict(good,response={'error':'tampered','result':None}),dict(good,trace=[])):
            try: compare(good,bad)
            except AssertionError: pass
            else: raise AssertionError('mutation accepted')
        compare({'success':True,'result':None},{'success':True,'result':None})
        for bad in ({'success':1,'result':None},{'success':False,'result':None},{'success':True}):
            try: compare({'success':True,'result':None},bad)
            except AssertionError: pass
            else: raise AssertionError('type/presence mutation accepted')
        report['mutation_controls']=6
        tests = run(['go','test','-count=1','-v','./internal/ingest/internal/pdfops'],'go-pdfops-tests',build=True)
        report['go_adapter_tests'] = len(re.findall(rb'^--- PASS: Test', tests, re.M))
        assert report['go_adapter_tests'] > 0
        facade = run(['go','test','-count=1','-v','./internal/ingest','-run','^Test(MergePDFs|RotatePDF)RealPath$'],'go-facade-tests',build=True)
        assert b'--- PASS: TestMergePDFsRealPath' in facade and b'--- PASS: TestRotatePDFRealPath' in facade
        report['go_facade_tests'] = 2
        run(['go','run','./scripts/rust-port/cmd/renderirgen','--check'],'go-render-ir',build=True)
        html = run(['go','test','-count=1','-v','./internal/export','-run','^TestPortRenderHTMLFixture$'],'go-render-html',build=True)
        assert b'--- PASS: TestPortRenderHTMLFixture' in html
        cargo = ['cargo']
        manifest = ['--manifest-path',str(ROOT/'Cargo.toml')]
        run(cargo+['nextest','run',*manifest,'--locked','-p','symdesk-render','--test-threads','2'],'nextest',build=True)
        summary = (output/'nextest.stderr').read_text()
        matched = re.search(r'Summary .*?(\d+) tests run: (\d+) passed, (\d+) skipped',summary)
        assert matched and int(matched[1]) > 0 and matched[1] == matched[2] and matched[3] == '0', summary
        report['rust_tests'] = int(matched[1])
        run(cargo+['clippy',*manifest,'--locked','-p','symdesk-render','--all-targets','--all-features','--','-D','warnings'],'clippy',build=True)
        run(cargo+['fmt',*manifest,'--all','--check'],'fmt',build=True)
        assert report['binaries']=={p:digest(Path(p)) for p in report['binaries']}
        report['passed']=True
        print(f'PASS {len(report["cases"])} real Go/Rust pairs',flush=True)
    finally:
        report['sources_after']=source_manifest()
        report['sources_unchanged']=report['sources_before']==report['sources_after']
        report['passed']=report['passed'] and report['sources_unchanged']
        (output/'report.json').write_text(json.dumps(report,indent=2)+'\n')
        if not report['sources_unchanged']: raise RuntimeError('gate inputs changed during execution')
    return 0

if __name__ == '__main__': raise SystemExit(main())
