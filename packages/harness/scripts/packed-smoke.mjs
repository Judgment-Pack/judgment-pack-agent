import {execFileSync} from 'node:child_process'
import {existsSync,mkdirSync,mkdtempSync,readdirSync,readFileSync,realpathSync,rmSync,statSync,writeFileSync} from 'node:fs'
import {tmpdir} from 'node:os'
import {dirname,extname,isAbsolute,join,relative,resolve,sep} from 'node:path'
import {fileURLToPath,pathToFileURL} from 'node:url'
import {build} from 'esbuild'
import {emittedTarget,moduleImports} from './normalize-esm.mjs'

const packageDirectory=fileURLToPath(new URL('..',import.meta.url))
const namedExports=['./contract','./vercel','./codex','./deepagents','./certification']
const npm=process.platform==='win32'?'npm.cmd':'npm'

const filesUnder=(directory,accept)=>{
 const files=[]
 const visit=at=>{for(const name of readdirSync(at)){const path=join(at,name);if(statSync(path).isDirectory())visit(path);else if(accept(path))files.push(path)}}
 visit(directory)
 return files
}

const exportTarget=value=>typeof value==='string'?value:value?.default
const inside=(root,target)=>{const path=relative(root,target);return path===''||(!isAbsolute(path)&&path!=='..'&&!path.startsWith(`..${sep}`))}
const displayed=(root,path)=>inside(root,path)?relative(root,path):path

function importSpecifiers(file){
 const source=readFileSync(file,'utf8')
 const found=moduleImports(source,file).map(row=>row.specifier)
 const requires=/(?:\brequire|\bmodule\s*\.\s*require)\s*\(\s*(?:'([^'\\]*(?:\\.[^'\\]*)*)'|"([^"\\]*(?:\\.[^"\\]*)*)")\s*\)/g
 for(const match of source.matchAll(requires)){
  const specifier=match[1]??match[2]
  if(specifier!==undefined)found.push(specifier)
 }
 return found
}

function localBase(file,specifier){
 if(specifier.startsWith('file:')){
  try{return fileURLToPath(new URL(specifier,pathToFileURL(file)))}catch{throw Error(`${file}: invalid local import ${specifier}`)}
 }
 if(isAbsolute(specifier))return specifier
 if(/^\.{1,2}[\\/]/.test(specifier))return resolve(dirname(file),specifier)
 return undefined
}

function resolveLocalImport(packageRoot,file,specifier){
 const base=localBase(file,specifier)
 if(base===undefined)return undefined
 const root=realpathSync(packageRoot)
 if(!inside(root,base))throw Error(`${displayed(root,file)} imports outside the package: ${specifier}`)
 const candidates=[base]
 if(extname(base)==='')candidates.push(`${base}.js`,`${base}.json`,`${base}.node`,`${base}.d.ts`,join(base,'index.js'),join(base,'index.d.ts'))
 const target=candidates.find(path=>existsSync(path)&&statSync(path).isFile())
 if(target===undefined)throw Error(`${displayed(root,file)} imports a missing package file: ${specifier}`)
 const real=realpathSync(target)
 if(!inside(root,real))throw Error(`${displayed(root,file)} imports outside the package: ${specifier}`)
 return real
}

export function inspectEmittedModules(packageRoot){
 const root=realpathSync(packageRoot)
 const emitted=filesUnder(join(root,'dist'),file=>file.endsWith('.js')||file.endsWith('.d.ts'))
 const graph=new Map()
 for(const file of emitted){
  const local=importSpecifiers(file).map(specifier=>{
   // NodeNext declarations use runtime .js specifiers, resolved to their .d.ts twin.
   const emitted=emittedTarget(join(root,'dist'),file,specifier)
   if(emitted&&emitted.specifier!==specifier)throw Error(`${displayed(root,file)} needs an explicit emitted module specifier: ${specifier}`)
   return emitted?.target??resolveLocalImport(root,file,specifier)
  }).filter(Boolean)
  graph.set(realpathSync(file),local)
 }
 return graph
}

export function packageEntrypoints(packageRoot){
 const root=realpathSync(packageRoot)
 const manifest=JSON.parse(readFileSync(join(root,'package.json'),'utf8'))
 const entries=[]
 for(const key of namedExports)if(!manifest.exports?.[key])throw Error(`package export ${key} is missing its runtime target`)
 for(const [key,value] of Object.entries(manifest.exports??{})){
  if(key.includes('*'))continue
  const target=exportTarget(value)
  if(typeof target!=='string')throw Error(`package export ${key} is missing its runtime target`)
  entries.push({specifier:`${manifest.name}${key.slice(1)}`,target:resolveLocalImport(root,join(root,'package.json'),target)})
 }
 for(const [key,value] of Object.entries(manifest.exports??{})){
  if(!key.includes('*'))continue
  const target=exportTarget(value)
  if(typeof target!=='string'||!target.includes('*'))throw Error(`package export ${key} has no wildcard runtime target`)
  const [prefix,suffix]=target.split('*')
  for(const file of filesUnder(root,path=>path.endsWith(suffix))){
   const packaged=`./${relative(root,file).split(sep).join('/')}`
   if(!packaged.startsWith(prefix)||!packaged.endsWith(suffix))continue
   const wildcard=packaged.slice(prefix.length,packaged.length-suffix.length)
   entries.push({specifier:`${manifest.name}${key.slice(1).replace('*',wildcard)}`,target:realpathSync(file)})
  }
 }
 return entries
}

export function validatePackage(packageRoot){
 const root=realpathSync(packageRoot)
 const graph=inspectEmittedModules(root)
 const entries=packageEntrypoints(root)
 for(const entry of entries){
  const seen=new Set()
  const walk=file=>{
   if(seen.has(file))return
   seen.add(file)
   const imports=graph.get(file)
   if(imports===undefined)throw Error(`${entry.specifier} resolves to a file outside the emitted module set`)
   for(const imported of imports)if(graph.has(imported))walk(imported)
  }
  walk(entry.target)
 }
 return entries
}

export async function bundleEntrypoints(projectRoot,packageRoot,entries){
 const consumers=join(projectRoot,'consumers')
 mkdirSync(consumers,{recursive:true})
 const inputs={}
 for(const [index,entry] of entries.entries()){
  const name=`entry-${index}`
  const consumer=join(consumers,`${name}.mjs`)
  writeFileSync(consumer,`import * as entry from ${JSON.stringify(entry.specifier)}\nexport default Object.keys(entry).length\n`)
  inputs[name]=consumer
 }
 await build({
  absWorkingDir:projectRoot,
  entryPoints:inputs,
  bundle:true,
  format:'esm',
  outdir:'bundle',
  platform:'browser',
  treeShaking:false,
  write:false,
  logLevel:'silent',
  plugins:[{
   name:'external-package-dependencies',
   setup(esbuild){
    esbuild.onResolve({filter:/^(?:@[^/]+\/)?[^./][^:]*/},args=>inside(packageRoot,args.importer)?{path:args.path,external:true}:undefined)
   }
  }]
 })
}

export function nativeEntrypoints(projectRoot,entries){
 const consumer=join(projectRoot,'native-consumer.mjs')
 writeFileSync(consumer,`import {strictEqual} from 'node:assert'
const seen=new Map()
let failed=0
for(const entry of ${JSON.stringify(entries)}){
 try{
  const namespace=await import(entry.specifier)
  if(entry.target&&seen.has(entry.target))strictEqual(namespace,seen.get(entry.target))
  if(entry.target)seen.set(entry.target,namespace)
 }catch(error){failed++;console.error(entry.specifier,error)}
}
if(failed)process.exitCode=1
`)
 return execFileSync(process.execPath,[consumer],{cwd:projectRoot,encoding:'utf8',env:{...process.env,NODE_OPTIONS:''},stdio:'pipe'})
}

export function validateDeclarations(projectRoot,packageRoot,entries){
 // Check every local declaration edge ourselves. The consumer uses the package's
 // existing skipLibCheck policy for dependency declaration bodies.
 inspectEmittedModules(packageRoot)
 const consumer=join(projectRoot,'declaration-consumer.mts')
 writeFileSync(consumer,entries.map((entry,index)=>`import * as entry${index} from ${JSON.stringify(entry.specifier)}\nexport type Entry${index}=typeof entry${index}\n`).join(''))
 const config=join(projectRoot,'declaration-tsconfig.json')
 writeFileSync(config,JSON.stringify({compilerOptions:{module:'NodeNext',moduleResolution:'NodeNext',target:'ES2022',lib:['ES2022','DOM','DOM.Iterable'],strict:true,noEmit:true,skipLibCheck:true,resolveJsonModule:true,types:[]},files:[consumer]}))
 const compiler=fileURLToPath(new URL('./bin/tsc',import.meta.resolve('typescript/package.json')))
 try{return execFileSync(process.execPath,[compiler,'-p',config],{cwd:projectRoot,encoding:'utf8',stdio:'pipe'})}
 catch(error){throw Error(`packed declaration resolution failed:\n${error.stdout??''}${error.stderr??''}`,{cause:error})}
}

export async function packedSmoke(){
 const temporary=mkdtempSync(join(tmpdir(),'judgment-pack-harness-'))
 try{
  const packed=JSON.parse(execFileSync(npm,['pack','--json','--pack-destination',temporary],{cwd:packageDirectory,encoding:'utf8'}))
  const tarball=join(temporary,packed[0].filename)
  writeFileSync(join(temporary,'package.json'),'{"private":true,"type":"module"}\n')
  execFileSync(npm,['install','--ignore-scripts','--no-audit','--no-fund',tarball],{cwd:temporary,stdio:'inherit'})
  const installedRoot=join(temporary,'node_modules','@judgment-pack','harness')
  const entries=validatePackage(installedRoot)
  nativeEntrypoints(temporary,entries)
  await bundleEntrypoints(temporary,installedRoot,entries)
  validateDeclarations(temporary,installedRoot,entries)
  const installed=join(installedRoot,'contract','schemas','corpus-entry.json')
  if(!readFileSync(installed).equals(readFileSync(new URL('../../../contract/schemas/corpus-entry.json',import.meta.url))))throw Error('packed corpus schema differs from its repository original')
  console.log(`packed harness: imported ${entries.length} supported entrypoints with native Node ESM, bundled them, and resolved their declarations from an isolated installed tarball`)
 }finally{
  rmSync(temporary,{recursive:true,force:true})
 }
}

if(process.argv[1]&&realpathSync(process.argv[1])===realpathSync(fileURLToPath(import.meta.url)))await packedSmoke()
