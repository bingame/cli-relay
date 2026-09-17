"""在隔离目录和本地假服务上验证 Codex，不使用真实凭据或模型。"""
import http.server
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading

binary = next((Path(os.environ['APPDATA']) / 'npm/node_modules/@openai').rglob('codex.exe'))
seen = []
class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def do_POST(self):
        self.rfile.read(int(self.headers.get('Content-Length', 0)))
        seen.append(self.headers.get('Authorization') == 'Bearer relay-probe-dummy')
        self.send_response(200)
        self.send_header('Content-Type', 'text/event-stream')
        self.end_headers()
        response = {'id':'resp_probe','object':'response','status':'completed','output':[{'id':'msg_probe','type':'message','role':'assistant','status':'completed','content':[{'type':'output_text','text':'PROBE_OK','annotations':[]}]}],'usage':{'input_tokens':1,'output_tokens':1,'total_tokens':2}}
        events = [{'type':'response.created','response':{**response,'status':'in_progress','output':[]}}, {'type':'response.output_item.added','output_index':0,'item':response['output'][0]}, {'type':'response.output_text.delta','item_id':'msg_probe','output_index':0,'content_index':0,'delta':'PROBE_OK'}, {'type':'response.output_item.done','output_index':0,'item':response['output'][0]}, {'type':'response.completed','response':response}]
        for e in events:
            self.wfile.write(('event: '+e['type']+'\ndata: '+json.dumps(e)+'\n\n').encode())

server=http.server.ThreadingHTTPServer(('127.0.0.1',0),Handler)
threading.Thread(target=server.serve_forever,daemon=True).start()
with tempfile.TemporaryDirectory(prefix='relay-probe-') as directory:
    root=Path(directory); env=os.environ.copy()
    env['CODEX_HOME']=str(root); env['RELAY_PROBE_KEY']='relay-probe-dummy'
    for name in list(env):
        if name in ('OPENAI_API_KEY','OPENAI_BASE_URL','ANTHROPIC_AUTH_TOKEN','ANTHROPIC_API_KEY'): env.pop(name,None)
    provider='[model_providers.relay_probe]\nname="Relay probe"\nbase_url="http://127.0.0.1:'+str(server.server_port)+'/v1"\nenv_key="RELAY_PROBE_KEY"\nwire_api="responses"\nrequest_max_retries=0\nstream_max_retries=0\n'
    (root/'config.toml').write_text(provider,encoding='utf-8')
    (root/'relay-probe.config.toml').write_text('model_provider="relay_probe"\nmodel="relay-probe-model"\n',encoding='utf-8')
    cases=[('override-hidden-from-shell',['-c','model_provider="relay_probe"','-c','model="relay-probe-model"','-c','shell_environment_policy.inherit="none"','-c','shell_environment_policy.include_only=["PATH"]']),('profile-file',['--profile','relay-probe']),('filters',['-c','model_provider="relay_probe"','-c','model="relay-probe-model"','-c','shell_environment_policy.filters={"RELAY_*"="exclude"}'])]
    for name,args in cases:
        seen.clear()
        try:
            p=subprocess.run([str(binary),*args,'exec','--skip-git-repo-check','--ephemeral','--json','Reply PROBE_OK without tools.'],cwd=root,env=env,capture_output=True,text=True,encoding='utf-8',timeout=40)
            print(json.dumps({'case':name,'exit':p.returncode,'auth_received':any(seen),'completed':'turn.completed' in p.stdout,'diagnostic':p.stderr[-700:] if p.returncode else ''}))
        except subprocess.TimeoutExpired: print(json.dumps({'case':name,'timeout':True,'auth_received':any(seen)}))
server.shutdown()
