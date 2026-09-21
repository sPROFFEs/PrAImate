'use strict';
const readline = require('node:readline');

// A transport can be a socket or a subprocess's stdin/stdout. Decode complete
// UTF-8 lines and settle every pending call when the transport closes.
class RPCClient {
  constructor(input, output, notify, onClose, timeoutMs = 30000) {
    this.output = output;
    this.pending = new Map();
    this.nextID = 1;
    this.closed = false;
    this.timeoutMs = timeoutMs;
    this.lines = readline.createInterface({ input, crlfDelay: Infinity });
    this.lines.on('line', line => {
      let msg;
      try { msg = JSON.parse(line); } catch { return this.close(new Error('Invalid backend response')); }
      if (msg.id !== undefined && this.pending.has(msg.id)) {
        const call = this.pending.get(msg.id);
        this.pending.delete(msg.id);
        clearTimeout(call.timer);
        if (msg.error) call.reject(new Error(msg.error.message || 'RPC error'));
        else call.resolve(msg.result);
      } else if (msg.method) notify(msg);
    });
    this.onClose = onClose;
    this.lines.on('close', () => this.close(new Error('Backend disconnected')));
    input.on('error', err => this.close(err));
    output.on('error', err => this.close(err));
  }
  request(method, params = {}) {
    if (this.closed) return Promise.reject(new Error('Backend disconnected'));
    return new Promise((resolve, reject) => {
      const id = this.nextID++;
      // Runs may legitimately take hours; cancellation is a separate RPC.
      const longRunning = method === 'chats.send' || method === 'workflows.run';
      const timer = longRunning ? undefined : setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(method + ' timed out'));
      }, this.timeoutMs);
      this.pending.set(id, { resolve, reject, timer });
      this.output.write(JSON.stringify({ jsonrpc: '2.0', id, method, params }) + '\n', err => {
        if (err) this.close(err);
      });
    });
  }
  close(err = new Error('Connection closed')) {
    if (this.closed) return;
    this.closed = true;
    for (const call of this.pending.values()) { clearTimeout(call.timer); call.reject(err); }
    this.pending.clear();
    this.lines.close();
    if (this.onClose) this.onClose(err);
  }
}
module.exports = { RPCClient };

