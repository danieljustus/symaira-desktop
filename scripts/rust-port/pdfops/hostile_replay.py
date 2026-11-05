#!/usr/bin/env python3
"""Go-oracle hostile PDF replay with REAL Poppler/qpdf (no stub tools).

Builds the Go oracle driver (internal/ingest/testdata/portpdfops, which calls
the production ingest facade) and the Rust probe (symdesk-render pdfops_probe),
generates the deterministic hostile corpus (hostile_inputs.py) plus valid
multi-page documents, then runs every operation (split/merge/rotate) with the
installed pdfinfo/pdfseparate/pdfunite/qpdf on both sides in disposable worlds.
Compared per case: response/error text, every filesystem side effect (whole
world incl. a sibling directory and the HOME/XDG/tmp dirs), and for each produced
PDF the real qpdf/pdfinfo view of it (page count, --check status, semantic JSON
without /ID, pdfinfo text) plus byte hashes (reported; required equal with the random /ID neutralized; raw equality is reported).
Resource bounds: wall clock, RLIMIT_FSIZE, delta size, response size.
Mutation controls prove the comparator and the real pipeline reject faults.
Inputs are generated, never oracle output; expectations come from the Go run.

Frozen mode (RUST-019, no Go needed afterwards):

* `--record [FILE]` / `--check`: run only the Go driver over the whole corpus with
  the real tools and write (or verify) the per-OS recording
  `testdata/port/render/pdfops-hostile-recording-<os>.json`: tool versions plus one
  normalized observation per case (`make render-pdfops-hostile-fixtures-generate`);
* `--replay FILE --rust-binary PROBE`: rebuild each world, run the Rust probe with the
  same real tools and compare with the recorded Go observation
  (`crates/symdesk-render/tests/pdfops_hostile_frozen.rs`). It starts no Go and needs
  the same tool versions the recording names.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import resource
import shutil
import signal
import subprocess
import sys
import tempfile
import zlib

sys.dont_write_bytecode = True
HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
sys.path.insert(0, str(HERE))
sys.path.insert(0, str(ROOT / 'scripts/rust-port'))
from hostile_inputs import document  # noqa: E402
from real_tools import pdf as make_pdf  # noqa: E402
from verify import compare, normalized  # noqa: E402

TOOLS = ('PDFInfo', 'PDFSeparate', 'PDFUnite', 'QPDF')
NAMES = ('pdfinfo', 'pdfseparate', 'pdfunite', 'qpdf')
RUN_TIMEOUT = 40
FSIZE_LIMIT = 64 * 1024 * 1024
DELTA_LIMIT = 16 * 1024 * 1024
HASH_LIMIT = 4 * 1024 * 1024
BOMB_BYTES = 96 * 1024 * 1024
HOSTILE = ('flate-bomb', 'huge-count', 'pages-loop', 'kids-cycle', 'huge-stream', 'truncated-stream',
           'missing-xref', 'malformed-xref', 'deep-pages', 'encrypted', 'garbage', 'empty')


def digest(data): return hashlib.sha256(data).hexdigest()


def inputs_for(directory):
    """name -> bytes of every named regular input (hostile + valid)."""
    files = {name: path.read_bytes() for name, path in
             ((p.stem, p) for p in sorted(directory.glob('*.pdf')))}
    # Decompression bomb: 96 MiB of zeros inside a valid FlateDecode page stream (~100 KiB on disk).
    packer, chunks = zlib.compressobj(9), []
    for _ in range(BOMB_BYTES // (1 << 20)): chunks.append(packer.compress(bytes(1 << 20)))
    chunks.append(packer.flush())
    packed = b''.join(chunks)
    assert len(packed) <= 128 * 1024, 'bomb fixture exceeds size bound'
    files['flate-bomb'] = document([
        b'<< /Type /Catalog /Pages 2 0 R >>', b'<< /Type /Pages /Count 1 /Kids [3 0 R] >>',
        b'<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 300] /Resources << >> /Contents 4 0 R >>',
        f'<< /Length {len(packed)} /Filter /FlateDecode >>\nstream\n'.encode() + packed + b'\nendstream'])
    files['good'] = make_pdf(['one', 'two', 'three', 'four'])
    files['b'] = make_pdf(['five', 'six'])
    files['garbage'] = b'this is not a pdf\x00\xff' * 64
    files['empty'] = b''
    return files


def corpus():
    cases = []

    def add(id, operation, **kw): cases.append(dict(id=id, operation=operation, **kw))
    all_inputs = ('good', 'b', 'valid', *HOSTILE)
    for name in all_inputs:
        for tag, spec in (('1', '1'), ('range', '1-2'), ('swap', '2,1'), ('dup', '1,1'), ('zero', '0'),
                          ('far', '99'), ('alpha', 'a'), ('empty', ''), ('huge', '1-9223372036854775807'),
                          ('space', ' 1 , 2 '), ('uni', '1,\u200b')):
            add(f'split-{name}-{tag}', 'split', input=f'{name}.pdf', pages=spec, output='out/parts')
        for tag, degrees, spec in (('90', 90, ''), ('90p1', 90, '1'), ('m90p2', -90, '2'), ('180p', 180, '1,2,1'),
                                   ('far', 90, '99'), ('45', 45, ''), ('360', 360, ''), ('0', 0, ''),
                                   ('alpha', 90, 'a'), ('neg', 90, '-1'), ('zero', 90, '0')):
            add(f'rotate-{name}-{tag}', 'rotate', input=f'{name}.pdf', degrees=degrees, pages=spec, output='out/rot.pdf')
    for degrees in (91, -360, 450, -45, 135, 2**31, 2**62, -2**62, 9223372036854775807):
        add(f'rotate-good-degrees{degrees}', 'rotate', input='good.pdf', degrees=degrees, pages='', output='out/rot.pdf')
    for spec in (',', '1,,2', '1-', '-', '1-+3', '+1', '1.0', '01', '3-1', '9223372036854775808', '1\x00'):
        add(f'rotate-good-spec{abs(hash(spec)) % 10**6 if False else digest(spec.encode())[:8]}', 'rotate',
            input='good.pdf', degrees=90, pages=spec, output='out/rot.pdf')
        add(f'split-good-spec{digest(spec.encode())[:8]}', 'split', input='good.pdf', pages=spec, output='out/parts')
    # merge: mixed valid / hostile inputs
    add('merge-good-b', 'merge', inputs=['good.pdf', 'b.pdf'], output='out/m.pdf')
    add('merge-b-good', 'merge', inputs=['b.pdf', 'good.pdf'], output='out/m.pdf')
    add('merge-good-good', 'merge', inputs=['good.pdf', 'good.pdf'], output='out/m.pdf')
    add('merge-three', 'merge', inputs=['good.pdf', 'b.pdf', 'valid.pdf'], output='out/m.pdf')
    add('merge-single', 'merge', inputs=['good.pdf'], output='out/m.pdf')
    add('merge-none', 'merge', inputs=[], output='out/m.pdf')
    for name in HOSTILE:
        add(f'merge-good-{name}', 'merge', inputs=['good.pdf', f'{name}.pdf'], output='out/m.pdf')
        add(f'merge-{name}-good', 'merge', inputs=[f'{name}.pdf', 'good.pdf'], output='out/m.pdf')
        add(f'merge-{name}-{name}', 'merge', inputs=[f'{name}.pdf', f'{name}.pdf'], output='out/m.pdf')
    add('merge-valid-hostile-all', 'merge', inputs=[f'{n}.pdf' for n in ('valid', *HOSTILE)], output='out/m.pdf')
    add('merge-missing', 'merge', inputs=['good.pdf', 'nothere.pdf'], output='out/m.pdf')
    add('merge-dir', 'merge', inputs=['good.pdf', 'adir'], output='out/m.pdf')
    add('merge-symlink-ok', 'merge', inputs=['good.pdf', 'link-good.pdf'], output='out/m.pdf')
    add('merge-symlink-hostile', 'merge', inputs=['good.pdf', 'link-kids-cycle.pdf'], output='out/m.pdf')
    add('merge-symlink-dangling', 'merge', inputs=['good.pdf', 'dangling.pdf'], output='out/m.pdf')
    # output-side traversal, aliasing, existing and unusable destinations
    for op, extra in (('merge', dict(inputs=['good.pdf', 'b.pdf'])),
                      ('rotate', dict(input='good.pdf', degrees=90, pages='')),
                      ('split', dict(input='good.pdf', pages='1,2'))):
        for tag, output in (('dotdot', 'out/../../outside/x.pdf'), ('existing-file', 'existing.pdf'),
                            ('existing-dir', 'adir'), ('under-file', 'good.pdf/x.pdf'),
                            ('via-symlink-dir', 'outlink/x.pdf'), ('dangling-link', 'dangling.pdf'),
                            ('alias-input', 'good.pdf'), ('alias-symlink', 'link-good.pdf'),
                            ('alias-hardlink', 'hard-good.pdf'), ('name-too-long', 'n' * 300 + '.pdf'),
                            ('trailing-slash', 'out/trail/'), ('dot', '.'), ('deep-new', 'a/b/c/d/x.pdf'),
                            ('abs-outside', '@OUTSIDE@/abs.pdf'), ('empty', '')):
            add(f'{op}-out-{tag}', op, output=output, **extra)
    cases.append(dict(id='split-spec-parts-existing', operation='split', input='good.pdf', pages='1,2,3',
                      output='parts', premake=['parts/part-002.pdf']))
    assert len({c['id'] for c in cases}) == len(cases), 'duplicate case id'
    return cases


def snapshot(world):
    result = {}
    for path in sorted(world.rglob('*')):
        relative = path.relative_to(world).as_posix()
        st = path.lstat()
        entry = {'mode': st.st_mode & 0o7777}
        if path.is_symlink():
            entry.update(type='symlink', target=os.readlink(path))
        elif path.is_dir():
            entry['type'] = 'directory'
        else:
            entry.update(type='file', size=st.st_size, nlink=st.st_nlink)
            if st.st_size <= HASH_LIMIT: entry['sha256'] = digest(path.read_bytes())
        result[relative] = entry
    return result


def delta(before, after):
    return {k: v for k, v in after.items() if before.get(k) != v}


class Harness:
    def __init__(self, out, runtime, paths, go, rust, inputs):
        self.out, self.runtime, self.paths, self.go, self.rust, self.inputs = out, runtime, paths, go, rust, inputs
        self.commands = 0
        self.maxrss = 0

    def tool_env(self, world):
        env = {'PATH': '', 'LC_ALL': 'C', 'TZ': 'UTC'}
        for key in ('HOME', 'USERPROFILE', 'XDG_CONFIG_HOME', 'XDG_DATA_HOME', 'XDG_CACHE_HOME',
                    'XDG_STATE_HOME', 'TMPDIR', 'TMP', 'TEMP'):
            path = world / 'scratch' / key
            path.mkdir(parents=True, exist_ok=True, mode=0o700)
            env[key] = str(path)
        return env

    def run(self, argv, cwd, env, stdin=None, check=False):
        def limits():
            resource.setrlimit(resource.RLIMIT_FSIZE, (FSIZE_LIMIT, FSIZE_LIMIT))
            resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
        self.commands += 1
        process = subprocess.Popen(argv, cwd=cwd, env=env, stdin=subprocess.PIPE if stdin is not None else subprocess.DEVNULL,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True, preexec_fn=limits)
        try:
            stdout, stderr = process.communicate(stdin, timeout=RUN_TIMEOUT)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.communicate()
            raise AssertionError(f'time bound exceeded: {argv[0]}')
        try: os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError: pass
        rss = resource.getrusage(resource.RUSAGE_CHILDREN).ru_maxrss
        self.maxrss = max(self.maxrss, rss * (1 if platform.system() == 'Darwin' else 1024))
        if check and process.returncode: raise RuntimeError(f'{argv}: exit {process.returncode}')
        return process.returncode, stdout, stderr

    def make_world(self, case, language):
        world = self.runtime / 'worlds' / case['id'] / language
        while world.exists(): world = world.with_name(world.name + '+')
        work, outside = world / 'work', world / 'outside'
        for path in (work, outside): path.mkdir(parents=True, mode=0o700)
        for name, data in self.inputs.items():
            (work / f'{name}.pdf').write_bytes(data)
        (work / 'adir').mkdir(mode=0o755)
        (work / 'existing.pdf').write_bytes(b'previous')
        (work / 'link-good.pdf').symlink_to('good.pdf')
        (work / 'link-kids-cycle.pdf').symlink_to('kids-cycle.pdf')
        (work / 'dangling.pdf').symlink_to('nowhere.pdf')
        (work / 'outlink').symlink_to('../outside')
        os.link(work / 'good.pdf', work / 'hard-good.pdf')
        for rel in case.get('premake', []):
            (work / rel).parent.mkdir(parents=True, exist_ok=True)
            (work / rel).write_bytes(b'previous')
        return world, work

    def inspect(self, path, cwd, env):
        """Real-tool view of one PDF; /ID is the only nondeterministic field."""
        view = {}
        code, out, err = self.run([self.paths['qpdf'], '--show-npages', str(path)], cwd, env)
        view['npages'] = {'exit': code, 'stdout': out.decode(errors='replace').strip(), 'stderr': err.decode(errors='replace')}
        code, out, err = self.run([self.paths['qpdf'], '--check', str(path)], cwd, env)
        view['check'] = {'exit': code, 'stderr': err.decode(errors='replace')}
        code, out, err = self.run([self.paths['qpdf'], '--json', '--json-stream-data=none', str(path)], cwd, env)
        semantic = {}
        if code in (0, 3):
            semantic = json.loads(out)
            for block in semantic.get('qpdf', []):
                if isinstance(block, dict) and 'trailer' in block:
                    block['trailer'].get('value', {}).pop('/ID', None)
        view['semantic_sha256'] = digest(json.dumps(semantic, sort_keys=True).encode())
        view['json_exit'] = code
        code, out, err = self.run([self.paths['pdfinfo'], str(path)], cwd, env)
        view['pdfinfo'] = {'exit': code, 'stdout': out.decode(errors='replace'), 'stderr': err.decode(errors='replace')}
        return view

    def observe(self, case, language, binary, tools=None):
        world, work = self.make_world(case, language)
        env = self.tool_env(world)
        request = {k: case[k] for k in ('operation', 'input', 'inputs', 'output', 'pages', 'degrees') if k in case}
        request['output'] = request.get('output', '').replace('@OUTSIDE@', str(world / 'outside'))
        request.update(layer='api', tools=tools or {key: self.paths[name] for key, name in zip(TOOLS, NAMES)})
        before = snapshot(world)
        code, out, err = self.run([str(binary)], work, env, json.dumps(request).encode())
        after = snapshot(world)
        assert code == 0 and not err, f'{case["id"]}/{language}: driver exit {code} stderr {err[:400]!r}'
        assert len(out) < 1 << 20, 'response size bound'
        response = json.loads(out)
        changes = delta(before, after)
        # bounds + hygiene: inputs untouched, no stage/temp leftovers, no writes in HOME/XDG/tmp
        for name in self.inputs:
            assert before[f'work/{name}.pdf'] == after[f'work/{name}.pdf'] or case.get('alias_ok'), \
                f'{case["id"]}/{language}: input {name} changed'
        assert not [p for p in changes if p.startswith('scratch/')], 'scratch dir written'
        assert not [p for p in after if '.symdesk-pdf-' in p], 'output stage leaked'
        assert sum(v.get('size', 0) for v in changes.values()) <= DELTA_LIMIT, 'output size bound'
        # side effects must stay beneath the requested destination (and ancestors of it)
        targets = [work] if not request['output'] and case['operation'] == 'split' else []
        if request['output']:
            lexical = Path(os.path.normpath(work / request['output']))
            targets = [lexical, Path(os.path.realpath(lexical.parent)) / lexical.name]
        for rel in changes:
            full = world / rel
            allowed = any(full == t or t in full.parents or full in t.parents for t in targets)
            assert allowed, f'{case["id"]}/{language}: write outside destination: {rel}'
        outputs = {}
        for rel, entry in sorted(changes.items()):
            if entry['type'] == 'file':
                data = (world / rel).read_bytes() if entry.get('size', 0) <= HASH_LIMIT else b''
                if data.startswith(b'%PDF'):
                    neutral = re.sub(rb'/ID\s*\[\s*<[0-9a-fA-F]+>\s*<[0-9a-fA-F]+>\s*\]', b'/ID[]', data)
                    outputs[rel] = self.inspect(world / rel, work, env) | {'bytes_sha256': entry.get('sha256'), 'bytes_sans_id_sha256': digest(neutral)}
        # an error response must leave no partial single-file output
        if response['error'] and case['operation'] != 'split':
            new_files = [rel for rel, e in changes.items() if e['type'] == 'file' and rel.startswith('work/out')]
            assert not new_files or case['id'].find('-out-') >= 0, f'{case["id"]}/{language}: partial output after error'
        # raw bytes of qpdf/Poppler outputs carry a random /ID: compared semantically, hashed in `info`
        stable = {k: ({kk: vv for kk, vv in v.items() if kk != 'sha256'} if k in outputs else v) for k, v in changes.items()}
        view = {'response': response, 'delta': stable, 'outputs': {k: {kk: vv for kk, vv in v.items() if kk != 'bytes_sha256'} for k, v in outputs.items()}}
        info = {'bytes': {k: v['bytes_sha256'] for k, v in outputs.items()}}
        return normalized(view, world), normalized(info, world)


def compare_case(harness, case, tools_rust=None):
    seen = []
    infos = []
    for language, binary in (('go', harness.go), ('rust', harness.rust)):
        view, info = harness.observe(case, language, binary, tools_rust if language == 'rust' else None)
        seen.append(view)
        infos.append(info)
    (harness.out / f'{case["id"]}.json').write_text(json.dumps(seen, indent=1, sort_keys=True) + '\n')
    try:
        compare(*seen)
        error = None
    except AssertionError as exc:
        error = str(exc)
    return seen, infos, error


def host_os():
    if sys.platform == 'darwin': return 'darwin'
    if sys.platform.startswith('linux'): return 'linux'
    raise SystemExit(f'hostile PDF recordings exist for macOS and Linux only, not {sys.platform}')


def default_recording():
    return ROOT / f'testdata/port/render/pdfops-hostile-recording-{host_os()}.json'


def tool_versions(paths):
    versions = {}
    for name, path in paths.items():
        raw = subprocess.run([path, '--version' if name == 'qpdf' else '-v'], capture_output=True, text=True, timeout=15)
        versions[name] = (raw.stdout + raw.stderr).splitlines()[0]
    return versions


def real_tool_paths():
    found = {name: shutil.which(name) for name in NAMES}
    if not all(found.values()): raise RuntimeError(f'real Poppler/qpdf tools required: {found}')
    return {name: str(Path(path).resolve()) for name, path in found.items()}


def hostile_inputs(runtime, paths):
    """Generate the deterministic corpus inputs; returns name -> bytes."""
    generated = runtime / 'inputs'
    subprocess.run([sys.executable, '-I', str(HERE / 'hostile_inputs.py'), str(generated), paths['qpdf']], check=True, capture_output=True, timeout=60)
    again = runtime / 'inputs-again'
    subprocess.run([sys.executable, '-I', str(HERE / 'hostile_inputs.py'), str(again), paths['qpdf']], check=True, capture_output=True, timeout=60)
    assert json.loads((generated / 'manifest.json').read_text()) == json.loads((again / 'manifest.json').read_text()), 'hostile inputs not deterministic'
    return inputs_for(generated)


def build_go_driver(runtime):
    go = runtime / 'bin/pdfops-go'
    go.parent.mkdir(parents=True)
    # The Go tool this process was started with (the portgen check puts its own GOROOT first).
    toolchain = dict(os.environ, GOTOOLCHAIN='go1.26.10', GOENV='off', GOWORK='off')
    go_root = subprocess.check_output(['go', 'env', 'GOROOT'], text=True, env=toolchain, cwd=ROOT).strip()
    subprocess.run([f'{go_root}/bin/go', 'build', '-o', str(go), './internal/ingest/testdata/portpdfops'], cwd=ROOT, check=True,
                   timeout=900, env=dict(os.environ, GOTOOLCHAIN='local', GOENV='off', GOWORK='off', CGO_ENABLED='0'))
    identity = json.loads(subprocess.check_output([str(go), '--identity'], timeout=30))
    assert identity['compiler'] == 'go1.26.10', identity
    return go, identity


def record_document(runtime, out):
    paths = real_tool_paths()
    go, identity = build_go_driver(runtime)
    harness = Harness(out, runtime, paths, go, None, hostile_inputs(runtime, paths))
    cases = []
    for case in corpus():
        view, _ = harness.observe(case, 'go', go)
        cases.append({'id': case['id'], 'view': view})
    return {'schema_version': 1, 'oracle': identity['oracle'], 'compiler': identity['compiler'], 'os': host_os(),
            'tools': tool_versions(paths), 'cases': cases}


def record_main(args):
    out, runtime = args.output.resolve(), args.runtime_root.resolve()
    for path in (out, runtime): path.mkdir(parents=True, exist_ok=False)
    document = record_document(runtime, out)
    encoded = (json.dumps(document, indent=1, sort_keys=True, ensure_ascii=False) + '\n').encode()
    target = args.recording or default_recording()
    if args.check:
        if not target.is_file() or target.read_bytes() != encoded:
            print(f'{target.name} drifted from the live Go PDF adapters on {host_os()}', file=sys.stderr)
            return 1
        print(f'PASS {target.name} matches the live Go PDF adapters ({len(document["cases"])} cases, real tools {document["tools"]})')
        return 0
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(encoded)
    print(f'recorded {target} ({len(document["cases"])} cases)')
    return 0


def replay_main(args):
    """Replay the recorded Go observations through the Rust probe; Go is never started."""
    out, runtime = args.output.resolve(), args.runtime_root.resolve()
    for path in (out, runtime): path.mkdir(parents=True, exist_ok=False)
    recording = json.loads(args.replay.read_text())
    assert recording['schema_version'] == 1 and recording['os'] == host_os(), 'recording is for another OS'
    paths = real_tool_paths()
    versions = tool_versions(paths)
    if versions != recording['tools']:
        print(f'installed tools {versions} differ from the recorded {recording["tools"]}; re-record with '
              f'make render-pdfops-hostile-fixtures-generate', file=sys.stderr)
        return 1
    rust = args.rust_binary.resolve(strict=True)
    harness = Harness(out, runtime, paths, None, rust, hostile_inputs(runtime, paths))
    cases = corpus()
    assert [c['id'] for c in cases] == [c['id'] for c in recording['cases']], 'recording does not cover the corpus'
    failures = 0
    for case, recorded in zip(cases, recording['cases']):
        view, _ = harness.observe(case, 'rust', rust)
        try:
            compare(recorded['view'], view)
        except AssertionError as error:
            failures += 1
            print(f'FAIL {case["id"]}: {error}'[:400], file=sys.stderr, flush=True)
    if failures:
        print(f'{failures} hostile replay mismatches of {len(cases)}', file=sys.stderr)
        return 1
    print(f'PASS hostile PDF replay: {len(cases)} cases match the Go recording (real tools {versions})')
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path)
    parser.add_argument('--runtime-root', type=Path)
    parser.add_argument('--record', action='store_true', help='record the Go observations (no Rust side)')
    parser.add_argument('--check', action='store_true', help='with --record: verify the committed recording instead of writing it')
    parser.add_argument('--recording', type=Path, help='recording file for --record/--check (default: per-OS file)')
    parser.add_argument('--replay', type=Path, help='replay this recording through --rust-binary (no Go)')
    parser.add_argument('--go-binary', type=Path)
    parser.add_argument('--rust-binary', type=Path)
    parser.add_argument('--only', help='regex of case ids (diagnostics; never certifies)')
    args = parser.parse_args()
    if args.check and not args.record: parser.error('--check needs --record')
    if args.record or args.replay:
        scratch = None
        if not args.output or not args.runtime_root:
            scratch = Path(tempfile.mkdtemp(prefix='symdesk-hostile-'))
            args.output, args.runtime_root = args.output or scratch / 'out', args.runtime_root or scratch / 'runtime'
        try:
            return record_main(args) if args.record else replay_main(args)
        finally:
            if scratch: shutil.rmtree(scratch, ignore_errors=True)
    if not args.output or not args.runtime_root: parser.error('--output and --runtime-root are required')
    out, runtime = args.output.resolve(), args.runtime_root.resolve()
    out.mkdir(parents=True, exist_ok=False)
    runtime.mkdir(parents=True, exist_ok=False)
    found = {name: shutil.which(name) for name in NAMES}
    if not all(found.values()): raise RuntimeError(f'real Poppler/qpdf tools required: {found}')
    paths = {name: str(Path(path or '').resolve()) for name, path in found.items()}
    report = {'passed': False, 'head': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip(),
              'dirty': subprocess.check_output(['git', 'status', '--porcelain=v1'], cwd=ROOT, text=True),
              'native_os': platform.system(), 'native_arch': platform.machine(), 'tools': {}, 'cases': [], 'filtered': bool(args.only)}
    try:
        for name, path in paths.items():
            raw = subprocess.run([path, '--version' if name == 'qpdf' else '-v'], capture_output=True, text=True, timeout=15)
            report['tools'][name] = {'path': path, 'sha256': digest(Path(path).read_bytes()), 'version': (raw.stdout + raw.stderr).splitlines()[0]}
        if args.go_binary and args.rust_binary:
            go, rust = args.go_binary.resolve(), args.rust_binary.resolve()
        else:
            go = runtime / 'bin/pdfops-go'
            go.parent.mkdir()
            toolchain = dict(os.environ, GOTOOLCHAIN='go1.26.10', GOENV='off', GOWORK='off')
            go_root = subprocess.check_output(['go', 'env', 'GOROOT'], text=True, env=toolchain, cwd=ROOT).strip()
            subprocess.run([f'{go_root}/bin/go', 'build', '-o', str(go), './internal/ingest/testdata/portpdfops'], cwd=ROOT, check=True,
                           timeout=900, env=dict(os.environ, GOTOOLCHAIN='local', GOENV='off', GOWORK='off', CGO_ENABLED='0'))
            # The fixture check runs under a toolchain-only PATH; cargo still
            # needs the system linker driver and its own proxies.
            cargo_env = dict(os.environ, PATH=os.pathsep.join([os.environ.get('PATH', ''), str(Path.home() / '.cargo/bin'), '/usr/bin', '/bin']))
            cargo = str(Path.home() / '.cargo/bin/cargo')
            subprocess.run([cargo, 'build', '--locked', '-p', 'symdesk-render', '--examples'], cwd=ROOT, check=True, timeout=1800, env=cargo_env)
            target = json.loads(subprocess.check_output([cargo, 'metadata', '--no-deps', '--format-version', '1'], cwd=ROOT, env=cargo_env))['target_directory']
            rust = Path(target) / 'debug/examples/pdfops_probe'
        report['binaries'] = {str(p): digest(p.read_bytes()) for p in (go, rust)}
        identity = json.loads(subprocess.check_output([str(go), '--identity'], timeout=30))
        assert identity['compiler'] == 'go1.26.10', identity
        report['oracle'] = identity
        # generate (and check determinism of) the hostile inputs with the real qpdf
        generated = runtime / 'inputs'
        subprocess.run([sys.executable, '-I', str(HERE / 'hostile_inputs.py'), str(generated), paths['qpdf']], check=True, capture_output=True, timeout=60)
        again = runtime / 'inputs-again'
        subprocess.run([sys.executable, '-I', str(HERE / 'hostile_inputs.py'), str(again), paths['qpdf']], check=True, capture_output=True, timeout=60)
        report['input_manifest'] = json.loads((generated / 'manifest.json').read_text())
        assert report['input_manifest'] == json.loads((again / 'manifest.json').read_text()), 'hostile inputs not deterministic'
        inputs = inputs_for(generated)
        harness = Harness(out, runtime, paths, go, rust, inputs)
        # valid inputs must really be valid, hostile ones must really be hostile to the real tools
        scratch = runtime / 'inputs-check'
        scratch.mkdir()
        env = harness.tool_env(scratch)
        report['input_checks'] = {}
        for name, data in sorted(inputs.items()):
            (scratch / f'{name}.pdf').write_bytes(data)
            code, _, _ = harness.run([paths['qpdf'], '--check', str(scratch / f'{name}.pdf')], scratch, env)
            report['input_checks'][name] = code
        for name in ('good', 'b', 'valid'): assert report['input_checks'][name] == 0, f'{name} must pass qpdf --check'
        assert any(report['input_checks'][n] != 0 for n in HOSTILE), 'no input hostile to the real tools'
        cases = [c for c in corpus() if not args.only or re.search(args.only, c['id'])]
        failures = byte_equal = byte_total = 0
        stats = {'ok': {'split': 0, 'merge': 0, 'rotate': 0}, 'error': {'split': 0, 'merge': 0, 'rotate': 0}}
        last_ok = None
        for case in cases:
            seen, infos, error = compare_case(harness, case)
            go_view = seen[0]
            stats['error' if go_view['response']['error'] else 'ok'][case['operation']] += 1
            if case['id'] == 'merge-good-b': last_ok = case
            for rel, digest_go in infos[0]['bytes'].items():
                byte_total += 1
                byte_equal += digest_go == infos[1]['bytes'].get(rel)
            record = {'id': case['id'], 'operation': case['operation'], 'passed': error is None,
                      'go_error': go_view['response']['error'][:120], 'outputs': len(go_view['outputs'])}
            if error: record['error'] = error; failures += 1
            report['cases'].append(record)
            print(f'{case["id"]}: {"PASS" if error is None else "FAIL " + error[:200]}', flush=True)
        report.update(case_count=len(cases), stats=stats, raw_byte_identical_outputs=byte_equal, id_neutral_identical_outputs=byte_total, total_outputs=byte_total,
                      peak_child_rss_bytes=harness.maxrss, tool_invocations=harness.commands)
        assert report['cases'] and failures == 0, f'{failures} hostile differential mismatches'
        if not args.only:
            for op in ('split', 'merge', 'rotate'):
                assert stats['ok'][op] >= 10 and stats['error'][op] >= 10, f'corpus does not exercise both outcomes for {op}: {stats}'
            assert byte_total > 50
            # ---- mutation controls -------------------------------------------------
            assert last_ok is not None
            seen, _, _ = compare_case(harness, last_ok)
            good = seen[0]
            output_key = next(iter(good['outputs']))
            bad_pages = json.loads(json.dumps(good))
            bad_pages['outputs'][output_key]['npages']['stdout'] = '99'
            bad_error = json.loads(json.dumps(good)); bad_error['response']['error'] = 'tampered'
            bad_extra = json.loads(json.dumps(good)); bad_extra['delta']['work/extra.pdf'] = {'type': 'file', 'mode': 420, 'size': 1}
            bad_sem = json.loads(json.dumps(good)); bad_sem['outputs'][output_key]['semantic_sha256'] = '0' * 64
            bad_missing = json.loads(json.dumps(good)); bad_missing['outputs'].pop(output_key)
            controls = 0
            for bad in (bad_pages, bad_error, bad_extra, bad_sem, bad_missing):
                try: compare(good, bad)
                except AssertionError: controls += 1
                else: raise AssertionError('comparator mutation accepted')
            # injected faults in the REAL Rust pipeline must be caught by the same replay
            shims = runtime / 'shims'
            shims.mkdir()
            faults = {
                'rotate-wrong-angle': ('rotate-good-90', "args = [a.replace('--rotate=+90', '--rotate=+180') for a in args]"),
                'check-always-fails': ('split-good-range', "if '--check' in args: sys.exit(2)"),
                'merge-truncates-ranges': ('merge-good-b', "args = ['1' if a == '1-z' else a for a in args]"),
            }
            report['fault_controls'] = {}
            for fault, (case_id, body) in faults.items():
                shim = shims / f'{fault}-qpdf'
                shim.write_text(f"#!{sys.executable}\nimport os, sys\nargs = sys.argv[1:]\n{body}\nos.execv({paths['qpdf']!r}, [{paths['qpdf']!r}] + args)\n")
                shim.chmod(0o755)
                case = next(c for c in corpus() if c['id'] == case_id)
                tools = {key: paths[name] for key, name in zip(TOOLS, NAMES)} | {'QPDF': str(shim)}
                _, _, error = compare_case(harness, case | {'id': f'fault-{fault}'}, tools)
                assert error, f'fault {fault} was not detected by the comparator'
                report['fault_controls'][fault] = error[:200]
                controls += 1
            report['mutation_controls'] = controls
        report['binaries_unchanged'] = report['binaries'] == {str(p): digest(p.read_bytes()) for p in (go, rust)}
        report['tools_unchanged'] = all(digest(Path(t['path']).read_bytes()) == t['sha256'] for t in report['tools'].values())
        assert report['binaries_unchanged'] and report['tools_unchanged']
        report['passed'] = not args.only
        print(f'{"PASS" if report["passed"] else "FILTERED"} {len(cases)} hostile real-tool Go/Rust pairs; '
              f'{byte_total} outputs byte-identical after /ID neutralisation ({byte_equal} raw); stats {stats}', flush=True)
    finally:
        (out / 'report.json').write_text(json.dumps(report, indent=2, sort_keys=True) + '\n')
    return 0 if report['passed'] or args.only else 1


if __name__ == '__main__':
    raise SystemExit(main())
