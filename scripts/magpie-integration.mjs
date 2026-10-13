import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import http from 'node:http';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import assert from 'node:assert/strict';

const root=path.resolve(import.meta.dirname,'..');
const magpie=process.env.MAGPIE_BINARY;
if(!magpie)throw Error('Set MAGPIE_BINARY to an existing Magpie executable');
const dir=await fs.mkdtemp(path.join(os.tmpdir(),'quality-magpie-'));
const children=[];
let calls=0;
let probeAnswer='46.24 inches';
let probeStatus=200;
let releaseProbe;
const probeReady=new Promise(resolve=>{releaseProbe=resolve;});
const upstream=http.createServer(async(req,res)=>{
 let raw='';for await(const chunk of req)raw+=chunk;
 const body=JSON.parse(raw);calls++;
 const probe=body.messages?.at(-1)?.content?.startsWith('A previously isolated');
 if(probe){await probeReady;}
 const answer=probe?probeAnswer:'business reply';
 res.statusCode=probe?probeStatus:200;
 if(body.stream&&res.statusCode===200){
  res.setHeader('content-type','text/event-stream');
  for(const chunk of [
   {id:'fixture',model:'solo',choices:[{index:0,delta:{role:'assistant',content:answer},finish_reason:null}]},
   {id:'fixture',model:'solo',choices:[{index:0,delta:{},finish_reason:'stop'}],usage:{prompt_tokens:1,completion_tokens:1,total_tokens:2}},
  ]){res.write('data: '+JSON.stringify(chunk)+'\n\n');}
  res.end('data: [DONE]\n\n');
 }else{
  res.setHeader('content-type','application/json');res.end(JSON.stringify({id:'fixture',model:'solo',choices:[{message:{role:'assistant',content:answer},finish_reason:'stop'}],usage:{prompt_tokens:1,completion_tokens:1,total_tokens:2}}));
 }
});
await new Promise(r=>upstream.listen(0,'127.0.0.1',r));
const port=upstream.address().port;
const config=path.join(dir,'config','magpie');await fs.mkdir(config,{recursive:true});
const gate=path.join(dir,'quality.middleware.js');await fs.copyFile(path.join(root,'plugin/gate.middleware.js'),gate);
await fs.writeFile(path.join(config,'providers.json'),JSON.stringify({providers:[{id:'fixture',name:'Fixture',key:'fixture',chat:`http://127.0.0.1:${port}/v1`,models:['solo','luna']}]}));
await fs.writeFile(path.join(config,'plugins.json'),JSON.stringify({plugins:[{spec:gate}]}));
const log=path.join(dir,'magpie.log');await fs.writeFile(log,'');
const gatewayPort=await new Promise(resolve=>{const s=http.createServer();s.listen(0,'127.0.0.1',()=>{const p=s.address().port;s.close(()=>resolve(p))})});
const gateway=`http://127.0.0.1:${gatewayPort}`;
function launch(binary,args,env){const p=spawn(binary,args,{env,stdio:['pipe','pipe','pipe']});children.push(p);return p}
async function waitFor(fn){const until=Date.now()+15000;while(Date.now()<until){try{const v=await fn();if(v)return v}catch{}await new Promise(r=>setTimeout(r,150))}throw Error('integration timed out')}
try{
 const mp=launch(magpie,['serve'],{...process.env,HOME:dir,XDG_CONFIG_HOME:path.join(dir,'config'),XDG_CACHE_HOME:path.join(dir,'cache'),MAGPIE_ADDR:`127.0.0.1:${gatewayPort}`});
 mp.stderr.on('data',b=>fs.appendFile(log,b));mp.stdout.on('data',()=>{});
 await waitFor(async()=>{const r=await fetch(gateway+'/v1/models');return r.ok});
 const qc=launch(path.join(root,'bin/quality-check'),['-data',path.join(dir,'data'),'-gate',gate,'-owned'],process.env);
 let output='';const ready=new Promise((resolve,reject)=>{qc.stdout.on('data',b=>{output+=b;const i=output.indexOf('\n');if(i>=0)resolve(JSON.parse(output.slice(0,i)))});qc.once('error',reject)});
 const info=await ready;
 const api=async(p,body)=>{const r=await fetch(info.origin+p,{method:body?'POST':'GET',headers:{authorization:'Bearer '+info.control,'content-type':'application/json'},body:body?JSON.stringify(body):undefined});if(!r.ok)throw Error(await r.text());return r.json()};
 await api('/api/config',{gateway_url:gateway,gateway_key:'magpie',log_path:'',daily_limit:120});
 await api('/api/target',{model:'fixture/solo',enabled:true});
 await new Promise(r=>setTimeout(r,1500));
 const call=async model=>fetch(gateway+'/v1/chat/completions',{method:'POST',headers:{authorization:'Bearer magpie','content-type':'application/json'},body:JSON.stringify({model,messages:[{role:'user',content:'business'}],max_tokens:100})});
 let response=await call('fixture/solo');assert.equal(response.status,200);
 assert.equal((await response.json()).choices[0].message.content,'business reply','unchecked business request did not reach upstream');
 await waitFor(async()=>{const v=await api('/api/state');return v.state.results.some(r=>r.verdict==='running');});
 releaseProbe();
 try{await waitFor(async()=>{const v=await api('/api/state');return v.state.targets['fixture/solo'].state==='passed'});}catch(e){console.error(JSON.stringify(await api('/api/state')));console.error(await fs.readFile(log,'utf8'));throw e}
 await new Promise(r=>setTimeout(r,1200));response=await call('fixture/solo');assert.equal(response.status,200,await response.text());
 const protocols=[
  ['/v1/chat/completions',{model:'fixture/solo',messages:[{role:'user',content:'business'}],max_tokens:100}],
  ['/v1/messages',{model:'fixture/solo',max_tokens:100,messages:[{role:'user',content:'business'}]}],
  ['/v1/responses',{model:'fixture/solo',input:'business'}],
 ];
 async function verifyProtocols(blocked){
  for(const [endpoint,body] of protocols){
   const before=calls;
   const response=await fetch(gateway+endpoint,{method:'POST',headers:{authorization:'Bearer magpie','x-api-key':'magpie','anthropic-version':'2023-06-01','content-type':'application/json'},body:JSON.stringify(body)});
   assert.equal(response.status,blocked?403:200,endpoint+': '+(response.ok?'':await response.clone().text()));
   const result=await response.json();
   if(blocked){
    assert.equal(result.error.type,'permission_error');assert.match(result.error.message,/MAGPIE_QUALITY_REVIEW_REQUIRED/);assert.equal(calls,before);
   }else{
    const text=endpoint.endsWith('/messages')?result.content[0].text:endpoint.endsWith('/responses')?result.output.find(item=>item.type==='message').content[0].text:result.choices[0].message.content;
    assert.equal(text,'business reply',endpoint);assert.equal(calls,before+1,'business request was lost or duplicated');
   }
  }
 }
 probeAnswer='48 inches';
 await api('/api/action',{model:'fixture/solo',action:'test'});
 await waitFor(async()=>{const v=await api('/api/state');return v.state.targets['fixture/solo'].verdict==='review';});
 await new Promise(r=>setTimeout(r,1200));await verifyProtocols(false);
 await api('/api/action',{model:'fixture/solo',action:'strict'});
 await new Promise(r=>setTimeout(r,1200));await verifyProtocols(true);
 probeStatus=503;
 await api('/api/action',{model:'fixture/solo',action:'test'});
 await waitFor(async()=>{const v=await api('/api/state');return v.state.targets['fixture/solo'].state==='unavailable';});
 await new Promise(r=>setTimeout(r,1200));await verifyProtocols(true);
 await api('/api/action',{model:'fixture/solo',action:'monitor'});
 await new Promise(r=>setTimeout(r,1200));await verifyProtocols(false);
 await api('/api/action',{model:'fixture/solo',action:'hold'});
 await new Promise(r=>setTimeout(r,1200));await verifyProtocols(true);
 await api('/api/action',{model:'fixture/solo',action:'resume'});
 await new Promise(r=>setTimeout(r,1200));await verifyProtocols(false);
 const stopped=once(qc,'exit');qc.stdin.end();await stopped;
 await verifyProtocols(false);
 response=await call('fixture/luna');assert.equal(response.status,200);
 console.log('PASS: real Magpie forwards all three APIs in monitor mode, probes without a log, preserves explicit strict/held blocks and forwards with the detector stopped');
}finally{
 releaseProbe();
 for(const p of children){p.stdin.end();p.kill('SIGTERM')}
 await Promise.all(children.map(async p=>{if(p.exitCode===null)await Promise.race([once(p,'exit'),new Promise(r=>setTimeout(r,3000))]);if(p.exitCode===null)p.kill('SIGKILL')}));
 upstream.close();await fs.rm(dir,{recursive:true,force:true});
}
