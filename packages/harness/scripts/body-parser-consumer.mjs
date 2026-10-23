import assert from 'node:assert/strict'
import {execFileSync} from 'node:child_process'
import {existsSync,readFileSync,realpathSync,writeFileSync} from 'node:fs'
import http from 'node:http'
import {createRequire} from 'node:module'
import {dirname,join,relative} from 'node:path'
import {pathToFileURL} from 'node:url'
import {deflateSync} from 'node:zlib'

const npm=process.platform==='win32'?'npm.cmd':'npm'
const manifest=root=>JSON.parse(readFileSync(join(root,'package.json'),'utf8'))
const installed=(from,name)=>{
 const require=createRequire(join(from,'package.json'))
 for(const base of require.resolve.paths(name)??[]){
  const file=join(base,name,'package.json')
  if(!existsSync(file))continue
  assert.equal(JSON.parse(readFileSync(file,'utf8')).name,name)
  return realpathSync(dirname(file))
 }
 throw Error(`missing installed dependency ${name}`)
}

export function traceBodyParser(projectRoot,harnessRoot){
 const sdk=installed(harnessRoot,'@modelcontextprotocol/sdk')
 const express=installed(sdk,'express'),parser=installed(express,'body-parser')
 return {sdkVersion:manifest(sdk).version,sdkLocation:relative(projectRoot,sdk),
  sdkExpressRange:manifest(sdk).dependencies.express,
  expressVersion:manifest(express).version,expressLocation:relative(projectRoot,express),
  expressBodyParserRange:manifest(express).dependencies['body-parser'],
  bodyParserVersion:manifest(parser).version,bodyParserLocation:relative(projectRoot,parser)}
}

export function requireFixedBodyParser(row){
 assert.equal(row.bodyParserVersion,'2.4.0','installed MCP/Express path must resolve fixed body-parser2.4.0')
}

export async function verifyBodyParserConsumer(projectRoot,harnessRoot,stage){
 const row=traceBodyParser(projectRoot,harnessRoot)
 console.log(`packed body-parser ${stage}: ${JSON.stringify(row)}`)
 requireFixedBodyParser(row)
 assert.equal(row.sdkVersion,'1.32.1')
 const sdk=installed(harnessRoot,'@modelcontextprotocol/sdk')
 // These files are the SDK's supported ESM client/index.js and server/express.js subpaths.
 const {Client}=await import(pathToFileURL(join(sdk,'dist/esm/client/index.js')).href)
 const {createMcpExpressApp}=await import(pathToFileURL(join(sdk,'dist/esm/server/express.js')).href)
 assert.equal(typeof Client,'function')
 const app=createMcpExpressApp()
 const prefix=Buffer.concat([deflateSync(Buffer.from('{"ok":true}')),Buffer.from([0])])
 const tail=Buffer.from([1,2,3])
 let client,server,routeCalls=0,requestFinished=false
 try{
  const response=new Promise((resolve,reject)=>{
   app.post('/fixture',(req,res)=>{
    routeCalls++
    req.once('end',()=>{requestFinished=true;res.json(req.body)})
    client.end(tail)
   })
   server=app.listen(0,'127.0.0.1',()=>{
    client=http.request({host:'127.0.0.1',port:server.address().port,path:'/fixture',method:'POST',agent:false,
     headers:{'content-type':'application/json','content-encoding':'deflate','content-length':prefix.length+tail.length}},res=>{
      const bytes=[]
      res.on('data',chunk=>bytes.push(chunk));res.on('error',reject)
      res.on('end',()=>resolve({status:res.statusCode,body:Buffer.concat(bytes).toString('utf8')}))
     })
    client.on('error',reject)
    client.setTimeout(3000,()=>client.destroy(Error('MCP compressed request completion timed out')))
    client.write(prefix)
   })
   server.on('error',reject)
  })
  const result=await response
  assert.equal(result.status,200);assert.deepEqual(JSON.parse(result.body),{ok:true})
  assert.equal(requestFinished,true,'MCP middleware must consume the remaining HTTP request')
  assert.equal(routeCalls,1)
 }finally{
  client?.destroy()
  if(server){server.closeAllConnections();await new Promise(resolve=>server.close(resolve))}
 }
 console.log(`packed body-parser ${stage}: supported SDK imports and tiny compressed HTTP completion passed`)
}

export async function existingLockBodyParserConsumer(projectRoot,tarball){
 const install=args=>execFileSync(npm,['install','--ignore-scripts','--no-audit','--no-fund',...args],{cwd:projectRoot,stdio:'inherit'})
 // The temporary direct old pin seeds an old lock, then is removed before testing the upgrade.
 const input={private:true,type:'module',dependencies:{'@modelcontextprotocol/sdk':'1.32.1',express:'5.2.1',zod:'4.6.7','body-parser':'2.3.0'}}
 writeFileSync(join(projectRoot,'package.json'),JSON.stringify(input,null,2)+'\n')
 install([])
 delete input.dependencies['body-parser']
 writeFileSync(join(projectRoot,'package.json'),JSON.stringify(input,null,2)+'\n')
 install([])
 install([tarball])
 const harness=installed(projectRoot,'@judgment-pack/harness')
 const before=traceBodyParser(projectRoot,harness)
 console.log(`packed body-parser old-lock after candidate: ${JSON.stringify(before)}`)
 assert.equal(before.bodyParserVersion,'2.3.0','existing-lock control must retain the affected compatible transitive selection')
 const beforeManifest=readFileSync(join(projectRoot,'package.json'),'utf8')
 execFileSync(npm,['update','body-parser','--ignore-scripts','--no-audit','--no-fund'],{cwd:projectRoot,stdio:'inherit'})
 assert.equal(readFileSync(join(projectRoot,'package.json'),'utf8'),beforeManifest,'transitive refresh must not add a direct dependency')
 await verifyBodyParserConsumer(projectRoot,harness,'old-lock after explicit transitive update')
}
