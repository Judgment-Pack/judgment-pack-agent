import {execFileSync} from 'node:child_process'
import {mkdtempSync,readdirSync,readFileSync,rmSync,statSync,writeFileSync} from 'node:fs'
import {tmpdir} from 'node:os'
import {dirname,isAbsolute,join,relative,resolve} from 'node:path'
import {fileURLToPath} from 'node:url'
import {build} from 'vite'

const packageDirectory=fileURLToPath(new URL('..',import.meta.url))
const temporary=mkdtempSync(join(tmpdir(),'judgment-pack-harness-'))
const npm=process.platform==='win32'?'npm.cmd':'npm'

try{
 const packed=JSON.parse(execFileSync(npm,['pack','--json','--pack-destination',temporary],{cwd:packageDirectory,encoding:'utf8'}))
 const tarball=join(temporary,packed[0].filename)
 writeFileSync(join(temporary,'package.json'),'{"private":true,"type":"module"}\n')
 execFileSync(npm,['install','--ignore-scripts','--no-audit','--no-fund',tarball],{cwd:temporary,stdio:'inherit'})
 const installedRoot=join(temporary,'node_modules','@judgment-pack','harness')
 const emitted=[]
 const visit=directory=>{
  for(const name of readdirSync(directory)){
   const path=join(directory,name)
   if(statSync(path).isDirectory())visit(path)
   else if(path.endsWith('.js')||path.endsWith('.d.ts'))emitted.push(path)
  }
 }
 visit(join(installedRoot,'dist'))
 for(const file of emitted){
  const source=readFileSync(file,'utf8')
  for(const match of source.matchAll(/(?:from\s*|import\s*\(\s*|import\s*)['"](\.{1,2}\/[^'"]+)['"]/g)){
   const target=resolve(dirname(file),match[1]);const outside=relative(installedRoot,target)
   if(outside.startsWith('..')||isAbsolute(outside))throw Error(`${relative(installedRoot,file)} imports outside the package: ${match[1]}`)
  }
 }
 const entrypoints=['contract','vercel','codex','deepagents','certification']
 const imports=entrypoints.map((entry,index)=>`import * as entry${index} from '@judgment-pack/harness/${entry}'`).join('\n')
 const consumer=join(temporary,'consumer.mjs')
 writeFileSync(consumer,`${imports}\nexport default [${entrypoints.map((_,index)=>`entry${index}`).join(',')}].map(entry=>Object.keys(entry).length)\n`)
 await build({
  root:temporary,configFile:false,logLevel:'silent',
  plugins:[{name:'empty-optional-ws',resolveId:id=>id==='ws'?'\0empty-ws':null,load:id=>id==='\0empty-ws'?'export default undefined':null}],
  build:{minify:false,reportCompressedSize:false,outDir:'bundle',lib:{entry:consumer,formats:['es'],fileName:'consumer'}}
 })
 execFileSync(process.execPath,['--input-type=module','--eval',"const counts=(await import('./bundle/consumer.js')).default;if(counts.length!==5||counts.some(count=>count===0))throw Error('a supported entrypoint exported nothing')"],{cwd:temporary,stdio:'inherit'})
 const installed=join(installedRoot,'contract','schemas','corpus-entry.json')
 if(!readFileSync(installed).equals(readFileSync(new URL('../../../contract/schemas/corpus-entry.json',import.meta.url))))throw Error('packed corpus schema differs from its repository original')
 console.log('packed harness: imported 5 supported entrypoints')
}finally{
 rmSync(temporary,{recursive:true,force:true})
}
