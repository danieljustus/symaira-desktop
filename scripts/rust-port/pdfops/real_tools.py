#!/usr/bin/env python3
"""Native real-PDF diagnostic using installed Poppler/qpdf and synthetic inputs.
No downloads, services or user documents. Does not certify the full renderer.
"""
import argparse
import hashlib
import json
import os

from pathlib import Path
import shutil
import signal
import subprocess
import sys
sys.dont_write_bytecode = True
from verify import ROOT, TOOLS, NAMES, compare, normalized, source_manifest


def pdf(labels):
    """Small valid uncompressed PDF, with actual xref offsets, not oracle output."""
    objects = [b'<< /Type /Catalog /Pages 2 0 R >>', b'', b'<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>']
    kids = []
    for label in labels:
        page_id = len(objects) + 1
        kids.append(f'{page_id} 0 R')
        objects.append(f'<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 400] /Resources << /Font << /F1 3 0 R >> >> /Contents {page_id+1} 0 R >>'.encode())
        text = f'BT /F1 18 Tf 30 200 Td ({label}) Tj ET\n'.encode()
        objects.append(f'<< /Length {len(text)} >>\nstream\n'.encode()+text+b'endstream')
    objects[1] = f'<< /Type /Pages /Count {len(labels)} /Kids [{" ".join(kids)}] >>'.encode()
    result = bytearray(b'%PDF-1.4\n')
    offsets = [0]
    for index, obj in enumerate(objects, 1):
        offsets.append(len(result)); result.extend(f'{index} 0 obj\n'.encode()+obj+b'\nendobj\n')
    start = len(result)
    result.extend(f'xref\n0 {len(offsets)}\n0000000000 65535 f \n'.encode())
    for offset in offsets[1:]: result.extend(f'{offset:010d} 00000 n \n'.encode())
    result.extend(f'trailer\n<< /Root 1 0 R /Size {len(offsets)} >>\nstartxref\n{start}\n%%EOF\n'.encode())
    return bytes(result)


def main():
    p=argparse.ArgumentParser(description=__doc__)
    for arg in ('output','runtime-root','go-binary','rust-binary'): p.add_argument('--'+arg,required=True,type=Path)
    a=p.parse_args()
    out=a.output.resolve(); out.mkdir(parents=True,exist_ok=False)
    runtime=a.runtime_root.resolve(); runtime.mkdir(parents=True,exist_ok=False)
    paths={name:shutil.which(name) for name in (*NAMES,'pdftotext')}
    if not all(paths.values()): raise RuntimeError('real Poppler/qpdf tools required')
    paths={name:path for name,path in paths.items() if path is not None}
    report={'passed':False,'diagnostic_complete':False,'source':'installed real Poppler/qpdf; synthetic valid PDFs', 'sources_before':source_manifest(), 'tools':{name:{'path':str(path),'sha256':hashlib.sha256(Path(path).read_bytes()).hexdigest()} for name,path in paths.items()}, 'binaries':{str(path):hashlib.sha256(path.read_bytes()).hexdigest() for path in (a.go_binary,a.rust_binary)},'cases':[],'commands':[],'validation_warnings':[]}
    def run(argv,label,cwd,env,data=None,check=True):
        record={'argv':argv,'exit':None,'label':label,'cwd':str(cwd),'timed_out':False}
        report['commands'].append(record)
        if data is not None: (out/(label+'.stdin')).write_bytes(data)
        with (out/(label+'.stdout')).open('xb') as stdout, (out/(label+'.stderr')).open('xb') as stderr:
            process=subprocess.Popen(argv,cwd=cwd,env=env,stdin=subprocess.PIPE if data is not None else subprocess.DEVNULL,stdout=stdout,stderr=stderr,start_new_session=True)
            try:
                process.communicate(data,timeout=15)
            except subprocess.TimeoutExpired:
                record['timed_out']=True
                os.killpg(process.pid,signal.SIGKILL)
                process.wait(timeout=5)
                raise
            finally:
                record['exit']=process.returncode
        if check and record['exit']: raise RuntimeError(f'{label}: exit {record["exit"]}')
        return (out/(label+'.stdout')).read_bytes(), record['exit']
    cases=[
        dict(id='split',operation='split',input='a.pdf',pages='3,1',output='parts',expected=[['one'],['two','three'],['four']]),
        dict(id='merge',operation='merge',inputs=['b.pdf','a.pdf'],output='out/result.pdf',expected=[['five','six','one','two','three','four']]),
        dict(id='rotate',operation='rotate',input='a.pdf',output='out/result.pdf',degrees=90,pages='1,3',expected=[['one','two','three','four']]),
        dict(id='rotate-negative',operation='rotate',input='a.pdf',output='out/result.pdf',degrees=-90,pages='',expected=[['one','two','three','four']]),
        dict(id='malformed-split',operation='split',input='bad.pdf',pages='1',output='parts'),
        dict(id='malformed-merge',operation='merge',inputs=['bad.pdf','b.pdf'],output='out/result.pdf'),
        dict(id='malformed-rotate',operation='rotate',input='bad.pdf',degrees=90,output='out/result.pdf'),
        dict(id='rotate-page-outside',operation='rotate',input='a.pdf',output='out/result.pdf',degrees=90,pages='50'),
        dict(id='merge-output-alias',operation='merge',inputs=['a.pdf','b.pdf'],output='a.pdf'),
        dict(id='rotate-output-alias',operation='rotate',input='a.pdf',degrees=90,output='a.pdf'),
        dict(id='split-output-alias',operation='split',input='part-001.pdf',pages='1',output='.'),
    ]
    for alias in ('symlink','hardlink'):
        for operation in ('merge','rotate'):
            cases.append(dict(id=f'{operation}-{alias}-alias',operation=operation,input='a.pdf',inputs=['a.pdf','b.pdf'],degrees=90,output='alias.pdf',alias=alias))
        cases.append(dict(id=f'split-{alias}-alias',operation='split',input='a.pdf',pages='1',output='parts',alias=alias))
    try:
        version_env={'PATH':'','LC_ALL':'C','TZ':'UTC','HOME':str(runtime)}
        for name,path in paths.items():
            run([path,'--version' if name=='qpdf' else '-v'],'version-'+name,runtime,version_env)
        # Establish that warnings are from tool OUTPUTS, not invalid synthetic
        # inputs. These bytes are generated test inputs, never oracle results.
        report['input_fixtures']={}
        for name,labels in (('a.pdf',['one','two','three','four']),('b.pdf',['five','six'])):
            fixture=runtime/name
            fixture.write_bytes(pdf(labels))
            report['input_fixtures'][name]=hashlib.sha256(fixture.read_bytes()).hexdigest()
            run([paths['qpdf'],'--check',str(fixture)],'input-check-'+name,runtime,version_env)
        for case in cases:
            observations=[]
            errors=[]
            for language,binary in (('go',a.go_binary),('rust',a.rust_binary)):
                root=runtime/case['id']/language; root.mkdir(parents=True,mode=0o700)
                env={'PATH':'','LC_ALL':'C','TZ':'UTC'}
                for key in ('HOME','USERPROFILE','XDG_CONFIG_HOME','XDG_DATA_HOME','XDG_CACHE_HOME','XDG_STATE_HOME','TMPDIR','TMP','TEMP'):
                    path=root/key; path.mkdir(mode=0o700); env[key]=str(path)
                (root/'a.pdf').write_bytes(pdf(['one','two','three','four'])); (root/'b.pdf').write_bytes(pdf(['five','six'])); (root/'bad.pdf').write_bytes(b'%PDF-1.4\ninvalid\n')
                inputs=['a.pdf','b.pdf','bad.pdf']
                if case['id']=='split-output-alias':
                    (root/'part-001.pdf').write_bytes(pdf(['one','two','three','four']))
                    inputs.append('part-001.pdf')
                if case.get('alias'):
                    alias_path=root/('parts/part-001.pdf' if case['operation']=='split' else 'alias.pdf')
                    alias_path.parent.mkdir(parents=True,exist_ok=True)
                    if case['alias']=='symlink': alias_path.symlink_to(os.path.relpath(root/'a.pdf',alias_path.parent))
                    if case['alias']=='hardlink': os.link(root/'a.pdf',alias_path)
                before={name:hashlib.sha256((root/name).read_bytes()).hexdigest() for name in inputs}
                request={k:v for k,v in case.items() if k not in ('id','expected','alias')}
                request.update(layer='api',tools={key:paths[name] for key,name in zip(TOOLS,NAMES)})
                label=case['id']+'-'+language
                response=json.loads(run([str(binary)],label,root,env,json.dumps(request).encode())[0])
                preserved=before=={name:hashlib.sha256((root/name).read_bytes()).hexdigest() for name in before}
                (out/(label+'.input-hashes.json')).write_text(json.dumps({'before':before,'after':{name:hashlib.sha256((root/name).read_bytes()).hexdigest() for name in before}},indent=2)+'\n')
                artifacts=[]
                outputs=response['result'] if isinstance(response['result'],list) else ([str(root/case['output'])] if (root/case['output']).is_file() else [])
                for i,path in enumerate(outputs):
                    inspection=label+f'-pdf-{i}'
                    raw,exit_code=run([paths['qpdf'],'--json','--json-stream-data=inline',path],inspection,root,env,check=False)
                    # qpdf exit 3 means warnings, NOT a clean validation. Continue
                    # diagnostic parsing but preserve the exit and keep safety red.
                    if exit_code: report['validation_warnings'].append({'label':inspection,'exit':exit_code})
                    semantic=json.loads(raw) if exit_code in (0,3) else {}
                    # qpdf/pdfunite allocate document IDs. Only /ID is excluded;
                    # all page objects, streams, fonts and metadata stay compared.
                    for block in semantic.get('qpdf',[]):
                        if isinstance(block,dict) and 'trailer' in block:
                            block['trailer'].get('value',{}).pop('/ID',None)
                    text,text_exit=run([paths['pdftotext'],'-layout',path,'-'],label+f'-text-{i}',root,env,check=False)
                    if text_exit: report['validation_warnings'].append({'label':label+f'-text-{i}','exit':text_exit})
                    labels=[piece.strip() for piece in text.decode().split('\f') if piece.strip()]
                    artifacts.append({'semantic':semantic,'text':labels,'validation_exit':exit_code,'text_exit':text_exit,'validation_stderr':(out/(inspection+'.stderr')).read_text(),'text_stderr':(out/(label+f'-text-{i}.stderr')).read_text()})
                if 'expected' in case and [x['text'] for x in artifacts]!=case['expected']:
                    errors.append(language+': page order/text mismatch')
                if case['id'].startswith('malformed-') and not response['error']:
                    errors.append(language+': malformed input accepted')
                if case['id'].endswith('-alias') and not response['error']:
                    errors.append(language+': input alias accepted')
                observations.append(normalized({'response':response,'artifacts':artifacts,'input_preserved':preserved},root))
            (out/(case['id']+'.json')).write_text(json.dumps(observations,indent=2)+'\n')
            try: compare(*observations)
            except AssertionError as error: errors.append(str(error))
            report['cases'].append({'id':case['id'],'parity':not errors,'errors':errors,'input_preserved':all(o['input_preserved'] for o in observations)})
            print(report['cases'][-1],flush=True)
        report['diagnostic_complete']=[c['id'] for c in report['cases']]==[c['id'] for c in cases]
        report['parity']=all(c['parity'] for c in report['cases'])
        report['passed']=report['diagnostic_complete'] and report['parity'] and all(c['input_preserved'] for c in report['cases']) and not report['validation_warnings']
    finally:
        report['sources_after']=source_manifest(); report['sources_unchanged']=report['sources_before']==report['sources_after']
        report['binaries_unchanged']=all(hashlib.sha256(Path(p).read_bytes()).hexdigest()==expected for p,expected in report['binaries'].items())
        report['tools_unchanged']=all(hashlib.sha256(Path(t['path']).read_bytes()).hexdigest()==t['sha256'] for t in report['tools'].values())
        report['passed']=report['passed'] and report['sources_unchanged'] and report['binaries_unchanged'] and report['tools_unchanged']
        (out/'report.json').write_text(json.dumps(report,indent=2)+'\n')
        if not report['sources_unchanged']: raise RuntimeError('source changed')
    return 0 if report['passed'] else 1
if __name__=='__main__': raise SystemExit(main())
