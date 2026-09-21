'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const {PassThrough} = require('node:stream');
const {RPCClient} = require('./rpc');
const {terminalOptions} = require('./terminal');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {EventEmitter} = require('node:events');

test('RPC handles split UTF-8, notifications and out-of-order replies', async () => {
  const input = new PassThrough(), output = new PassThrough(), events = [];
  const client = new RPCClient(input,output,m => events.push(m),() => {});
  const a = client.request('first'), b = client.request('second');
  const bytes = Buffer.from(JSON.stringify({id:2,result:'🐒 Windows\\folder'})+'\n');
  const pos = bytes.indexOf(Buffer.from('🐒'));
  input.write(bytes.subarray(0,pos+1)); input.write(bytes.subarray(pos+1));
  input.write(JSON.stringify({method:'run.event',params:{text:'delta'}})+'\n');
  input.write(JSON.stringify({id:1,result:'first'})+'\n');
  assert.equal(await a,'first'); assert.equal(await b,'🐒 Windows\\folder');
  assert.equal(events[0].params.text,'delta'); client.close();
});
test('RPC rejects pending runs on disconnect and bounds ordinary calls', async () => {
  const input = new PassThrough(), output = new PassThrough();
  const client = new RPCClient(input,output,() => {},() => {},10);
  await assert.rejects(client.request('session.get'),/timed out/);
  const rejected = assert.rejects(client.request('chats.send'),/disconnected/);
  input.end(); await rejected; assert.equal(client.pending.size,0);
});
test('RPC permits Stop while a long request is pending', async () => {
  const input = new PassThrough(), output = new PassThrough();
  const client = new RPCClient(input,output,() => {},() => {});
  const run = client.request('chats.send'), stop = client.request('runs.cancel');
  input.write('{"id":2,"result":true}\n{"id":1,"error":{"message":"cancelled"}}\n');
  assert.equal(await stop,true); await assert.rejects(run,/cancelled/); client.close();
});

function extensionHarness(platform, trusted = true, descriptor = null) {
  const commands = new Map(), stored = new Map(), requests = [], spawns = [], terminals = [], errors = [];
  const cwd = platform === 'win32' ? 'C:\\Users\\Test User\\Project' : '/tmp/project with spaces';
  const executable = platform === 'win32' ? 'C:\\Program Files\\PrAImate\\praimate.exe' : '/opt/PrAImate/praimate';
  const disposable = {dispose(){}};
  let choiceID, available = ['claude','openclaude','codex','opencode','praimate-code'];
  let session = {cli:'praimate-code',tools:'safe'};
  const vscode = {
    workspace:{isTrusted:trusted,workspaceFolders:[{uri:{fsPath:cwd}}]},
    window:{
      createOutputChannel:() => ({append(){},appendLine(){},dispose(){}}),
      createStatusBarItem:() => ({show(){},dispose(){}}),
      registerWebviewViewProvider:(_,p) => {vscode.provider=p;return disposable;},
      registerTreeDataProvider:() => disposable,
      onDidChangeActiveTextEditor:() => disposable,onDidChangeTextEditorSelection:() => disposable,
      showErrorMessage:m => errors.push(m),createTerminal:o => {terminals.push(o);return {show(){}};}
      ,showQuickPick:async items => items.find(item => item.id === choiceID)
    },
    commands:{registerCommand:(id,fn) => {commands.set(id,fn);return disposable;},executeCommand:() => {}},
    EventEmitter:class{constructor(){this.event=()=>{};}fire(){}dispose(){}},
    TreeItem:class{constructor(label){this.label=label;}},StatusBarAlignment:{Left:0}
  };
  const spawn = (exe,args,opts) => {
    spawns.push({exe,args,opts});
    const child = new EventEmitter();
    child.stdout = new PassThrough();child.stdin = new PassThrough();child.stderr = new PassThrough();
    child.kill = () => {child.stdout.end();};
    let buffer = '';
    child.stdin.on('data',chunk => {
      buffer += chunk.toString();
      while (buffer.includes('\n')) {
        const end = buffer.indexOf('\n'), req = JSON.parse(buffer.slice(0,end));
        buffer = buffer.slice(end+1);requests.push(req);
        let result = {};
        if (req.method === 'system.initialize') result = {serverVersion:'test'};
        if (req.method === 'session.update') { session = {...session,...req.params}; result = session; }
        if (['agents.list','models.list','clis.list'].includes(req.method)) result = [];
        if (req.method === 'chats.create') result = {ID:'chat-1'};
        if (req.method === 'chats.send') result = {chatId:'chat-1',reply:'reply'};
        if (req.method === 'chats.messages') result = [{Role:'user',Content:'hello'},{Role:'assistant',Content:'reply'}];
        if (req.method === 'terminals.list') result = available.map(id => ({id,label:id,available:true}));
        if (req.method === 'terminals.prepare') {
          const cli = req.params.cli || session.cli;
          result = {cli,cwd,command:platform === 'win32' ? 'C:\\CLI Tools\\'+cli+'.exe' : '/CLI Tools/'+cli,
            args:cli === session.cli && session.model ? [cli === 'codex' ? '-m' : '--model',session.model] : [],env:{PATH:'/managed/runtime'}};
        }
        queueMicrotask(() => child.stdout.write(JSON.stringify({id:req.id,result})+'\n'));
      }
    });
    return child;
  };
  let watch;
  const mockFS = {...fs,readFileSync:(file,...args) => file.endsWith('connection.json') && descriptor ? JSON.stringify(descriptor) : fs.readFileSync(file,...args),
    watchFile:(_,opts,fn) => {watch=fn;},unwatchFile(){}};
  const sandbox = {require:name => name==='vscode'?vscode:name==='node:fs'?mockFS:name==='node:child_process'?{spawn}:require(name),
    module:{exports:{}},process:{platform,env:{PRAIMATE_BIN:executable}},console,setTimeout,clearTimeout,Buffer};
  vm.runInNewContext(fs.readFileSync(path.join(__dirname,'extension.js'),'utf8'),sandbox);
  const ctx = {subscriptions:[],extensionPath:__dirname,extensionUri:{fsPath:__dirname},
    workspaceState:{get:(key,fallback) => stored.has(key)?stored.get(key):fallback,update:async(key,v)=>stored.set(key,v)}};
  sandbox.module.exports.activate(ctx);
  return {commands,requests,spawns,terminals,errors,cwd,executable,stored,provider:()=>vscode.provider,
    choose:id=>{choiceID=id;},setAvailable:ids=>{available=ids;},vscode,
    changeDescriptor:next => {descriptor=next;watch({mtimeMs:2,size:100},{mtimeMs:1,size:100});},
    close:() => sandbox.module.exports.deactivate()};
}
for (const platform of ['linux','win32']) {
  test(platform+': launches stdio with spaced paths and persists chat identity', async () => {
    const h = extensionHarness(platform);
    try {
      await new Promise(resolve => setImmediate(resolve));
      assert.equal(h.spawns.length,1); assert.equal(h.spawns[0].exe,h.executable);
      assert.equal(JSON.stringify(h.spawns[0].args),'["serve","--stdio"]');
      assert.equal(h.spawns[0].opts.shell,false); assert.equal(h.spawns[0].opts.windowsHide,true);
      assert.equal(h.spawns[0].opts.cwd,h.cwd);
      await h.provider().send('hello',false); await h.provider().send('again',false);
      assert.equal(h.requests.filter(r => r.method==='chats.create').length,1);
      const sends = h.requests.filter(r => r.method==='chats.send');
      assert.equal(sends.length,2); assert.equal(sends[1].params.chatId,'chat-1');
      await h.commands.get('praimate.openTerminal')();
      assert.equal(h.terminals[0].shellPath,platform === 'win32' ? 'C:\\CLI Tools\\praimate-code.exe' : '/CLI Tools/praimate-code');
      assert.equal(JSON.stringify(h.terminals[0].shellArgs),'[]');
      assert.deepEqual(h.errors,[]);
    } finally {h.close();}
  });
}
test('untrusted workspaces never spawn the backend', () => {
  const h = extensionHarness('win32',false);
  try{assert.equal(h.spawns.length,0);}finally{h.close();}
});
for (const platform of ['linux','win32']) {
  test(platform+': selected CLI and picker launch native terminals without changing chat settings',async () => {
    const cwd = platform === 'win32' ? 'C:\\Users\\Test User\\Project' : '/tmp/project with spaces';
    const h = extensionHarness(platform,true,{launchId:'terminal-test',config:{workspace:cwd,cli:'codex',model:'custom-model'}});
    try {
      await new Promise(resolve=>setImmediate(resolve));
      await h.commands.get('praimate.openTerminal')();
      assert.deepEqual(Array.from(h.terminals[0].shellArgs),['-m','custom-model']);
      assert.match(h.terminals[0].shellPath,/codex(?:\.exe)?$/);
      const before = h.requests.filter(r=>r.method==='session.update').length;
      for (const cli of ['claude','openclaude','opencode','praimate-code']) {
        h.choose(cli); await h.commands.get('praimate.chooseTerminal')();
        const term = h.terminals.at(-1);
        assert.equal(term.name,'PrAImate · '+cli); assert.equal(term.cwd,cwd);
        assert.equal(term.shellArgs.length,0,'another CLI must not inherit Codex model');
        assert.equal(term.env.PRAIMATE_STUDIO_TOKEN,null);
      }
      assert.equal(h.requests.filter(r=>r.method==='session.update').length,before);
      h.choose(undefined); await h.commands.get('praimate.chooseTerminal')();
      assert.equal(h.terminals.length,5,'cancel must not launch');
      h.setAvailable([]); await h.commands.get('praimate.chooseTerminal')();
      assert.match(h.errors.at(-1),/No CLI executables/);
    } finally {h.close();}
  });
}
test('terminal commands fail closed in untrusted workspaces',async () => {
  const h=extensionHarness('win32',false);
  try {await h.commands.get('praimate.openTerminal')(); await h.commands.get('praimate.chooseTerminal')(); assert.equal(h.terminals.length,0); assert.equal(h.requests.length,0);}
  finally {h.close();}
});
test('Windows batch launch quotes spaced paths, disables AutoRun and rejects shell expansion',() => {
  const plan={cli:'claude',cwd:'C:\\Project Folder',command:'C:\\CLI Tools\\claude.cmd',args:['--model','model with spaces'],env:{PATH:'managed'}};
  const options=terminalOptions(plan,'win32',{SystemRoot:'C:\\Windows'});
  assert.equal(options.shellPath,'C:\\Windows\\System32\\cmd.exe');
  assert.equal(options.shellArgs,'/d /s /v:off /c ""C:\\CLI Tools\\claude.cmd" "--model" "model with spaces""');
  for(const value of ['a&whoami','%PATH%','!VAR!','a"b','a\nb','a|b','a>b','a^b']) {
    assert.throws(()=>terminalOptions({...plan,args:['--model',value]},'win32',{}),/cannot safely quote/);
  }
  const native=terminalOptions({...plan,command:'C:\\CLI Tools\\claude.exe',args:['--model','literal & value']},'win32',{});
  assert.deepEqual(native.shellArgs,['--model','literal & value']);
});
test('private launch descriptor overrides stale environment and preserves edits on reconnect', async () => {
  const descriptor = {backend:'/new backend/praimate',launchId:'first',config:{workspace:'/tmp/project with spaces',cli:'claude',model:'launch-model'}};
  const h = extensionHarness('linux',true,descriptor);
  try {
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(h.spawns[0].exe,descriptor.backend);
    assert.equal(h.requests.find(r => r.method==='session.update').params.model,'launch-model');
    h.stored.set('session',{cli:'claude',model:'user-edited',workspace:h.cwd});
    await h.commands.get('praimate.reconnect')();
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(h.requests.filter(r => r.method==='session.update').at(-1).params.model,'user-edited');
    h.changeDescriptor({...descriptor,backend:'/restarted/praimate',launchId:'second'});
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(h.spawns.at(-1).exe,'/restarted/praimate');
    assert.equal(h.requests.filter(r => r.method==='session.update').at(-1).params.model,'launch-model');
  } finally {h.close();}
});

function webviewHarness() {
  const html = fs.readFileSync(path.join(__dirname,'resources/chat.html'),'utf8');
  const script = /<script>([\s\S]*?)<\/script>/.exec(html)[1];
  const elements = new Map();
  const element = tag => ({tag,dataset:{},listeners:{},value:'',checked:true,disabled:false,children:[],innerHTML:'',textContent:'',className:'',
    scrollHeight:0,scrollTop:0,clientHeight:0,setAttribute(name,value){this[name]=value;},
    addEventListener(name,fn){this.listeners[name]=fn;},appendChild(v){this.children.push(v);},replaceChildren(...v){this.children=v;this.innerHTML='';},
    fire(name,event={}){this.listeners[name]?.(event);}});
  const document = {getElementById:id => {if(!elements.has(id)) elements.set(id,element());return elements.get(id);},createElement:element};
  const posted = [], listeners = {};
  const sandbox = {document,window:{addEventListener:(name,fn)=>listeners[name]=fn},Option:function(text,value){this.text=text;this.value=value;},
    acquireVsCodeApi:()=>({postMessage:m=>posted.push(m)}),navigator:{clipboard:{writeText:async()=>{}}}};
  vm.createContext(sandbox);vm.runInContext(script,sandbox);
  return {elements,posted,send:data=>listeners.message({data}),eval:code=>vm.runInContext(code,sandbox)};
}
test('webview renders escaped prose and code using text nodes and clears old history', () => {
  const h = webviewHarness(); assert.equal(h.posted[0].type,'ready');
  h.send({type:'messages',messages:[{role:'assistant',content:'line one\nline two <img src=x>\n'+'```js\n<script>alert(1)</script>\n```'}]});
  const rendered = h.elements.get('messages').children[0];
  assert.match(rendered.children[1].innerHTML,/line one<br>line two &lt;img/);
  const box = rendered.children.find(e=>e.className==='code-box');
  assert.equal(box.children[1].tag,'pre'); assert.equal(box.children[1].textContent,'<script>alert(1)</script>\n');
  assert.equal(box.children[1].innerHTML,'');
  h.send({type:'messages',messages:[]});
  assert.equal(h.elements.get('messages').children.length,1);
  assert.equal(h.elements.get('messages').children[0].className,'welcome');
});
test('status updates preserve unsaved settings; errors preserve the draft and offer recovery', () => {
  const h = webviewHarness();
  const status = {connected:true,activeCLI:'claude',activeModel:'original',activeTools:'safe',availableCLIs:[{id:'claude',label:'Claude',available:true,capabilities:{toolLevels:['','ask']}}]};
  h.send({type:'status',status});
  h.elements.get('model-input').value='unsaved-model'; h.elements.get('session-form').fire('input');
  h.send({type:'status',status:{...status,models:['new-model']}});
  assert.equal(h.elements.get('model-input').value,'unsaved-model');
  h.elements.get('prompt-input').value='keep my prompt'; h.elements.get('send-btn').fire('click');
  const request = h.posted.at(-1); assert.equal(request.type,'send');
  h.send({type:'actionResult',id:request.requestId,ok:false,error:'backend disconnected'});
  assert.equal(h.elements.get('prompt-input').value,'keep my prompt');
  assert.equal(h.elements.get('notice').textContent,'backend disconnected');
  h.send({type:'status',status:{...status,connected:false,error:'Reopen Desktop'}});
  assert.equal(h.elements.get('connection').hidden,false); assert.equal(h.elements.get('send-btn').disabled,true);
  h.elements.get('reconnect-btn').fire('click'); assert.equal(h.posted.at(-1).type,'reconnect');
});
test('pending CLI detection preserves the persisted permission level on save', () => {
  const h = webviewHarness();
  h.send({type:'status',status:{connected:true,activeCLI:'codex',activeModel:'old',activeTools:'full',availableCLIs:[]}});
  assert.equal(h.elements.get('tools-select').value,'full');
  h.elements.get('model-input').value='new'; h.elements.get('session-form').fire('input');
  h.elements.get('session-form').fire('submit',{preventDefault(){}});
  const request = h.posted.at(-1);
  assert.equal(request.type,'updateConfig'); assert.equal(request.config.tools,'full');
});
test('single navigation loads collections and exposes contextual actions', () => {
  const h = webviewHarness(); h.send({type:'status',status:{connected:true,activeCLI:'claude'}});
  h.eval("navigate('history')"); assert.equal(h.posted.at(-1).name,'history');
  h.send({type:'collection',name:'history',items:[{id:'chat-42',title:'Persisted chat',cli:'claude'}]});
  const actions = h.elements.get('catalog-items').children[0].children.at(-1).children;
  assert.deepEqual(actions.map(a=>a.textContent),['Resume','Rename','Delete']);
  actions[0].fire('click'); assert.equal(h.posted.at(-1).id,'chat-42');
  const req=h.posted.at(-1); h.send({type:'actionResult',id:req.requestId,ok:true});
  assert.equal(h.elements.get('chat-page').hidden,false);
  const manifest=JSON.parse(fs.readFileSync(path.join(__dirname,'package.json'),'utf8'));
  assert.equal(manifest.contributes.views['praimate-sidebar'].length,1);
});
