import { spawn } from 'node:child_process';
import { existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import os from 'node:os';
import { resolveCloudflared } from './cloudflared-runtime.mjs';
import { startGatewayDiscovery } from './gateway-discovery.mjs';
import { waitForConsole } from './console-readiness.mjs';

const root = fileURLToPath(new URL('../', import.meta.url));
let child, ready, stopDiscovery;
function start(options = {}) {
  if (ready) return ready;
  ready = new Promise((resolve, reject) => {
    const platform = { win32: 'windows', darwin: 'darwin', linux: 'linux' }[process.platform] || process.platform;
    const arch = { x64: 'amd64', arm64: 'arm64' }[process.arch] || process.arch;
    const suffix = platform === 'windows' ? '.exe' : '';
    const bundled = path.join(root, 'bin', `quality-check-${platform}-${arch}${suffix}`);
    const fallback = path.join(root, 'bin', `quality-check${suffix}`);
    const binary = options.binaryPath || (requireFile(bundled) ? bundled : fallback);
    const args=['-data', options.dataDir || path.join(os.homedir(), '.config', 'magpie-quality-check'), '-gate', path.join(root, 'plugin', 'gate.middleware.js'),'-owned'];
    if(options.cloudflaredPath)args.push('-cloudflared',options.cloudflaredPath);
    child = spawn(binary, args, { stdio: ['pipe', 'pipe', 'pipe'], windowsHide: true });
    child.stdin.on('error',()=>{});
    let output = '';
    const timer = setTimeout(() => { child.kill(); reject(new Error('质量检测后台启动超时')); }, 15000);
    child.stdout.on('data', b => { output += b; const i = output.indexOf('\n'); if (i >= 0) { try { const v = JSON.parse(output.slice(0, i)); clearTimeout(timer); resolve(v); } catch { reject(new Error('后台启动信息无效')); } } });
    child.stderr.on('data', b => { console.warn(b.toString().trimEnd()); });
    child.once('error', e => { clearTimeout(timer); ready = undefined; reject(new Error(`无法启动 Go 后台，请先构建二进制：${e.message}`)); });
    child.once('exit', () => { clearTimeout(timer); stopDiscovery?.(); stopDiscovery = undefined; ready = undefined; reject(new Error('检测后台已退出')); });
  });
  return ready;
}
function requireFile(file) {
  return existsSync(file);
}
process.once('exit', () => child?.kill());
export default { id: 'quality-check', server: async (_input, options = {}) => ({
  auth: {
    provider: 'quality-check',
    methods: [false,true].map(remote=>({ type: 'oauth', label: remote?'打开临时远程控制台（Cloudflare）':'打开本机质量检测控制台', async authorize() {
      const v = await start(options);
      const binary=remote?await resolveCloudflared(options,options.dataDir || path.join(os.homedir(), '.config', 'magpie-quality-check')):undefined;
      const r=await fetch(v.origin+'/api/console',{method:'POST',headers:{authorization:'Bearer '+v.control,'content-type':'application/json'},body:JSON.stringify({remote,binary})});
      if(!r.ok)throw Error(await r.text());const {url}=await r.json();
      if(remote){
        try { await waitForConsole(url); }
        catch(error){
          await fetch(v.origin+'/api/console/close',{method:'POST',headers:{authorization:'Bearer '+v.control,'content-type':'application/json'},body:'{}'}).catch(()=>{});
          // The service delays tunnel shutdown to deliver its acknowledgement.
          await new Promise(resolve=>setTimeout(resolve,350));
          throw error;
        }
      }
      return { url, instructions: `完整管理链接：${url}\n复制链接后在浏览器打开。链接十分钟内有效且只能使用一次。此入口不是供应商登录，无需提交验证码；使用后可以取消此窗口。${remote ? '临时通道最长 30 分钟。' : ''}`, method: 'code', callback: async () => ({ type: 'failed' }) };
    } })),
    async loader() { await start(options); return {}; }
  },
  async config(cfg) {
    // The control-only provider has no inference calls to trigger loader.
    // Start on host configuration so persisted monitoring works after restart.
    await start(options);
    const service=await ready;
    stopDiscovery ??= startGatewayDiscovery(service, options);
    cfg.provider ??= {};
    cfg.provider['quality-check'] = { name: 'Intelligence Check', models: {} };
  }
}) };
