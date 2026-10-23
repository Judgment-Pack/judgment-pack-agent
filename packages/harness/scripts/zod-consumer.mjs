import assert from 'node:assert/strict'
import {execFileSync} from 'node:child_process'
import {createHash} from 'node:crypto'
import {existsSync,readFileSync,realpathSync,writeFileSync} from 'node:fs'
import {createRequire} from 'node:module'
import {dirname,join,relative} from 'node:path'

const npm=process.platform==='win32'?'npm.cmd':'npm'
export const fixedZod='4.6.7'
const manifestAt=root=>JSON.parse(readFileSync(join(root,'package.json'),'utf8'))

function dependencyRoot(from,name){
 const require=createRequire(join(from,'package.json'))
 // Use Node's installed-package search order. Exported package.json can be a
 // nested format manifest, and a package may expose only client/server subpaths.
 for(const base of require.resolve.paths(name)??[]){
  const manifest=join(base,name,'package.json')
  if(!existsSync(manifest))continue
  if(JSON.parse(readFileSync(manifest,'utf8')).name!==name)throw Error(`installed dependency has an unexpected name: ${name}`)
  return realpathSync(dirname(manifest))
 }
 const error=Error(`cannot locate installed dependency ${name}`)
 error.code='MODULE_NOT_FOUND'
 throw error
}

// Follow the installed production/peer graph from the harness, not a root require('zod').
// Package placement is traced with Node resolution; native ESM imports are checked separately.
export function traceZodResolutions(packageRoot){
 const rows=[],seen=new Set()
 const visit=root=>{
  root=realpathSync(root)
  if(seen.has(root))return
  seen.add(root)
  const manifest=manifestAt(root)
  const dependencies={...manifest.dependencies,...manifest.optionalDependencies,...manifest.peerDependencies}
  if(Object.hasOwn(dependencies,'zod')){
   const zodRoot=dependencyRoot(root,'zod')
   rows.push({owner:manifest.name,ownerVersion:manifest.version,ownerRoot:root,zodVersion:manifestAt(zodRoot).version,zodRoot})
  }
  for(const name of Object.keys(dependencies).sort()){
   let child
   try{child=dependencyRoot(root,name)}
   catch(error){
    const optional=Object.hasOwn(manifest.optionalDependencies??{},name)||manifest.peerDependenciesMeta?.[name]?.optional
    if(optional&&error.code==='MODULE_NOT_FOUND')continue
    throw error
   }
   visit(child)
  }
 }
 visit(packageRoot)
 return rows.sort((a,b)=>a.ownerRoot.localeCompare(b.ownerRoot))
}

export function requireFixedZod(rows){
 assert.ok(rows.length>0,'the installed Zod graph must not be empty')
 const retained=rows.filter(row=>row.zodVersion!==fixedZod)
 assert.deepEqual(retained.map(row=>`${row.owner}@${row.ownerVersion} -> zod@${row.zodVersion}`),[],
  'installed engine/SDK paths retain a different Zod; inspect and refresh the consumer lock explicitly')
}

export function reportZodResolutions(projectRoot,stage,rows){
 console.log(`packed Zod ${stage}: ${JSON.stringify(rows.map(({owner,ownerVersion,ownerRoot,zodVersion,zodRoot})=>({owner,ownerVersion,location:relative(projectRoot,ownerRoot),zodVersion,zodLocation:relative(projectRoot,zodRoot)})))}`)
}

export function verifyHarnessZod(projectRoot,packageRoot,stage){
 const rows=traceZodResolutions(packageRoot)
 for(const owner of ['@judgment-pack/harness','ai','deepagents','@langchain/core','@modelcontextprotocol/sdk']){
  assert.ok(rows.some(row=>row.owner===owner),`missing installed engine/SDK Zod path: ${owner}`)
 }
 reportZodResolutions(projectRoot,stage,rows)
 requireFixedZod(rows)
 return rows
}

// Proposed refresh for this exact dependency set. Its result is evidence, not a
// promise that a library pin repairs every already-installed host graph.
export function existingLockZodConsumer(projectRoot,tarball,harnessManifest){
 const install=args=>execFileSync(npm,['install','--ignore-scripts','--no-audit','--no-fund',...args],{cwd:projectRoot,stdio:'inherit'})
 writeFileSync(join(projectRoot,'package.json'),JSON.stringify({private:true,type:'module',dependencies:{...harnessManifest.dependencies,zod:'4.4.3'}},null,2)+'\n')
 install([])
 const oldZod=dependencyRoot(projectRoot,'zod')
 assert.equal(manifestAt(oldZod).version,'4.4.3','old-lock control must begin with the affected version')
 const oldRows=traceZodResolutions(dependencyRoot(projectRoot,'deepagents'))
 assert.ok(oldRows.some(row=>row.zodVersion==='4.4.3'),'old-lock fixture must share an affected engine/SDK path')
 reportZodResolutions(projectRoot,'old-lock baseline',oldRows)
 const lockHash=createHash('sha256').update(readFileSync(join(projectRoot,'package-lock.json'))).digest('hex')
 console.log(`packed Zod old-lock baseline digest: ${lockHash}`)
 install([tarball])
 const harness=dependencyRoot(projectRoot,'@judgment-pack/harness')
 assert.equal(manifestAt(dependencyRoot(harness,'zod')).version,fixedZod,'candidate harness must pin its own fixed copy')
 const afterInstall=traceZodResolutions(harness)
 reportZodResolutions(projectRoot,'old-lock after candidate',afterInstall)
 // Do not hide retained shared copies behind the harness-local pin.
 console.log(`packed Zod retained paths after candidate: ${afterInstall.filter(row=>row.zodVersion!==fixedZod).length}`)
 install(['--save-exact',`zod@${fixedZod}`])
 execFileSync(npm,['dedupe','--ignore-scripts','--no-audit','--no-fund'],{cwd:projectRoot,stdio:'inherit'})
 verifyHarnessZod(projectRoot,harness,'old-lock after explicit root pin and dedupe')
}
