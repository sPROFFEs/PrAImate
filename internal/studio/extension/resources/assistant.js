// Shared desktop Assistant. Model output is always rendered as text.
(() => {
  const requests = new Map(), approvals = new Map();
  let config = null, snapshot = {messages:[]}, busy = false, available = false, loading = false, connectionChecked = false;
  const draft = $('assistant-prompt'), error = $('assistant-error');
  draft.value = saved.assistantDraft || '';
  draft.addEventListener('input',() => vscode.setState?.({...vscode.getState?.(),assistantDraft:draft.value}));
  function fail(e) { error.textContent = String(e?.message || e); error.hidden = !error.textContent; }
  function rpc(method,params={}) {
    return new Promise((resolve,reject) => {
      const id = post('assistantRPC',{method,params}); requests.set(id,{resolve,reject});
    });
  }
  function uiContext() { return {page:page === 'assistant' ? 'dashboard' : page === 'chat' ? 'chats' : page,chat_id:status.chatId || '',project:status.workspace || '',worker_id:workerSelected || ''}; }
  function render() {
    const root = $('assistant-messages'), atBottom = root.scrollHeight-root.scrollTop-root.clientHeight < 80;
    root.replaceChildren();
    for(const message of snapshot.messages || []) { const card=element('article','','msg '+message.role);card.append(element('div',message.role === 'user' ? 'You' : 'Assistant','role'),element('p',message.text));root.appendChild(card); }
    if(!root.children.length) root.appendChild(element('p','Find a conversation, manage an agent or delegate work. Enable the local assistant in Preferences to get started.','empty'));
    $('assistant-steps').replaceChildren();
    for(const step of snapshot.task?.steps || []) {
      const row=element('details','','card'), title=element('summary',step.action+' · '+step.status);row.appendChild(title);
      if(step.detail) row.appendChild(element('p',step.detail));
      if(step.run_id?.startsWith('worker-')) row.appendChild(button('Open worker execution',() => {workerSelected=step.run_id;navigate('workers');post('workerRefresh',{id:step.run_id});}));
      $('assistant-steps').appendChild(row);
    }
    busy = snapshot.task?.status === 'running';
    $('assistant-send').disabled = !available || !config?.enabled || busy;
    $('assistant-stop').hidden = !busy;
    draft.disabled = busy;
    if(atBottom) root.scrollTop=root.scrollHeight;
  }
  async function load() {
    if(!status.connected || loading) return;
    loading=true;connectionChecked=true;
    try {
      available = await rpc('assistant.available');
      if(!available) throw Error('Open Studio from PrAImate Desktop to use Assistant & Voice. The desktop window can remain hidden.');
      [config,snapshot] = await Promise.all([rpc('assistant.config'),rpc('assistant.snapshot')]);
      render(); renderSettings(); updateVoiceButtons(); fail('');
    } catch(e) {fail(e);} finally {loading=false;}
  }
  async function sendAssistant() {
    if(busy || !config?.enabled || !draft.value.trim()) return;
    const message=draft.value;draft.value='';draft.dispatchEvent(new Event('input'));busy=true;error.hidden=true;
    snapshot={...snapshot,task:{status:'running',steps:[]},messages:[...(snapshot.messages || []),{role:'user',text:message}]};render();
    try {snapshot=await rpc('assistant.send',{message,context:uiContext()});}
    catch(e) {fail(e);try{snapshot=await rpc('assistant.snapshot');}catch{} }
    finally {busy=false;render();}
  }
  $('assistant-send').onclick=sendAssistant;
  draft.onkeydown=e=>{if(e.key==='Enter' && !e.shiftKey && !e.isComposing){e.preventDefault();sendAssistant();}};
  $('assistant-stop').onclick=()=>rpc('assistant.cancel').catch(fail);
  $('assistant-refresh').onclick=load;
  $('assistant-monkey').onclick=()=>{navigate('assistant');draft.focus();};
  $('assistant-settings-toggle').onclick=()=>{$('assistant-settings').hidden=!$('assistant-settings').hidden;};
  window.addEventListener('assistant-open',load);
  function field(root,label,value,change,type='text',choices=null) {
    const wrap=element('label',label,'field'), input=document.createElement(choices?'select':'input');
    if(choices) for(const [id,title] of choices) {const option=element('option',title);option.value=id;input.appendChild(option);}
    else input.type=type;
    if(type==='checkbox')input.checked=!!value;else input.value=value ?? '';
    input.onchange=()=>change(type==='checkbox'?input.checked:type==='number'?Number(input.value):input.value);
    wrap.appendChild(input);root.appendChild(wrap);return input;
  }
  function group(title) {const details=element('details','','section');details.appendChild(element('summary',title));$('assistant-settings').appendChild(details);return details;}
  function renderSettings() {
    if(!config)return;
    const root=$('assistant-settings');root.replaceChildren();
    const general=group('Assistant & model');general.open=true;
    field(general,'Enable Assistant',config.enabled,v=>config.enabled=v,'checkbox');
    field(general,'Model profile',config.model_id,v=>{config.model_id=v;if(v!=='existing'){config.context=v==='qwen-quality'?4096:2048;config.output=v==='qwen-quality'?768:512;}renderSettings();},'text',[['lfm-efficient','LFM2.5 350M · Efficient'],['qwen-quality','Qwen3.5 0.8B · Quality'],['existing','Existing local endpoint']]);
    if(config.model_id==='existing'){field(general,'Endpoint',config.endpoint,v=>config.endpoint=v);field(general,'Model',config.model,v=>config.model=v);general.appendChild(element('p','Use an existing Local Model profile; its saved authentication is resolved by PrAImate.','hint'));}
    field(general,'Open shortcut',config.shortcut,v=>config.shortcut=v);
    field(general,'Start model with app',config.start_with_app,v=>config.start_with_app=v,'checkbox');
    field(general,'Keep model loaded',config.keep_loaded,v=>config.keep_loaded=v,'checkbox');
    const voice=group('Voice Input · independent from Assistant');
    field(voice,'Enable Voice Input',config.voice.enabled,v=>config.voice.enabled=v,'checkbox');
    field(voice,'Speech model',config.voice.model_id,v=>config.voice.model_id=v,'text',[['whisper-tiny','Tiny · fastest'],['whisper-base','Base · balanced'],['whisper-small','Small · higher accuracy']]);
    field(voice,'Language (auto or language code)',config.voice.language,v=>config.voice.language=v);
    field(voice,'CPU threads',config.voice.threads,v=>config.voice.threads=v,'number');
    field(voice,'Dictation shortcut',config.voice.shortcut,v=>config.voice.shortcut=v);
    field(voice,'Keep speech model loaded',config.voice.keep_loaded,v=>config.voice.keep_loaded=v,'checkbox');
    field(voice,'Send transcript automatically',config.voice.auto_send,v=>config.voice.auto_send=v,'checkbox');
    voice.appendChild(element('p','Hold Dictate to record locally, then release. Audio is discarded after transcription. Transcripts remain editable unless auto-send is enabled.','hint'));
    const runtime=group('Runtime & execution limits');
    for(const [key,label] of Object.entries({context:'Context tokens',output:'Output tokens',threads:'CPU threads',gpu_layers:'GPU layers',batch:'Batch size',temperature:'Temperature',top_p:'Top P',top_k:'Top K',repeat_penalty:'Repeat penalty',idle_seconds:'Unload after seconds',max_turns:'Maximum turns',max_actions:'Maximum actions',max_failures:'Maximum failures',max_delegations:'Maximum delegations',command_seconds:'Command timeout seconds'})) if(key in config) field(runtime,label,config[key],v=>config[key]=v,'number');
    const perms=group('Independent action permissions');
    for(const [key,value] of Object.entries(config.permissions || {}))field(perms,key.replaceAll('_',' '),value,v=>config.permissions[key]=v,'text',[['deny','Deny'],['ask','Ask'],['allow','Allow']]);
    const actions=element('div','','toolbar');
    actions.append(button('Save preferences',async()=>{try{await rpc('assistant.configure',{config});await load();}catch(e){fail(e);}}),button('Test model',()=>rpc('assistant.health').then(()=>{$('assistant-phase').textContent='Model ready';}).catch(fail)),button('Clear conversation',async()=>{try{await rpc('assistant.clear');await load();}catch(e){fail(e);}}));root.appendChild(actions);
    const packages=group('Local model packages');packages.appendChild(button('Load package catalogue',()=>loadPackages(packages)));
    const activity=group('Encrypted activity');for(const item of (snapshot.activity || []).slice(-30).reverse())activity.appendChild(element('p',item.action+' · '+item.status+' · '+new Date(item.at).toLocaleString(),'hint'));
  }
  async function loadPackages(root) {
    try {
      const catalogue=await rpc('assistant.artifacts');root.replaceChildren(element('summary','Local model packages'));root.open=true;
      if(catalogue.notice)root.appendChild(element('p',catalogue.notice,'hint'));
      root.appendChild(button('Cancel download',()=>rpc('assistant.artifacts.cancel').catch(fail)));
      for(const item of catalogue.items || []) {
        const card=element('div','','card');card.append(element('strong',item.name),element('p',item.description,'hint'));
        const run=async action=>{try{await rpc('assistant.artifacts.'+action,{id:item.artifact_id});await loadPackages(root);}catch(e){fail(e);}};
        card.appendChild(button(item.installed?'Repair':'Install',()=>run('install'),!catalogue.distribution_ready));
        if(item.installed)card.append(button('Verify',()=>run('verify')),button('Remove',()=>run('remove')));
        root.appendChild(card);
      }
    }catch(e){fail(e);}
  }
  function renderApprovals() {
    const root=$('assistant-approvals');root.replaceChildren();
    for(const [id,item] of approvals){const card=element('div','','card');card.append(element('strong','Permission requested · '+item.tool),element('p',item.detail));
      for(const [label,allow,remember] of [['Allow once',true,false],['Allow for session',true,true],['Deny',false,false]])card.appendChild(button(label,async()=>{try{await rpc('assistant.approve',{id,allow,remember});approvals.delete(id);renderApprovals();}catch(e){fail(e);}}));root.appendChild(card);}
  }
  // Capture is held by this view; lease IDs make cancellation safe across views.
  const voiceButtons=[];let recording=null,held=false,generation=0,voiceBusy=false,heldKey='';
  function updateVoiceButtons(){for(const entry of voiceButtons){entry.button.hidden=!config?.voice?.enabled;entry.button.disabled=voiceBusy;entry.button.title='Hold to dictate · '+(config?.voice?.shortcut || 'Mod+Shift+M');}}
  function voiceNotice(entry,text){entry.notice.textContent=text;entry.notice.hidden=!text;}
  async function begin(entry) {
    if(held || voiceBusy || !config?.voice?.enabled)return;
    held=true;const gen=++generation;voiceNotice(entry,'Opening microphone…');
    try {
      const lease=await rpc('voice.begin');
      if(!held || gen!==generation){await rpc('voice.end',{id:lease.id});return;}
      recording={lease,entry};
      if(!lease.native)throw Error('Native dictation is unavailable on this system. Use Voice Input in the desktop app.');
      voiceNotice(entry,'Recording… Release to transcribe.');
      recording.timer=setTimeout(finish,120000);
    }catch(e){if(gen===generation){cancel();voiceNotice(entry,e.message);}}
  }
  async function finish(){
    if(!held)return;held=false;
    if(!recording){generation++;return;}
    const current=recording,gen=generation;recording=null;clearTimeout(current.timer);voiceBusy=true;updateVoiceButtons();
    voiceNotice(current.entry,'Transcribing locally…');
    try {
      const audio=await rpc('voice.finish',{id:current.lease.id});
      if(gen!==generation)return;
      const result=await rpc('voice.transcribe',{audio,context:uiContext()});
      if(gen!==generation)return;
      const input=current.entry.input;input.value=[input.value,result.text].filter(Boolean).join(' ');input.dispatchEvent(new Event('input'));input.focus();voiceNotice(current.entry,'');
      if(config.voice.auto_send && current.entry.submit)current.entry.submit();
    }catch(e){if(gen===generation)voiceNotice(current.entry,e.message);}
    finally{voiceBusy=false;updateVoiceButtons();}
  }
  function cancel(){generation++;held=false;if(recording){clearTimeout(recording.timer);rpc('voice.end',{id:recording.lease.id}).catch(()=>{});voiceNotice(recording.entry,'');recording=null;}if(voiceBusy)rpc('voice.cancel').catch(()=>{});}
  function attachVoice(input,container,submit){if(!input||!container)return;const b=button('Dictate',()=>{}),note=element('span','','hint');note.setAttribute('role','status');note.hidden=true;b.hidden=true;b.type='button';const entry={button:b,notice:note,input,submit};voiceButtons.push(entry);container.append(b,note);
    b.onpointerdown=e=>{if(e.button===0){b.setPointerCapture(e.pointerId);begin(entry);}};b.onpointerup=finish;b.onpointercancel=cancel;
    b.onkeydown=e=>{if([' ','Enter'].includes(e.key)&&!e.repeat){e.preventDefault();begin(entry);}};b.onkeyup=e=>{if([' ','Enter'].includes(e.key)){e.preventDefault();finish();}};
  }
  attachVoice(draft,$('assistant-composer'),sendAssistant);
  attachVoice(promptInput,$('send-btn')?.parentElement,send);
  const workerText=$('worker-followup');attachVoice(workerText,workerText?.parentElement,()=> $('worker-continue')?.click());
  attachVoice($('worker-task'),$('worker-task')?.parentElement,null);
  function shortcut(e,value){const key=value?.split('+').at(-1);return key&&(e.ctrlKey||e.metaKey)&&e.shiftKey&&!e.altKey&&(key==='Space'?e.code==='Space':e.key.toUpperCase()===key);}
  window.addEventListener('keydown',e=>{if(e.repeat||!config)return;if(shortcut(e,config.shortcut)){e.preventDefault();navigate('assistant');draft.focus();}else if(shortcut(e,config.voice?.shortcut)&&config.voice.enabled){e.preventDefault();heldKey=e.code||e.key;const entry=voiceButtons.find(x=>x.input===document.activeElement)||voiceButtons.find(x=>x.input===draft);if(entry.input===draft)navigate('assistant');begin(entry);}});
  window.addEventListener('keyup',e=>{if(heldKey&&(e.code||e.key)===heldKey){heldKey='';finish();}});
  window.addEventListener('blur',()=>{if(held)cancel();});window.addEventListener('pagehide',cancel);
  window.addEventListener('message',({data:m})=>{
    if(m.type==='assistantReply'){const request=requests.get(m.id);if(request){requests.delete(m.id);m.error?request.reject(Error(m.error)):request.resolve(m.result);}}
    if(m.type==='actionResult'&&!m.ok&&requests.has(m.id)){requests.get(m.id).reject(Error(m.error));requests.delete(m.id);}
    if(m.type==='status'){if(m.status.connected && !connectionChecked)load();if(!m.status.connected){connectionChecked=false;cancel();for(const r of requests.values())r.reject(Error('PrAImate disconnected'));requests.clear();available=false;render();}}
    if(m.type==='assistantEvent'){
      if(m.method==='assistant.navigate') {const target={chats:'chat',dashboard:'overview',code:'chat',studio:'chat',localllm:'settings'}[m.value.page]||m.value.page;if(m.value.chat_id)post('openChat',{id:m.value.chat_id});if(m.value.worker_id)workerSelected=m.value.worker_id;if(pages[target])navigate(target);}
      if(m.method==='assistant.config.changed'){config=m.value;if(!config.voice.enabled)cancel();render();renderSettings();updateVoiceButtons();}
      if(m.method==='assistant.event'){$('assistant-phase').textContent=m.value.phase;if(m.value.task)snapshot={...snapshot,task:m.value.task};render();if(['completed','failed','cancelled'].includes(m.value.phase)){approvals.clear();renderApprovals();load();}}
      if(m.method==='assistant.approval'){approvals.set(m.value.id,m.value);renderApprovals();navigate('assistant');}
      if(m.method==='assistant.artifact.progress')$('assistant-phase').textContent=m.value.phase+' · '+Math.round((m.value.downloaded||0)/1048576)+' MiB';
    }
  });
  if(status.connected)load();
})();
