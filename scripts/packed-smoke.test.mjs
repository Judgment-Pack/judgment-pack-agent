// Dependency regressions run with the packed-consumer check.
import './zod-size.test.mjs'
import assert from 'node:assert/strict'
import {mkdirSync,mkdtempSync,readFileSync,rmSync,writeFileSync} from 'node:fs'
import {tmpdir} from 'node:os'
import {dirname,join,resolve} from 'node:path'
import test from 'node:test'
import {bundleEntrypoints,consumerDirectory,inspectEmittedModules,nativeEntrypoints,packageEntrypoints,validateDeclarations,validatePackage,validateRuntimeDependencies} from '../packages/harness/scripts/packed-smoke.mjs'

import {normalizeEmittedModules,rewriteMap} from '../packages/harness/scripts/normalize-esm.mjs'
import {decode} from '@jridgewell/sourcemap-codec'
import {requireFixedZod,traceZodResolutions} from '../packages/harness/scripts/zod-consumer.mjs'

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
 assert.throws(()=>inspectEmittedModules(dynamic),/imports a missing package file|missing emitted import/)
 assert.throws(()=>inspectEmittedModules(required),/imports a missing package file|missing emitted import/)
})

test('rejects a missing dependency of a wildcard-exported module',t=>{
 const exports={"./contract":"./dist/good.js","./vercel":"./dist/good.js","./codex":"./dist/good.js","./deepagents":"./dist/good.js","./certification":"./dist/good.js","./*":"./dist/*.js"}
 const root=fixture({'package.json':JSON.stringify({name:'fixture',exports}),'dist/good.js':'export const good=true\n','dist/broken.js':"import './missing.js'\n"});t.after(()=>rmSync(root,{recursive:true,force:true}))
 assert(packageEntrypoints(root).some(entry=>entry.specifier==='fixture/broken'))
 assert.throws(()=>validatePackage(root),/imports a missing package file|missing emitted import/)
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


test('normalizes file, dotted file, directory, dynamic and declaration imports deterministically',t=>{
 const source=`import {answer} from './answer'; export * from './directory'; export * from './dotted.name';
import external from 'external/package'; import fs from 'node:fs';
export const load=()=>import('./directory');
export const text="import('./answer')"; // import './answer'
`
 const declarations="export type {Answer} from './answer'; export * from './directory'; export declare const x: import('./answer').Answer;\n"
 const root=fixture({'entry.js':source,'entry.d.ts':declarations,'answer.js':'export const answer=42;','answer.d.ts':'export type Answer=42;','directory/index.js':'export const found=true;','directory/index.d.ts':'export type Found=true;','dotted.name.js':'export const dotted=true;'})
 t.after(()=>rmSync(root,{recursive:true,force:true}))
 normalizeEmittedModules(root)
 assert.equal(readFileSync(join(root,'entry.js'),'utf8'),`import {answer} from "./answer.js"; export * from "./directory/index.js"; export * from "./dotted.name.js";
import external from 'external/package'; import fs from 'node:fs';
export const load=()=>import("./directory/index.js");
export const text="import('./answer')"; // import './answer'
`)
 assert.equal(readFileSync(join(root,'entry.d.ts'),'utf8'),'export type {Answer} from "./answer.js"; export * from "./directory/index.js"; export declare const x: import("./answer.js").Answer;\n')
 const first=readFileSync(join(root,'entry.js'),'utf8')
 const firstDeclaration=readFileSync(join(root,'entry.d.ts'),'utf8')
 normalizeEmittedModules(root)
 assert.equal(readFileSync(join(root,'entry.js'),'utf8'),first)
 assert.equal(readFileSync(join(root,'entry.d.ts'),'utf8'),firstDeclaration)
})

test('adds JSON attributes for imports, re-exports and dynamic imports and preserves existing attributes',t=>{
 const root=fixture({'entry.js':"import data from './data.json'; export {default as json} from './data.json'; export const load=()=>import('./data.json'); import existing from './data.json' with {type:'json'};",'data.json':'{"answer":42}'})
 t.after(()=>rmSync(root,{recursive:true,force:true}))
 normalizeEmittedModules(root)
 const expected="import data from './data.json' with { type: 'json' }; export {default as json} from './data.json' with { type: 'json' }; export const load=()=>import('./data.json', { with: { type: 'json' } }); import existing from './data.json' with {type:'json'};"
 assert.equal(readFileSync(join(root,'entry.js'),'utf8'),expected)
 normalizeEmittedModules(root)
 assert.equal(readFileSync(join(root,'entry.js'),'utf8'),expected)
})

test('fails missing, computed and escaping imports without partially rewriting files',t=>{
 for(const bad of ["export * from './missing'", "import(name)", "import '../outside.js'"]){
  const root=fixture({'a.js':"export * from './ok'",'ok.js':'export const ok=true;','z.js':bad})
  t.after(()=>rmSync(root,{recursive:true,force:true}))
  assert.throws(()=>normalizeEmittedModules(root),/missing emitted import|not a string literal|escapes emitted tree/)
  assert.equal(readFileSync(join(root,'a.js'),'utf8'),"export * from './ok'")
 }
})

test('moves generated source-map columns without changing original sources or positions',()=>{
 const source="import './a'; export const answer=42;\n"
 // Handwritten mappings at columns 0, 7, 12, 14 and 27, all to source line 0.
 const map={version:3,file:'entry.js',sourceRoot:'../',sources:['entry.ts'],sourcesContent:[source],names:[],mappings:'AAAA,OAAO,KAAK,EAAE,aAAa'}
 const output=rewriteMap(map,source,[{start:7,end:12,text:'"./a.js"'},{start:12,end:12,text:" with { type: 'json' }"}])
 assert.deepEqual(decode(output.mappings),[[[0,0,0,0],[7,0,0,7],[37,0,0,12],[39,0,0,14],[52,0,0,27]]])
 assert.deepEqual({...output,mappings:map.mappings},map)
})

test('normalizes adjacent JavaScript and declaration maps deterministically',t=>{
 const map={version:3,file:'entry.js',sourceRoot:'',sources:['../entry.ts'],names:[],mappings:'AAAA,OAAO,KAAK'}
 const root=fixture({'entry.js':"import './a';\n//# sourceMappingURL=entry.js.map\n",'entry.js.map':JSON.stringify(map),'entry.d.ts':"export * from './a';\n//# sourceMappingURL=entry.d.ts.map\n",'entry.d.ts.map':JSON.stringify({...map,file:'entry.d.ts'}),'a.js':'export const a=42;','a.d.ts':'export declare const a:42;'})
 t.after(()=>rmSync(root,{recursive:true,force:true}))
 normalizeEmittedModules(root)
 for(const file of ['entry.js.map','entry.d.ts.map']){
  const first=readFileSync(join(root,file),'utf8')
  assert.equal(JSON.parse(first).sources[0],'../entry.ts')
  normalizeEmittedModules(root)
  assert.equal(readFileSync(join(root,file),'utf8'),first)
 }
})

test('native and declaration checks reject extensionless installed imports, then pass normalized imports',async t=>{
 const project=fixture({
  'package.json':'{"private":true,"type":"module"}\n',
  'node_modules/fixture/package.json':JSON.stringify({name:'fixture',type:'module',exports:{'./entry':{types:'./dist/entry.d.ts',default:'./dist/entry.js'}}}),
  'node_modules/fixture/dist/entry.js':"export {answer} from './directory'\n",
  'node_modules/fixture/dist/entry.d.ts':"export type {Answer} from './directory'\n",
  'node_modules/fixture/dist/directory/index.js':'export const answer=42\n',
  'node_modules/fixture/dist/directory/index.d.ts':'export type Answer=42\n'
 })
 t.after(()=>rmSync(project,{recursive:true,force:true}))
 const entries=[{specifier:'fixture/entry'}]
 assert.throws(()=>nativeEntrypoints(project,entries),/ERR_UNSUPPORTED_DIR_IMPORT|ERR_MODULE_NOT_FOUND/)
 assert.throws(()=>validateDeclarations(project,join(project,'node_modules','fixture'),entries),/explicit emitted module specifier|TS2834|TS2835/)
 normalizeEmittedModules(join(project,'node_modules','fixture','dist'))
 nativeEntrypoints(project,entries)
 validateDeclarations(project,join(project,'node_modules','fixture'),entries)
})


test('rejects a dev-only runtime import even when an ancestor checkout could supply it',t=>{
 const root=fixture({
  'package.json':JSON.stringify({name:'fixture',dependencies:{declared:'1.0.0'},devDependencies:{'development-only':'1.0.0'}}),
  'dist/entry.js':"import 'development-only'\n",
  'node_modules/development-only/package.json':'{"name":"development-only","type":"module","main":"index.js"}',
  'node_modules/development-only/index.js':'export const development=true;'
 })
 t.after(()=>rmSync(root,{recursive:true,force:true}))
 assert.throws(()=>validateRuntimeDependencies(root),/undeclared runtime dependency development-only/)
 writeFileSync(join(root,'dist','entry.js'),"import 'declared/subpath'; import 'node:fs'; import 'fs';\n")
 validateRuntimeDependencies(root)
})

test('refuses consumers inside the checkout or underneath ancestor node_modules',t=>{
 const checkout=fixture({'work/tmp/.keep':'','elsewhere/.keep':'','node_modules/dev/.keep':''})
 const unrelated=fixture({'.keep':''})
 t.after(()=>{rmSync(checkout,{recursive:true,force:true});rmSync(unrelated,{recursive:true,force:true})})
 assert.throws(()=>consumerDirectory(join(checkout,'work','tmp'),checkout),/outside the checkout/)
 assert.throws(()=>consumerDirectory(join(checkout,'elsewhere'),unrelated),/ancestor node_modules/)
})


test('traces a shared SDK Zod copy instead of trusting the harness-local pin',t=>{
 const project=fixture({
  'node_modules/@judgment-pack/harness/package.json':JSON.stringify({name:'@judgment-pack/harness',version:'fixture',dependencies:{'fixture-sdk':'1.0.0',zod:'4.6.7'}}),
  'node_modules/@judgment-pack/harness/node_modules/zod/package.json':'{"name":"zod","version":"4.6.7"}',
  'node_modules/fixture-sdk/package.json':JSON.stringify({name:'fixture-sdk',version:'1.0.0',peerDependencies:{zod:'^4'},exports:{'.':'./dist/absent-root.cjs','./client':'./dist/client.cjs','./*':'./dist/*'}}),
  'node_modules/fixture-sdk/dist/package.json':'{"type":"commonjs"}',
  'node_modules/fixture-sdk/dist/client.cjs':'module.exports={};',
  'node_modules/zod/package.json':'{"name":"zod","version":"4.4.3"}'
 })
 t.after(()=>rmSync(project,{recursive:true,force:true}))
 const root=join(project,'node_modules','@judgment-pack','harness')
 const rows=traceZodResolutions(root)
 assert.equal(rows.find(row=>row.owner==='@judgment-pack/harness').zodVersion,'4.6.7')
 assert.equal(rows.find(row=>row.owner==='fixture-sdk').zodVersion,'4.4.3')
 assert.throws(()=>requireFixedZod(rows),/retain a different Zod/)
 writeFileSync(join(project,'node_modules','zod','package.json'),'{"name":"zod","version":"4.6.7"}')
 requireFixedZod(traceZodResolutions(root))
 assert.throws(()=>requireFixedZod([]),/must not be empty/)
})
