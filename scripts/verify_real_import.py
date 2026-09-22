"""用用户指定 dump 在临时目录验证完整 CLI 导入；只打印计数，不调用模型。"""
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile

binary=Path(sys.argv[1]).resolve()
source=Path(sys.argv[2]).resolve()
with tempfile.TemporaryDirectory(prefix='relay-import-verify-') as directory:
    root=Path(directory)/'relay'
    env=os.environ.copy()
    env['RELAY_PASSPHRASE']='temporary-local-import-verification-only'
    def call(*args):
        p=subprocess.run([str(binary),'--home',str(root),*args],env=env,capture_output=True,text=True,encoding='utf-8')
        if p.returncode: raise RuntimeError('验证命令失败：'+args[0]+'，退出码 '+str(p.returncode))
        return json.loads(p.stdout)
    dry=call('provider','import','--from','cc-switch',str(source),'--dry-run')
    assert not root.exists(),'dry-run产生副作用'
    report=call('provider','import','--from','cc-switch',str(source))
    assert len(call('provider','list'))==report['count']
    assert not (root/'current.json').exists(),'导入自动switch'
    renders=0; checked_secrets=[]
    for row in report['providers']:
        target = row['target']
        if target not in ('codex','claude'): continue
        args=call('provider','render-args',target,row['id'])
        values=call('provider','render-env',target,row['id'],'--format','json')
        sensitive=[value for key,value in values.items() if len(value)>=8 and ('KEY' in key or 'TOKEN' in key or 'AUTH' in key or 'HEADER' in key)]
        for value in sensitive:
            assert value not in json.dumps(args,ensure_ascii=False),'凭据进入argv'
            checked_secrets.append(value.encode())
        renders+=1
    for path in root.rglob('*'):
        if path.is_file():
            content=path.read_bytes()
            for value in checked_secrets:
                assert value not in content,'凭据明文落盘'
    db=sqlite3.connect(root/'providers.db')
    counts={'providers':db.execute('select count(*) from providers').fetchone()[0], 'encrypted_secret_rows':db.execute('select count(*) from provider_secrets').fetchone()[0], 'encrypted_snapshots':db.execute('select count(*) from import_log where length(raw_snapshot)>0').fetchone()[0]}
    db.close()
    print(json.dumps({'dry_run_no_writes':True,'rendered_targets':renders,'no_plaintext_secret_files':True,**counts}))
