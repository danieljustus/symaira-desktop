"""Prepared Unix MCP lifetime regressions; invoked only by admitted PDF probe.

Controlled qpdf wrapper is an executable INPUT, not an oracle. It delays the
real qpdf until a readiness-synchronized EOF/release, or until transport abort
kills it and its TERM-resistant descendant. No product response is synthesized.
"""
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time
from typing import Any

TOOL = r'''import os, signal, subprocess, sys, time
from pathlib import Path
def mark(name):
    pending = Path(name + '.pending')
    pending.write_text(str(os.getpid()))
    pending.replace(name)
def wait_for(name):
    deadline = time.monotonic() + 10
    while not Path(name).is_file():
        if time.monotonic() >= deadline:
            raise RuntimeError('controlled tool barrier timed out')
        time.sleep(.005)
if sys.argv[1:] == ['descendant']:
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    mark('descendant-ready')
    while True:
        time.sleep(1)
real = os.environ['PDF_MCP_REAL_QPDF']
if sys.argv[1:2] == ['--check']:
    os.execv(real, [real, *sys.argv[1:]])
mark('leader')
subprocess.Popen([sys.executable, __file__, 'descendant'])
wait_for('descendant-ready')
mark('ready')
wait_for('release')
os.execv(real, [real, *sys.argv[1:]])
'''


def cases() -> list[dict[str, Any]]:
    return [dict(id=f'transport-{name}-{framed}-{ending}', name=name,
                 framed=framed, ending=ending)
            for name in ('desk_rotate_pdf', 'rotate_pdf')
            for framed in (False, True)
            for ending in ('fatal-read', 'fatal-write-ping', 'fatal-write-tool', 'eof')]


def wire(body, framed):
    return (f'Content-Length: {len(body)}\r\n\r\n'.encode() + body
            if framed else body + b'\n')


def alive(pid):
    # Fail closed even on a retained zombie, as the existing safety.py does.
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    return True


def wait_until(predicate, seconds, message):
    deadline = time.monotonic() + seconds
    while not predicate():
        if time.monotonic() >= deadline:
            raise AssertionError(message)
        time.sleep(.005)


def verify(runtime, output, binaries, tools, pdf_input, run_tool, snapshot, frame_body):
    """Compare lifetime invariants; retain abort diagnostics, not byte parity.

    Raw ordinary/notification envelopes are covered by the main differential.
    Abort races may suppress an in-flight response, so this corpus gates actual
    nonzero exit, cancellation, child death, input preservation and publication.
    """
    declared = cases()
    report: dict[str, Any] = dict(passed=False, declared_cases=declared, cases=[],
                  source_class='real production MCP with controlled readiness wrapper around real qpdf',
                  comparison='lifetime effects; abort stdout/stderr retained, NOT byte-parity acceptance',
                  tool_sha256=hashlib.sha256(TOOL.encode()).hexdigest(),
                  python=dict(path=sys.executable, sha256=hashlib.sha256(Path(sys.executable).read_bytes()).hexdigest()))
    try:
        for case in declared:
            observations = []
            for language, binary in binaries.items():
                label = case['id'] + '-' + language
                root = runtime / label
                root.mkdir(mode=0o700)
                env = dict(PATH=str(root), TZ='UTC', LC_ALL='C', LANG='C',
                           PDF_MCP_REAL_QPDF=str(tools['qpdf']))
                for key in ('HOME', 'USERPROFILE', 'XDG_CONFIG_HOME', 'XDG_DATA_HOME',
                            'XDG_STATE_HOME', 'XDG_CACHE_HOME', 'TMPDIR', 'TMP', 'TEMP'):
                    directory = root / key
                    directory.mkdir(mode=0o700)
                    env[key] = str(directory)
                original = pdf_input(['one', 'two'])
                (root / 'a.pdf').write_bytes(original)
                wrapper = root / 'qpdf'
                wrapper.write_text('#!' + sys.executable + '\n' + TOOL)
                wrapper.chmod(0o700)
                baseline = {p.relative_to(root).as_posix(): hashlib.sha256(p.read_bytes()).hexdigest()
                            for p in root.rglob('*') if p.is_file()}
                record: dict[str, Any] = dict(language=language, passed=False, exit=None, errors=[],
                              outer_cleanup_required=False, timed_out=False,
                              argv=[str(binary), 'mcp', '--allow-write=true'], cwd=str(root))
                observations.append(record)
                first = wire(json.dumps(dict(jsonrpc='2.0', id=1, method='tools/call', params=dict(
                    name=case['name'], arguments=dict(input='a.pdf', output='out/result.pdf', degrees=90)))).encode(), case['framed'])
                input_log = output / (label + '.stdin')
                input_log.write_bytes(first)
                process = None
                fault_fd = None
                pids = set()
                try:
                    # Validate the synthetic PDF with the same real tool used
                    # after the barrier; a broken seed is not cancellation proof.
                    _, err, command = run_tool([tools['qpdf'], '--check', root / 'a.pdf'], label + '-input', root, env)
                    assert command['exit'] == 0 and not command['timed_out'] and not err
                    with (output / (label + '.stdout')).open('xb') as out, (output / (label + '.stderr')).open('xb') as err:
                        write_failure = case['ending'].startswith('fatal-write')
                        if write_failure:
                            # A read-only output descriptor yields EBADF on
                            # write in BOTH real CLIs. A closed pipe instead
                            # invokes Go's stdout SIGPIPE policy before ServeIO
                            # can observe/cancel on a writer error.
                            fault_fd = os.open(output / (label + '.stdout'), os.O_RDONLY)
                        process = subprocess.Popen([binary, 'mcp', '--allow-write=true'], cwd=root, env=env,
                                                   stdin=subprocess.PIPE, stdout=fault_fd if write_failure else out,
                                                   stderr=err, start_new_session=True)
                        if fault_fd is not None:
                            os.close(fault_fd)
                            fault_fd = None
                        assert process.stdin is not None
                        process.stdin.write(first)
                        process.stdin.flush()
                        wait_until(lambda: (root / 'ready').is_file(), 5, 'PDF operation did not reach readiness')
                        assert process.poll() is None, 'MCP exited before abort barrier'
                        for marker in ('leader', 'ready', 'descendant-ready'):
                            pid = int((root / marker).read_text())
                            assert pid > 1 and alive(pid), 'readiness requires live owned process'
                            pids.add(pid)
                        record['readiness_verified'] = True
                        record['owned_pids'] = sorted(pids)
                        if case['ending'] == 'fatal-read':
                            tail = b'Content-Length: 10\r\n\r\n{}'
                            process.stdin.write(tail)
                            process.stdin.flush()
                            process.stdin.close()
                        elif write_failure:
                            record['write_fault'] = 'read-only stdout descriptor; EBADF, not SIGPIPE'
                            followup: dict[str, Any] = dict(jsonrpc='2.0', id=2, method='ping')
                            if case['ending'] == 'fatal-write-tool':
                                followup.update(method='tools/call', params=dict(name='desk_status', arguments={}))
                            tail = wire(json.dumps(followup).encode(), case['framed'])
                            process.stdin.write(tail)
                            process.stdin.flush()
                            # Keep stdin OPEN while waiting. EOF must not be
                            # required to discover an asynchronous writer error.
                        else:
                            tail = b''
                            process.stdin.close()
                            (root / 'release').write_bytes(b'release after clean EOF')
                        with input_log.open('ab') as transcript:
                            transcript.write(tail)
                        start = time.monotonic()
                        try:
                            process.wait(timeout=5)
                        except subprocess.TimeoutExpired:
                            record['timed_out'] = True
                            raise AssertionError('MCP failed to drain/abort within five seconds') from None
                        record['elapsed_after_event_seconds'] = time.monotonic() - start
                        record['exit'] = process.returncode
                        if not process.stdin.closed:
                            process.stdin.close()
                    wait_until(lambda: not any(alive(pid) for pid in pids), 2, 'product left owned descendant/leader alive')
                    record['children_reaped_by_product'] = True
                    assert all((root / name).is_file() and hashlib.sha256((root / name).read_bytes()).hexdigest() == sha
                               for name, sha in baseline.items()), 'protected input/tool changed'
                    assert not list(root.rglob('.symdesk-pdf-*')), 'staging leaked'
                    target = root / 'out/result.pdf'
                    if case['ending'] == 'eof':
                        assert process.returncode == 0 and target.is_file(), 'clean EOF cancelled publication'
                        raw = (output / (label + '.stdout')).read_bytes()
                        response = json.loads(frame_body(raw, case['framed']))
                        assert response['id'] == 1 and response['result']['isError'] is False
                        assert not (output / (label + '.stderr')).read_bytes()
                        _, pages = snapshot(root, label, env, baseline)
                        assert pages == [['one', 'two']], 'clean EOF did not produce real PDF'
                    else:
                        assert process.returncode is not None and process.returncode > 0, 'transport failure was swallowed or signalled'
                        assert not target.exists(), 'PDF published after fatal transport abort'
                    record['passed'] = True
                except (AssertionError, OSError, ValueError, KeyError) as error:
                    record['errors'].append(str(error))
                finally:
                    # Harness cleanup never counts as product cleanup. Preserve
                    # failed roots, stdout/stderr and marker evidence for review.
                    if process is not None:
                        if process.stdin is not None and not process.stdin.closed:
                            try:
                                process.stdin.close()
                            except BrokenPipeError:
                                pass  # cleanup of an already failed transport
                        if process.poll() is None:
                            record['outer_cleanup_required'] = True
                            os.killpg(process.pid, signal.SIGKILL)
                            process.wait(timeout=5)
                        record['exit'] = process.returncode
                    if fault_fd is not None:
                        os.close(fault_fd)
                    for marker in ('leader', 'ready', 'descendant-ready'):
                        path = root / marker
                        if path.is_file() and path.read_text().isdecimal():
                            pids.add(int(path.read_text()))
                    for pid in pids:
                        if pid > 1 and alive(pid):
                            record['outer_cleanup_required'] = True
                            try:
                                os.kill(pid, signal.SIGKILL)
                            except ProcessLookupError:
                                pass
                    record['passed'] = record['passed'] and not record['outer_cleanup_required']
                    (output / (label + '.transport.json')).write_text(json.dumps(record, indent=2) + '\n')
            report['cases'].append(dict(id=case['id'], passed=all(o['passed'] for o in observations), observations=observations))
        report['passed'] = ([c['id'] for c in report['cases']] == [c['id'] for c in declared]
                            and all(c['passed'] for c in report['cases']))
    finally:
        (output / 'transport-report.json').write_text(json.dumps(report, indent=2) + '\n')
    return report
