// Exercise the built MCP executable over real stdio against a running collector.
import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';
import { resolve } from 'node:path';

const executable = resolve('bin', process.platform === 'win32' ? 'goruntrace-mcp.exe' : 'goruntrace-mcp');
const child = spawn(executable, ['-endpoint', process.argv[2] || 'http://127.0.0.1:8080'], { stdio: ['pipe', 'pipe', 'inherit'] });
const lines = createInterface({ input: child.stdout });
const pending = new Map();
let id = 0;
lines.on('line', line => {
  try {
    const result = JSON.parse(line);
    const entry = pending.get(result.id);
    if (!entry) return;
    pending.delete(result.id);
    clearTimeout(entry.timer);
    if (result.error) entry.reject(new Error(JSON.stringify(result.error)));
    else entry.resolve(result.result);
  } catch (error) { console.error('Invalid MCP stdout:', error.message); }
});
function failPending(error) {
  for (const entry of pending.values()) { clearTimeout(entry.timer); entry.reject(error); }
  pending.clear();
}
child.on('error', failPending);
child.on('exit', code => failPending(new Error(`MCP process exited: ${code}`)));
function request(method, params) {
  return new Promise((resolve, reject) => {
    const next = ++id;
    const timer = setTimeout(() => { pending.delete(next); reject(new Error(`${method} timed out`)); }, 10000);
    pending.set(next, { resolve, reject, timer });
    child.stdin.write(JSON.stringify({ jsonrpc: '2.0', id: next, method, params }) + '\n');
  });
}
try {
  await request('initialize', { protocolVersion: '2025-11-25', capabilities: {}, clientInfo: { name: 'goruntrace-smoke', version: '0.1.0' } });
  child.stdin.write(JSON.stringify({ jsonrpc: '2.0', method: 'notifications/initialized' }) + '\n');
  const list = await request('tools/list', {});
  if (list.tools.length !== 5 || list.tools.some(tool => !tool.annotations?.readOnlyHint)) throw new Error('Expected five read-only tools');
  const failures = await request('tools/call', { name: 'get_failed_runs', arguments: { limit: 1 } });
  if (failures.isError) throw new Error('get_failed_runs failed');
  const run = failures.structuredContent.items[0];
  if (run) {
    const detail = await request('tools/call', { name: 'get_run_details', arguments: { run_id: run.run_id } });
    if (detail.isError || detail.structuredContent.run.run_id !== run.run_id) throw new Error('Run evidence mismatch');
    console.log(`MCP stdio PASS: five read-only tools; ${failures.structuredContent.total} failed runs; source run ${run.run_id}; ${detail.structuredContent.events.length} raw events`);
  } else console.log('MCP stdio PASS: five read-only tools; empty failure history');
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
} finally {
  lines.close();
  child.stdin.end();
  child.kill();
}
