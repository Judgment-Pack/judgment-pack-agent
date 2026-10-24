import assert from 'node:assert/strict'
import {execFileSync} from 'node:child_process'
import {createHash} from 'node:crypto'
import {existsSync,readdirSync,readFileSync,realpathSync,writeFileSync} from 'node:fs'
import {createRequire} from 'node:module'
import {dirname,join,relative} from 'node:path'
import {pathToFileURL} from 'node:url'

const manifest=root=>JSON.parse(readFileSync(join(root,'package.json'),'utf8'))
export function installedPackage(from,name){
 const require=createRequire(join(from,'package.json'))
 for(const base of require.resolve.paths(name)??[]){
  const file=join(base,name,'package.json')
  if(existsSync(file)){
   assert.equal(JSON.parse(readFileSync(file,'utf8')).name,name)
   return realpathSync(dirname(file))
  }
 }
 throw Error(`missing installed dependency ${name}`)
}

// Walk package metadata and nested node_modules only; include dev and extraneous copies.
export function inventoryUndici(projectRoot){
 const copies=[],owners=[],seen=new Set()
 const visitPackage=at=>{
  const file=join(at,'package.json')
  if(!existsSync(file))return
  const root=realpathSync(at)
  if(seen.has(root))return
  seen.add(root)
  const pkg=manifest(root)
  if(pkg.name==='undici')copies.push({root,location:relative(projectRoot,root),version:pkg.version})
  const range=pkg.dependencies?.undici??pkg.optionalDependencies?.undici??pkg.peerDependencies?.undici
  if(range){
   const resolved=installedPackage(root,'undici')
   owners.push({owner:pkg.name,version:pkg.version,location:relative(projectRoot,root),range,
    undiciVersion:manifest(resolved).version,undiciLocation:relative(projectRoot,resolved)})
  }
  visitModules(join(root,'node_modules'))
 }
 const visitModules=at=>{
  if(!existsSync(at))return
  for(const name of readdirSync(at).sort()){
   if(name.startsWith('.'))continue
   if(name.startsWith('@'))for(const child of readdirSync(join(at,name)).sort())visitPackage(join(at,name,child))
   else visitPackage(join(at,name))
  }
 }
 visitPackage(projectRoot)
 visitModules(join(projectRoot,'node_modules'))
 return {copies:copies.sort((a,b)=>a.location.localeCompare(b.location)),owners:owners.sort((a,b)=>a.location.localeCompare(b.location))}
}

export const publicUndiciInventory=inventory=>({copies:inventory.copies.map(({location,version})=>({location,version})),owners:inventory.owners})
const approved=new Set(['7.30.1','8.12.2'])
export function requireFixedUndici(inventory){
 assert.ok(inventory.copies.length>0,'installed Undici inventory must not be empty')
 for(const copy of inventory.copies)assert.ok(approved.has(copy.version),`unverified Undici copy: ${copy.location}@${copy.version}`)
 for(const owner of inventory.owners){
  assert.ok(approved.has(owner.undiciVersion),`affected owner resolution: ${owner.owner}@${owner.undiciVersion}`)
  const required=/^\^(\d+)\.(\d+)\.(\d+)$/.exec(owner.range)
  const actual=owner.undiciVersion.split('.').map(Number)
  assert.ok(required&&actual[0]===Number(required[1])&&(actual[1]>Number(required[2])||actual[1]===Number(required[2])&&actual[2]>=Number(required[3])),
   `incompatible Undici owner resolution: ${owner.owner} ${owner.range} -> ${owner.undiciVersion}`)
 }
}

// Exercise the actual installed parser. Only item-count properties are reduced
// as an isolated test seam; no source patch or large input is needed.
export function undiciBehaviorTests(test,copy){
 const require=createRequire(join(copy.root,'package.json'))
 const undici=require(copy.root)
 const {EventSourceStream}=require(join(copy.root,'lib/web/eventsource/eventsource-stream.js'))
 const label=`Undici behavior ${copy.version}`
 const parser=(t,maxEventSize=64)=>{
  const events=[]
  const stream=new EventSourceStream({maxEventSize,push:event=>{if(event!==null)events.push(event);return true}})
  stream.on('error',()=>{})
  t.after(()=>stream.destroy())
  return {stream,events}
 }
 const write=(stream,chunk)=>new Promise(resolve=>stream.write(Buffer.from(chunk),error=>resolve(error??undefined)))
 const bounded=(error,message)=>{assert.ok(error,message);assert.match(error.message,/EventSource message size exceeded/)}
 test(`${label}: retained data-field count refuses at a tiny seam`,async t=>{
  const {stream,events}=parser(t);stream.maxDataLines=2
  bounded(await write(stream,'data:\ndata:\ndata:\n'),'retained data-field count must refuse the third field')
  assert.equal(events.length,0)
 })
 test(`${label}: buffered chunk count refuses at a tiny seam`,async t=>{
  const {stream}=parser(t);stream.maxBufferedChunks=2
  assert.equal(await write(stream,'a'),undefined);assert.equal(await write(stream,'b'),undefined)
  bounded(await write(stream,'c'),'retained chunk count must refuse the third chunk')
  assert.ok(stream.chunks.length<=2)
 })
 test(`${label}: unfinished line length refuses before retaining more bytes`,async t=>{
  const {stream}=parser(t,4)
  bounded(await write(stream,'abcdefghijkl'),'unfinished line must refuse above four value plus seven syntax bytes')
 })
 for(const [field,value] of [['id','abcde'],['event','abcde'],['retry','12345']]){
  test(`${label}: retained ${field} bytes are bounded`,async t=>{
   const {stream}=parser(t,4)
   bounded(await write(stream,`${field}:${value}\n`),`retained ${field} bytes must refuse above the event bound`)
  })
 }
 test(`${label}: combined metadata field bytes are bounded`,async t=>{
  const {stream}=parser(t,4)
  bounded(await write(stream,'id:ab\nevent:cd\nretry:1\n'),'combined retained metadata bytes must refuse above the event bound')
 })
 test(`${label}: legitimate completed events reset retained state`,async t=>{
  const {stream,events}=parser(t,4);stream.maxDataLines=2;stream.maxBufferedChunks=2
  assert.equal(await write(stream,'id:a\nevent:x\nretry:1\ndata:b\n\n'),undefined)
  assert.equal(await write(stream,'data:cd\n\n'),undefined)
  assert.deepEqual(events.map(event=>event.options.data),['b','cd'])
  assert.equal(events[0].type,'x');assert.equal(events[1].options.lastEventId,'a')
  assert.equal(stream.state.reconnectionTime,1)
 })
 const body='fixture',url='data:text/plain,fixture'
 const digest=(algorithm,text)=>createHash(algorithm).update(text).digest('base64')
 const mismatch=error=>error?.name==='TypeError'&&error.cause?.message==='integrity mismatch'
 test(`${label}: lowercase SRI positive and mismatch controls`,async()=>{
  assert.equal(await (await undici.fetch(url,{integrity:`sha256-${digest('sha256',body)}`})).text(),body)
  await assert.rejects(undici.fetch(url,{integrity:`sha256-${digest('sha256','other')}`}),mismatch)
 })
 for(const algorithm of ['sha256','sha384','sha512'])for(const token of [algorithm.toUpperCase(),algorithm.replace('sha','ShA')]){
  test(`${label}: ${token} SRI validates matching bytes and refuses mismatches`,async()=>{
   assert.equal(await (await undici.fetch(url,{integrity:`${token}-${digest(algorithm,body)}`})).text(),body)
   await assert.rejects(undici.fetch(url,{integrity:`${token}-${digest(algorithm,'other')}`}),mismatch,
    `${token} SRI must reject bytes that do not match the pinned digest`)
  })
 }
 test(`${label}: case-normalized SRI selects the strongest algorithm`,async()=>{
  assert.equal(await (await undici.fetch(url,{integrity:`SHA256-${digest('sha256','other')} sha512-${digest('sha512',body)}`})).text(),body)
  await assert.rejects(undici.fetch(url,{integrity:`sha256-${digest('sha256',body)} ShA512-${digest('sha512','other')}`}),mismatch,
   'strongest mixed-case SRI must refuse its mismatch even beside a matching weaker hash')
 })
}


export async function verifyUndiciConsumer(projectRoot,harnessRoot,stage){
 const inventory=inventoryUndici(projectRoot)
 console.log(`packed Undici ${stage}: ${JSON.stringify(publicUndiciInventory(inventory))}`)
 requireFixedUndici(inventory)
 const anthropic=installedPackage(harnessRoot,'@ai-sdk/anthropic')
 const provider=installedPackage(anthropic,'@ai-sdk/provider-utils')
 const providerRequire=createRequire(join(provider,'package.json'))
 const sdk=await import(pathToFileURL(providerRequire.resolve('@ai-sdk/provider-utils')).href)
 assert.equal(typeof sdk.fetchWithValidatedEndpoint,'function')
 const root=installedPackage(provider,'undici'),runtime=providerRequire(root)
 const body='fixture',good=createHash('sha256').update(body).digest('base64')
 const bad=createHash('sha256').update('other').digest('base64')
 assert.equal(await (await runtime.fetch('data:text/plain,fixture',{integrity:`ShA256-${good}`})).text(),body)
 await assert.rejects(runtime.fetch('data:text/plain,fixture',{integrity:`SHA256-${bad}`}),
  error=>error?.name==='TypeError'&&error.cause?.message==='integrity mismatch')
 const {EventSourceStream}=providerRequire(join(root,'lib/web/eventsource/eventsource-stream.js'))
 const events=[]
 const parser=new EventSourceStream({maxEventSize:64,push:event=>{if(event!==null)events.push(event);return true}})
 parser.on('error',()=>{})
 try{
  await new Promise((resolve,reject)=>parser.write(Buffer.from('data:fixture\n\n'),error=>error?reject(error):resolve()))
  assert.equal(events.length,1);assert.equal(events[0].options.data,body)
 }finally{parser.destroy()}
 console.log(`packed Undici ${stage}: actual provider-utils import/owned fetch SRI and legitimate SSE passed`)
 return inventory
}

export async function verifyUndiciDevelopment(projectRoot){
 const root=installedPackage(projectRoot,'jsdom')
 const require=createRequire(join(root,'package.json'))
 const {JSDOM}=require(root)
 const document=new JSDOM('<p>fixture</p>')
 try{assert.equal(document.window.document.querySelector('p').textContent,'fixture')}
 finally{document.window.close()}
 console.log('installed Undici development: jsdom imports and legitimate document creation passed')
}

export async function existingLockUndiciConsumer(projectRoot,tarball){
 const npm=process.platform==='win32'?'npm.cmd':'npm'
 const install=args=>execFileSync(npm,['install','--ignore-scripts','--no-audit','--no-fund',...args],{cwd:projectRoot,stdio:'inherit'})
 const input={private:true,type:'module',dependencies:{'@ai-sdk/provider-utils':'5.0.55',undici:'7.29.0',zod:'4.6.7'}}
 writeFileSync(join(projectRoot,'package.json'),JSON.stringify(input,null,2)+'\n')
 install([])
 delete input.dependencies.undici
 writeFileSync(join(projectRoot,'package.json'),JSON.stringify(input,null,2)+'\n')
 install([])
 install([tarball])
 const before=inventoryUndici(projectRoot)
 console.log(`packed Undici old-lock after candidate: ${JSON.stringify(publicUndiciInventory(before))}`)
 assert.ok(before.owners.some(row=>row.owner==='@ai-sdk/provider-utils'&&row.undiciVersion==='7.29.0'),
  'old-lock consumer must retain the affected compatible provider selection before refresh')
 const inputBefore=readFileSync(join(projectRoot,'package.json'),'utf8')
 execFileSync(npm,['update','undici','--ignore-scripts','--no-audit','--no-fund'],{cwd:projectRoot,stdio:'inherit'})
 assert.equal(readFileSync(join(projectRoot,'package.json'),'utf8'),inputBefore,'consumer refresh must not add a direct Undici dependency')
 await verifyUndiciConsumer(projectRoot,installedPackage(projectRoot,'@judgment-pack/harness'),'old-lock after explicit transitive update')
}
