#!/usr/bin/env -S uv run --script
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright 2026 Agenxy
# /// script
# requires-python = ">=3.12"
# dependencies = []
# ///
"""Bounded, model-free installed harness tool-list change measurement."""
import json
import os
from pathlib import Path
import queue
import shutil
import subprocess
import sys
import tempfile
import threading
import time

SCRIPT = Path(__file__).resolve()
ROOT = Path(tempfile.gettempdir())
APP = '/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex'

def server(root):
    root = Path(root)
    log = root / 'server.jsonl'
    sent = False
    inputs = queue.Queue()
    def read():
        for line in sys.stdin: inputs.put(line)
        inputs.put(None)
    threading.Thread(target=read,daemon=True).start()
    def emit(value):
        print(json.dumps(value), flush=True)
    until = time.monotonic() + 60
    while time.monotonic() < until:
        if (root / 'notify').exists() and not sent:
            emit({'jsonrpc':'2.0','method':'notifications/tools/list_changed'})
            with log.open('a') as f: f.write(json.dumps({'pid':os.getpid(),'method':'NOTIFIED'})+'\n')
            sent = True
        try: line=inputs.get(timeout=.025)
        except queue.Empty: continue
        if not line: return
        req = json.loads(line)
        method = req.get('method')
        with log.open('a') as f: f.write(json.dumps({'pid':os.getpid(),'method':method,'version':(root/'version').read_text()})+'\n')
        if 'id' not in req: continue
        if method == 'initialize':
            result = {'protocolVersion':req['params']['protocolVersion'],'capabilities':{'tools':{'listChanged':True}},'serverInfo':{'name':'bounded-tool-change-fixture','version':'1'}}
        elif method == 'tools/list':
            result = {'tools':[{'name':'version_'+(root/'version').read_text(),'description':'Synthetic probe, never called','inputSchema':{'type':'object','properties':{}}}]}
        elif method in ('resources/list','resources/templates/list'):
            result = {'resources':[]} if method=='resources/list' else {'resourceTemplates':[]}
        elif method == 'ping': result = {}
        else:
            emit({'jsonrpc':'2.0','id':req['id'],'error':{'code':-32601,'message':'fixture method unavailable'}})
            continue
        emit({'jsonrpc':'2.0','id':req['id'],'result':result})

class Process:
    def __init__(self, args, root, env):
        self.log = (root/'stderr.log').open('w')
        self.p = subprocess.Popen(args, cwd=root, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=self.log, text=True, bufsize=1, start_new_session=True)
        self.q = queue.Queue()
        self.events=[]
        def read():
            for line in self.p.stdout:
                try: value=json.loads(line)
                except ValueError: value={'unparsed':line[:200]}
                self.q.put(value)
        threading.Thread(target=read,daemon=True).start()
    def send(self, value):
        self.p.stdin.write(json.dumps(value)+'\n'); self.p.stdin.flush()
    def until(self, predicate, timeout=20):
        end=time.monotonic()+timeout
        while time.monotonic()<end:
            try: value=self.q.get(timeout=min(.25,max(.01,end-time.monotonic())))
            except queue.Empty:
                if self.p.poll() is not None: raise RuntimeError('process exited '+str(self.p.returncode))
                continue
            self.events.append(value)
            if predicate(value): return value
        raise TimeoutError('bounded reply wait')
    def rpc(self, method, params=None):
        ident=len(self.events)+100
        self.send({'id':ident,'method':method,'params':params})
        result=self.until(lambda x:x.get('id')==ident)
        if 'error' in result: raise RuntimeError(result['error'])
        return result.get('result')
    def close(self):
        import signal
        if self.p.poll() is None:
            os.killpg(self.p.pid,signal.SIGTERM)
            try: self.p.wait(timeout=3)
            except subprocess.TimeoutExpired:
                os.killpg(self.p.pid,signal.SIGKILL);self.p.wait(timeout=3)
        self.log.close()

def logs(root):
    p=root/'server.jsonl'
    return [json.loads(x) for x in p.read_text().splitlines()] if p.exists() else []

def wait_setup(root):
    end=time.monotonic()+20
    while time.monotonic()<end:
        if any(x['method']=='tools/list' for x in logs(root)): return
        time.sleep(.05)
    raise RuntimeError('setup failed: fixture never listed')

def run(kind):
    root=Path(tempfile.mkdtemp(prefix=kind+'-',dir=ROOT))
    (root/'version').write_text('one')
    ch=root/'codex';ch.mkdir()
    env=dict(os.environ,HOME=str(root),CODEX_HOME=str(ch),CLAUDE_CONFIG_DIR=str(root/'claude'))
    for k in ('ANTHROPIC_API_KEY','OPENAI_API_KEY','CLAUDE_CODE_OAUTH_TOKEN','CODEX_THREAD_ID'):
        env.pop(k,None)
    if kind.startswith('codex'):
        (ch/'config.toml').write_text('[mcp_servers.fixture]\ncommand='+json.dumps(sys.executable)+'\nargs='+json.dumps([str(SCRIPT),'server',str(root)])+'\n')
        args=[APP]
        if kind=='codex-enabled': args+=['--enable','mcp_tool_list_changed']
        args+=['app-server']
    else:
        config=root/'mcp.json'
        config.write_text(json.dumps({'mcpServers':{'fixture':{'command':sys.executable,'args':[str(SCRIPT),'server',str(root)]}}}))
        args=[shutil.which('claude') or 'claude','--bare','--print','--input-format','stream-json','--output-format','stream-json','--verbose','--strict-mcp-config','--mcp-config',str(config),'--settings','{"disableAllHooks":true}']
    p=Process(args,root,env)
    receipt={'kind':kind,'root':str(root),'model_calls':0}
    try:
        if kind.startswith('codex'):
            receipt['initialize']=p.rpc('initialize',{'clientInfo':{'name':'dibs_catalog_probe','version':'1'},'capabilities':{'experimentalApi':True}})
            p.send({'method':'initialized'})
            thread=p.rpc('thread/start',{'cwd':str(root),'approvalPolicy':'never','sandbox':'read-only'})
            tid=thread['thread']['id'];receipt['thread']=tid
            receipt['before']=p.rpc('mcpServerStatus/list',{'threadId':tid,'serverName':'fixture','detail':'toolsAndAuthOnly'})
        else:
            p.send({'type':'control_request','request_id':'init-probe','request':{'subtype':'initialize'}})
            init=p.until(lambda x:x.get('type')=='control_response' and x.get('response',{}).get('request_id')=='init-probe')
            receipt['initialize']={'type':init['type'],'subtype':init['response']['subtype']}
            p.send({'type':'control_request','request_id':'before-probe','request':{'subtype':'mcp_status'}})
            receipt['before']=p.until(lambda x:x.get('type')=='control_response' and x.get('response',{}).get('request_id')=='before-probe')
        wait_setup(root)
        receipt['setup_logs']=logs(root)
        assert any(x['method']=='initialize' for x in receipt['setup_logs'])
        assert not any(x['method']=='tools/call' for x in receipt['setup_logs'])
        (root/'version').write_text('two');(root/'notify').touch()
        # Fixed bounded observation, no product polling or model turn.
        time.sleep(2)
        receipt['notification_logs']=logs(root)
        if kind.startswith('codex'):
            receipt['after']=p.rpc('mcpServerStatus/list',{'threadId':tid,'serverName':'fixture','detail':'toolsAndAuthOnly'})
        else:
            p.send({'type':'control_request','request_id':'status-probe','request':{'subtype':'mcp_status'}})
            receipt['after']=p.until(lambda x:x.get('type')=='control_response' and x.get('response',{}).get('request_id')=='status-probe')
        receipt['final_logs']=logs(root)
    except Exception as e:
        receipt['error']=str(e);receipt['final_logs']=logs(root)
    finally:
        p.close()
        (root/'events.json').write_text(json.dumps(p.events,indent=2))
        (root/'receipt.json').write_text(json.dumps(receipt,indent=2))
    print(json.dumps(receipt),flush=True)
    return 'error' not in receipt

if __name__=='__main__':
    if sys.argv[1]=='server': server(sys.argv[2])
    else: sys.exit(0 if run(sys.argv[1]) else 1)
