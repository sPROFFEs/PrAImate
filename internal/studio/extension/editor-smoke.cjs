'use strict';
const assert = require('node:assert/strict');
const vscode = require('vscode');
exports.run = async function () {
  const ext = vscode.extensions.getExtension('PrAImate.praimate-studio');
  assert.ok(ext, 'development extension discovered');
  await ext.activate();
  assert.equal(ext.packageJSON.version, '0.6.0', 'old built-in extension must not win');
  let status;
  for (let attempt = 0; attempt < 25; attempt++) {
    await new Promise(resolve => setTimeout(resolve, 300));
    await vscode.commands.executeCommand('praimate.diagnostics');
    const document = vscode.window.activeTextEditor?.document;
    if (document?.languageId === 'json') {
      status = JSON.parse(document.getText());
      if (status.session?.cli === 'claude') break;
    }
  }
  assert.equal(status?.backendRunning, true, 'real authenticated backend connection');
  assert.equal(status?.session.cli, 'claude', 'launch configuration reached Core');
  assert.equal(status?.session.workspace, vscode.workspace.workspaceFolders[0].uri.fsPath);
  await vscode.commands.executeCommand('praimate.newChat');
  await vscode.commands.executeCommand('praimate.openPanel');
  await new Promise(resolve => setTimeout(resolve, 1000));
  assert.ok(vscode.window.tabGroups.all.some(group => group.tabs.some(tab => tab.label === 'PrAImate' && tab.input instanceof vscode.TabInputWebview)), 'real webview panel opens');
  await vscode.commands.executeCommand('praimate.chatView.focus');
  await new Promise(resolve => setTimeout(resolve, 1000));
  await vscode.commands.executeCommand('praimate.diagnostics');
  const ui = JSON.parse(vscode.window.activeTextEditor.document.getText()).ui;
  assert.equal(ui.openViews,2,'sidebar and panel were resolved');
  assert.equal(ui.readyViews,2,'both real webviews executed their scripts and completed the ready handshake');
  const fs=require('node:fs'), path=require('node:path');
  const terminals=[];
  for(const cli of ['claude','openclaude','codex','opencode','praimate-code']) {
    const terminal=await vscode.commands.executeCommand('praimate.openTerminal',cli);
    assert.ok(terminal,'terminal created for '+cli);
    try {
      const file=path.join(process.env.PRAIMATE_TEST_TERMINAL_DIR,cli+'.txt');
      for(let i=0;i<30 && !fs.existsSync(file);i++) await new Promise(resolve=>setTimeout(resolve,100));
      assert.ok(fs.existsSync(file),'real PTY executed fixture for '+cli);
      const argv=fs.readFileSync(file,'utf8').trimEnd().split('\n');
      assert.equal(argv[0],status.session.workspace,'terminal working folder');
      assert.deepEqual(argv.slice(1),cli==='claude'?['--model','fixture-model']:[],'CLI model arguments');
      terminals.push(cli);
    } finally {terminal.dispose();}
  }
  fs.writeFileSync(process.env.PRAIMATE_TEST_RESULT,JSON.stringify({ok:true,version:ext.packageJSON.version,connected:status.backendRunning,cli:status.session.cli,panel:true,sidebar:true,readyWebviews:ui.readyViews,terminals}));
  console.log('PRAIMATE_EDITOR_SMOKE_OK: extension 0.6.0, authenticated Core, both webviews, five native terminal fixtures');
};
