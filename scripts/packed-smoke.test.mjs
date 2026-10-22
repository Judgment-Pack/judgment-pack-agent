import assert from 'node:assert/strict'
import {mkdirSync,mkdtempSync,rmSync,writeFileSync} from 'node:fs'
import {tmpdir} from 'node:os'
import {dirname,join,resolve} from 'node:path'
import test from 'node:test'
import {bundleEntrypoints,inspectEmittedModules,packageEntrypoints,validatePackage} from '../packages/harness/scripts/packed-smoke.mjs'

const fixture=files=>{
 const root=mkdtempSync(join(tmpdir(),'packed-smoke-test-'))
 for(const [name,contents] of Object.entries(files)){const path=join(root,name);mkdirSync(dirname(path),{recursive:true});writeFileSync(path,contents)}
 return root
}
test('rejects an absolute import',t=>{
 const root=fixture({'dist/entry.js':"import '/outside.js'\n"});t.after(()=>rmSync(root,{recursive:true,force:true}))
 assert.throws(()=>inspectEmittedModules(root),/imports outside the package/)
})

test('rejects a file URL import',t=>{
 const root=fixture({'dist/entry.js':"import 'file:///outside.js'\n"});t.after(()=>rmSync(root,{recursive:true,force:true}))
 assert.throws(()=>inspectEmittedModules(root),/imports outside the package/)
})

test('checks dynamic import and require targets',t=>{
 const dynamic=fixture({'dist/entry.js':"import('./missing-dynamic.js')\n"})
 const required=fixture({'dist/entry.js':"module.require('./missing-required.js')\n"})
 t.after(()=>{rmSync(dynamic,{recursive:true,force:true});rmSync(required,{recursive:true,force:true})})
 assert.throws(()=>inspectEmittedModules(dynamic),/imports a missing package file/)
 assert.throws(()=>inspectEmittedModules(required),/imports a missing package file/)
})

test('rejects a missing dependency of a wildcard-exported module',t=>{
 const exports={"./contract":"./dist/good.js","./vercel":"./dist/good.js","./codex":"./dist/good.js","./deepagents":"./dist/good.js","./certification":"./dist/good.js","./*":"./dist/*.js"}
 const root=fixture({'package.json':JSON.stringify({name:'fixture',exports}),'dist/good.js':'export const good=true\n','dist/broken.js':"import './missing.js'\n"});t.after(()=>rmSync(root,{recursive:true,force:true}))
 assert(packageEntrypoints(root).some(entry=>entry.specifier==='fixture/broken'))
 assert.throws(()=>validatePackage(root),/imports a missing package file/)
})

test('accepts the built harness and all of its exported modules',()=>{
 const root=resolve('packages/harness')
 const entries=validatePackage(root)
 assert(entries.some(entry=>entry.specifier==='@judgment-pack/harness/contract'))
 assert(entries.some(entry=>entry.specifier==='@judgment-pack/harness/corpus'))
})

test('bundles an extensionless package import from an external project',async t=>{
 const project=fixture({
  'package.json':'{"private":true,"type":"module"}\n',
  'node_modules/fixture/package.json':JSON.stringify({name:'fixture',type:'module',exports:{'./entry':'./dist/entry.js'}}),
  'node_modules/fixture/dist/entry.js':"export {answer} from './answer'\n",
  'node_modules/fixture/dist/answer.js':'export const answer=42\n'
 })
 t.after(()=>rmSync(project,{recursive:true,force:true}))
 const packageRoot=join(project,'node_modules','fixture')
 await bundleEntrypoints(project,packageRoot,[{specifier:'fixture/entry'}])

 const broken=fixture({
  'package.json':'{"private":true,"type":"module"}\n',
  'node_modules/fixture/package.json':JSON.stringify({name:'fixture',type:'module',exports:{'./entry':'./dist/entry.js'}}),
  'node_modules/fixture/dist/entry.js':"export {answer} from './missing'\n"
 })
 t.after(()=>rmSync(broken,{recursive:true,force:true}))
 await assert.rejects(()=>bundleEntrypoints(broken,join(broken,'node_modules','fixture'),[{specifier:'fixture/entry'}]),/Could not resolve/)
})
