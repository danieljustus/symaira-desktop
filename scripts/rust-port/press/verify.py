"""Source-bound profile/discovery pairs called by the owning render gate.

The tool is a compiled stand-in. This proves discovery/cache behavior, not real
Typst rendering. Every result comes from the actual Go or Rust public API.
"""
import json
import os
from pathlib import Path
import re
import shutil


def verify_press(run, runtime, output, env, binary_root, stub, report, compare, normalized, digest):
    suffix = '.exe' if os.name == 'nt' else ''
    oracle = runtime / ('press-go'+suffix)
    candidate = binary_root / ('press_probe'+suffix)
    run(['go','build','-o',str(oracle),'./internal/pdf/testdata/portpress'], 'press-build-go', build=True)
    identity = json.loads(run([str(oracle),'--identity'], 'press-oracle-identity'))
    assert identity == report['oracle'], 'press and PDF oracle identities differ'
    report['binaries'].update({str(p):digest(p) for p in (oracle,candidate)})
    cases = [
        dict(id='registry',steps=[dict(operation='profiles')]+[dict(operation='lookup',name=n) for n in ('behoerde','brief','meeting','rechnung','report','Report',' report ','unknown','')]),
        dict(id='default',steps=[dict(operation='detect')]),
        dict(id='override',steps=[dict(operation='detect',name=' TOOL ')]),
        dict(id='unicode-fields',info='\u0085typst\u20020.15.0\u2028(build)\n',steps=[dict(operation='detect')]),
        dict(id='short-version',info='typst\n',steps=[dict(operation='detect')]),
        dict(id='empty-version',info='',steps=[dict(operation='detect')]),
        dict(id='version-failed',mode='error',steps=[dict(operation='detect')]),
        dict(id='precancelled',steps=[dict(operation='detect',cancel=True)]),
        # A 50-ms launch deadline races process startup, not discovery semantics.
        # Keep the expired deadline deterministic, then prove running cancellation
        # separately with the same ready-marker handshake as the PDF corpus.
        dict(id='deadline',steps=[dict(operation='detect',expired_deadline=True)]),
        dict(id='running-cancel',mode='cancel',steps=[dict(operation='detect',cancel_marker='ready')]),
        dict(id='positive-cache',steps=[dict(operation='detect'),dict(operation='remove',path='TOOL'),dict(operation='detect',name=' typst ',cancel=True),dict(operation='reset'),dict(operation='detect')]),
        dict(id='negative-cache',absent=True,steps=[dict(operation='detect'),dict(operation='install',source='STUB',path='TOOL'),dict(operation='detect'),dict(operation='reset'),dict(operation='detect')]),
        dict(id='distinct-keys',steps=[dict(operation='detect'),dict(operation='detect',name='other')]),
    ]
    report['press_cases'] = []
    for case in cases:
        observations = []
        failure = None
        for language,binary in (('go',oracle),('rust',candidate)):
            root = runtime / ('press-'+case['id']) / language
            root.mkdir(parents=True,mode=0o700)
            bindir = root/'bin'; bindir.mkdir(mode=0o700)
            tool = bindir/('typst'+suffix)
            if not case.get('absent'): shutil.copy2(stub,tool)
            variables = {k:v for k,v in env.items() if k in {'SystemRoot','WINDIR','PATHEXT','TZ','LC_ALL'}}
            variables.update(PATH=str(bindir),PDF_STUB_TOOL='typst',PDF_STUB_MODE=case.get('mode',''),PDF_STUB_INFO=case.get('info','typst 0.15.0 (abcdef)\n'))
            for key in ('HOME','USERPROFILE','XDG_CONFIG_HOME','XDG_DATA_HOME','XDG_STATE_HOME','XDG_CACHE_HOME','TMPDIR','TMP','TEMP'):
                directory=root/key; directory.mkdir(mode=0o700); variables[key]=str(directory)
            steps = [{k:(v.replace('TOOL',str(tool)).replace('STUB',str(stub)) if isinstance(v,str) else v) for k,v in step.items()} for step in case['steps']]
            label='press-'+case['id']+'-'+language
            raw=run([str(binary)],label,cwd=root,variables=variables,stdin=json.dumps(steps).encode())
            response=json.loads(raw)
            assert isinstance(response,list) and len(response)==len(steps)
            assert not (output/(label+'.stderr')).read_bytes(), 'unexpected probe stderr'
            trace = [json.loads(line) for line in (root/'trace.jsonl').read_bytes().split(b'\n') if line] if (root/'trace.jsonl').exists() else []
            if (root/'ready').exists() and os.name!='nt':
                pid=int((root/'ready').read_text())
                try: os.kill(pid,0)
                except ProcessLookupError: pass
                else: raise AssertionError('cancelled discovery process still alive')
            assert not list((root/'TMPDIR').iterdir()), 'discovery capture leaked'
            # This independent branch check prevents two missing discoveries
            # from passing simply by agreeing on a negative result.
            if case['id']=='default':
                assert response[0]['available'] is True and response[0]['version']=='0.15.0'
                assert trace == [{'tool':'typst','args':['--version']}]
            if case['id']=='deadline':
                assert response[0]['available'] is True and 'version' not in response[0] and trace == []
            if case['id']=='running-cancel':
                assert response[0]['available'] is True and 'version' not in response[0]
                assert trace == [{'tool':'typst','args':['--version']}] and (root/'ready').exists()
            if case['id']=='positive-cache':
                assert response[0]==response[2] and response[4]['available'] is False and len(trace)==1
            if case['id']=='negative-cache':
                assert response[0]==response[2] and response[4]['available'] is True and len(trace)==1
            observations.append(normalized({'response':response,'trace':trace},root))
        (output/('press-'+case['id']+'.json')).write_text(json.dumps(observations,indent=2)+'\n')
        try: compare(*observations)
        except AssertionError as error: failure=str(error)
        report['press_cases'].append({'id':case['id'],'passed':failure is None,'error':failure})
        print(f'press-{case["id"]}: {failure is None}',flush=True)
    assert [c['id'] for c in report['press_cases']] == [c['id'] for c in cases]
    assert all(c['passed'] is True for c in report['press_cases']), 'profile/discovery mismatch'
    tests=run(['go','test','-count=1','-v','./internal/pdf/internal/press','-run','^Test(BuiltinProfilesPresent|BehoerdeCapabilities|ReportIsNotAccessibilityGated|AllSorted|DetectTypst.*|TypstVersion.*)$'], 'go-press-tests', build=True)
    assert b'--- PASS: TestAllSorted' in tests and b'--- PASS: TestDetectTypstCache' in tests
    report['go_press_tests']=len(re.findall(rb'^--- PASS: Test', tests, re.M))
    assert report['go_press_tests'] > 0 and b'--- SKIP:' not in tests
