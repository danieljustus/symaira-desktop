"""Readiness-synchronized safety checks using controlled tool inputs, not PDFs."""
import hashlib
import json
import os
from pathlib import Path
import signal
import sys
import time

# Executable INPUT for testing product lifetime/publication boundaries. All
# responses and side effects are captured from the real Go/Rust adapters.
TOOL = r'''import os, signal, subprocess, sys, time
from pathlib import Path
if sys.argv[1:] == ['descendant']:
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    Path('descendant-ready').write_text(str(os.getpid()))
    while True: time.sleep(1)
if sys.argv[1] == '--check': raise SystemExit(0)
mode = os.environ['SAFETY_MODE']
Path('leader').write_text(str(os.getpid()))
if mode.startswith('descendant-'):
    child = subprocess.Popen([sys.executable, __file__, 'descendant'])
    deadline = time.monotonic()+5
    while not Path('descendant-ready').exists():
        if time.monotonic()>deadline: raise RuntimeError('descendant readiness timeout')
        time.sleep(.005)
    Path('ready').write_text(str(os.getpid()))
    if mode == 'descendant-cancel':
        while True: time.sleep(1)
if mode.startswith('flood-'):
    stream = sys.stdout.buffer if mode == 'flood-out' else sys.stderr.buffer
    for _ in range(257): stream.write(b'x'*65536); stream.flush()
if sys.argv[1:] == ['--version']:
    print('typst 0.15.0')
    raise SystemExit(0)
if mode == 'race':
    target = Path(os.environ['SAFETY_OUTPUT'])
    target.parent.mkdir(parents=True, exist_ok=True)
    if not target.exists(): os.link('a.pdf', target)
staged = sys.argv[2] if sys.argv[-1].startswith('--rotate=') else sys.argv[-1]
Path(staged).write_bytes(b'controlled staged PDF input; not a semantic PDF fixture\n')
'''


def alive(pid):
    try: os.kill(pid, 0)
    except ProcessLookupError: return False
    return True


def verify_safety(run, runtime, output, env, binaries, report, compare, normalized):
    if os.name == 'nt':
        raise RuntimeError('native Windows process-tree safety is not implemented; cannot pass')
    cases = [(operation, mode) for operation in ('merge', 'press')
             for mode in ('descendant-cancel', 'descendant-exit', 'flood-out', 'flood-err')]
    cases += [(operation, 'race') for operation in ('split', 'merge', 'rotate')]
    report['safety_cases'] = []
    report['safety_tool_sha256'] = hashlib.sha256(TOOL.encode()).hexdigest()
    for operation, mode in cases:
        case_id = f'safety-{operation}-{mode}'
        observations = []
        records = []
        for language in ('go', 'rust'):
            root = runtime/case_id/language
            root.mkdir(parents=True, mode=0o700)
            variables = {'PATH':'', 'LC_ALL':'C', 'TZ':'UTC', 'SAFETY_MODE':mode,
                         'SAFETY_OUTPUT':'parts/part-001.pdf' if operation=='split' else 'out/result.pdf'}
            for key in ('HOME','USERPROFILE','XDG_CONFIG_HOME','XDG_DATA_HOME','XDG_CACHE_HOME','XDG_STATE_HOME','TMPDIR','TMP','TEMP'):
                directory=root/key; directory.mkdir(mode=0o700); variables[key]=str(directory)
            original=b'controlled input; input preservation is byte-exact\n'
            for name in ('a.pdf','b.pdf'): (root/name).write_bytes(original)
            tool=root/('typst' if operation=='press' else 'qpdf')
            tool.write_text('#!'+sys.executable+'\n'+TOOL); tool.chmod(0o700)
            info=root/'pdfinfo'
            info.write_text('#!'+sys.executable+"\nprint('Pages: 4')\n"); info.chmod(0o700)
            if operation=='press':
                request=[dict(operation='detect',name=str(tool),cancel_marker='ready' if mode=='descendant-cancel' else '')]
            else:
                request=dict(operation=operation,layer='api',input='a.pdf',inputs=['a.pdf','b.pdf'],
                             output='parts' if operation=='split' else 'out/result.pdf',
                             pages='1',degrees=90,tools={'QPDF':str(tool),'PDFInfo':str(info),'PDFSeparate':'unused','PDFUnite':'unused'},
                             cancel_marker='ready' if mode=='descendant-cancel' else '')
            binary=binaries['press' if operation=='press' else 'pdf'][language]
            label=case_id+'-'+language
            record={'language':language,'outer_cleanup_required':False,'passed':False}
            records.append(record)
            start=time.monotonic()
            try:
                raw=run([str(binary)],label,cwd=root,variables=variables,stdin=json.dumps(request).encode())
                record['elapsed_seconds']=time.monotonic()-start
                assert record['elapsed_seconds']<8, 'bounded operation exceeded eight-second safety check'
                response=json.loads(raw)
                if mode.startswith('descendant-'):
                    assert (root/'ready').is_file() and (root/'descendant-ready').is_file(), 'readiness missing'
                    leader=int((root/'ready').read_text()); descendant=int((root/'descendant-ready').read_text())
                    record.update(leader_pid=leader,descendant_pid=descendant)
                    deadline=time.monotonic()+2
                    while (alive(leader) or alive(descendant)) and time.monotonic()<deadline: time.sleep(.005)
                    assert not alive(leader) and not alive(descendant), 'product left an owned process alive'
                if operation=='press':
                    assert response[0]['available'] is True
                    if mode=='descendant-exit': assert response[0]['version']=='0.15.0'
                    else: assert 'version' not in response[0]
                else:
                    error=response['error']
                    if mode.startswith('flood-'): assert 'exceeds 16 MiB' in error, error
                    elif mode=='race': assert 'publish PDF output:' in error, error
                    elif mode=='descendant-cancel': assert 'signal: killed' in error, error
                    else: assert not error, error
                for name in ('a.pdf','b.pdf'): assert (root/name).read_bytes()==original, 'input changed'
                assert not list(root.rglob('.symdesk-pdf-*')), 'output staging leaked'
                assert not (output/(label+'.stderr')).read_bytes(), 'driver stderr polluted'
                observations.append(normalized(response,root))
                record['passed']=True
            finally:
                # A failing product must not leak the fixture. Such cleanup is
                # retained as FAILURE, never counted as product safety.
                pids={int((root/marker).read_text()) for marker in ('leader','ready','descendant-ready') if (root/marker).is_file()}
                for pid in pids:
                    if alive(pid):
                        record['outer_cleanup_required']=True
                        try: os.kill(pid,signal.SIGKILL)
                        except ProcessLookupError: pass
                (output/(case_id+'.safety.json')).write_text(json.dumps(records,indent=2)+'\n')
        compare(*observations)
        report['safety_cases'].append({'id':case_id,'passed':all(r['passed'] and not r['outer_cleanup_required'] for r in records),'observations':records})
    assert len(report['safety_cases'])==len(cases) and all(c['passed'] for c in report['safety_cases'])
