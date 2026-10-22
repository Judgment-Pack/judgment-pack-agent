import {execFileSync} from 'node:child_process'
import {existsSync,mkdirSync,mkdtempSync,readdirSync,readFileSync,realpathSync,rmSync,statSync,writeFileSync} from 'node:fs'
import {tmpdir} from 'node:os'
import {dirname,extname,isAbsolute,join,relative,resolve,sep} from 'node:path'
import {fileURLToPath,pathToFileURL} from 'node:url'
import {initSync,parse} from 'es-module-lexer'
import {build} from 'vite'

const packageDirectory=fileURLToPath(new URL('..',import.meta.url))
const namedExports=['./contract','./vercel','./codex','./deepagents','./certification']
const npm=process.platform==='win32'?'npm.cmd':'npm'
initSync()

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
 const [imports]=parse(source,`${file}`)
 const found=imports.flatMap(row=>{
  if(typeof row.n==='string')return [row.n]
  if(row.d>=0&&row.d!==-2)throw Error(`${file}: dynamic import is not a string literal`)
  return []
 })
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
  const local=importSpecifiers(file).map(specifier=>resolveLocalImport(root,file,specifier)).filter(Boolean)
  graph.set(realpathSync(file),local)
 }
 return graph
}

export function packageEntrypoints(packageRoot){
 const root=realpathSync(packageRoot)
 const manifest=JSON.parse(readFileSync(join(root,'package.json'),'utf8'))
 const entries=[]
 for(const key of namedExports){
  const target=exportTarget(manifest.exports?.[key])
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

export async function packedSmoke(){
 const temporary=mkdtempSync(join(tmpdir(),'judgment-pack-harness-'))
 try{
  const packed=JSON.parse(execFileSync(npm,['pack','--json','--pack-destination',temporary],{cwd:packageDirectory,encoding:'utf8'}))
  const tarball=join(temporary,packed[0].filename)
  writeFileSync(join(temporary,'package.json'),'{"private":true,"type":"module"}\n')
  execFileSync(npm,['install','--ignore-scripts','--no-audit','--no-fund',tarball],{cwd:temporary,stdio:'inherit'})
  const installedRoot=join(temporary,'node_modules','@judgment-pack','harness')
  const entries=validatePackage(installedRoot)
  const consumers=join(temporary,'consumers')
  mkdirSync(consumers,{recursive:true})
  const inputs={}
  for(const [index,entry] of entries.entries()){
   const name=`entry-${index}`
   const consumer=join(consumers,`${name}.mjs`)
   writeFileSync(consumer,`import * as entry from ${JSON.stringify(entry.specifier)}\nexport default Object.keys(entry).length\n`)
   inputs[name]=consumer
  }
  // These bare, non-runtime seams are deliberately outside the package-local dependency proof.
  const virtual=new Map([['ws','export default undefined'],['vitest','export const vi={stubGlobal(){}}']])
  await build({
   root:temporary,configFile:false,logLevel:'silent',
   plugins:[{name:'non-runtime-bare-imports',resolveId:id=>virtual.has(id)?`\0empty-${id}`:null,load:id=>id.startsWith('\0empty-')?virtual.get(id.slice(7)):null}],
   build:{minify:false,reportCompressedSize:false,outDir:'bundle',rollupOptions:{preserveEntrySignatures:'strict',input:inputs,output:{entryFileNames:'[name].js'}}}
  })
  const names=JSON.stringify(Object.keys(inputs).map(name=>`./bundle/${name}.js`))
  execFileSync(process.execPath,['--input-type=module','--eval',`for(const file of ${names}){const loaded=await import(file);if(typeof loaded.default!=='number')throw Error(file+' did not import')}`],{cwd:temporary,stdio:'inherit'})
  const installed=join(installedRoot,'contract','schemas','corpus-entry.json')
  if(!readFileSync(installed).equals(readFileSync(new URL('../../../contract/schemas/corpus-entry.json',import.meta.url))))throw Error('packed corpus schema differs from its repository original')
  console.log(`packed harness: imported ${entries.length} supported entrypoints`)
 }finally{
  rmSync(temporary,{recursive:true,force:true})
 }
}

if(process.argv[1]&&realpathSync(process.argv[1])===realpathSync(fileURLToPath(import.meta.url)))await packedSmoke()
