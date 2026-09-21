'use strict';
const path = require('node:path');

// Build TerminalOptions, never a sendText command in the user's existing shell.
// Native binaries receive an argv array. Batch shims require cmd.exe; preserve
// spaces with its /s quoting convention and fail closed on shell metacharacters.
function terminalOptions(plan, platform, env) {
  if (!plan?.command || !plan.cwd || !Array.isArray(plan.args) || !plan.args.every(a => typeof a === 'string')) {
    throw new Error('Core returned an invalid terminal launch plan');
  }
  const options = {name:'PrAImate · '+plan.cli,cwd:plan.cwd,shellPath:plan.command,shellArgs:plan.args,
    env:{...plan.env,PRAIMATE_SOCK:null,PRAIMATE_STUDIO_TOKEN:null,PRAIMATE_STUDIO_CONFIG:null},
    message:'\r\nNative '+plan.cli+' terminal. CLI defaults apply; Studio permissions, persona, skills, MCP and local routing are not copied.\r\n'};
  if (platform === 'win32' && /\.(cmd|bat)$/i.test(plan.command)) {
    const values = [plan.command,...plan.args];
    if (values.some(value => /[\x00-\x1f"&|<>^%!]/.test(value))) {
      throw new Error('Windows batch launch cannot safely quote this path or model. Use a native .exe installation or remove shell metacharacters.');
    }
    options.shellPath = path.win32.join(env.SystemRoot || env.SYSTEMROOT || 'C:\\Windows','System32','cmd.exe');
    options.shellArgs = '/d /s /v:off /c "'+values.map(value => '"'+value+'"').join(' ')+'"';
  }
  return options;
}
module.exports = {terminalOptions};
