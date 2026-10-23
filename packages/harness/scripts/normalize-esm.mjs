import {existsSync,readFileSync,readdirSync,realpathSync,statSync,writeFileSync} from 'node:fs'
import {dirname,isAbsolute,join,relative,sep} from 'node:path'
import {fileURLToPath,pathToFileURL} from 'node:url'
import {parse} from '@babel/parser'
import {decode,encode} from '@jridgewell/sourcemap-codec'

const inside=(root,file)=>{const path=relative(root,file);return path===''||(!isAbsolute(path)&&path!=='..'&&!path.startsWith(`..${sep}`))}
const isFile=file=>existsSync(file)&&statSync(file).isFile()

// Parse declarations too: a JavaScript-only lexer can miss `export type ... from`.
export function moduleImports(source,file){
 const ast=parse(source,{sourceType:'module',plugins:file.endsWith('.d.ts')?['typescript']:[],createImportExpressions:true})
 const imports=[]
 const visit=node=>{
  if(!node||typeof node!=='object')return
  if(['ImportDeclaration','ExportNamedDeclaration','ExportAllDeclaration','ImportExpression','TSImportType','TSExternalModuleReference'].includes(node.type)){
   const literal=node.source??node.argument??node.expression
   if(literal){
    if(literal.type!=='StringLiteral')throw Error(`${file}: module import is not a string literal`)
    imports.push({specifier:literal.value,literal,node})
   }
  }
  for(const value of Object.values(node)){
   if(Array.isArray(value)){for(const child of value)if(child?.type)visit(child)}
   else if(value?.type)visit(value)
  }
 }
 visit(ast.program)
 return imports
}

// Resolve against the emitted tree, never against repository sources or node_modules.
export function emittedTarget(root,file,specifier){
 if(!specifier.startsWith('./')&&!specifier.startsWith('../'))return undefined
 const url=new URL(specifier,pathToFileURL(file))
 const base=fileURLToPath(url)
 if(!inside(root,base))throw Error(`${file}: import escapes emitted tree: ${specifier}`)
 const declarations=file.endsWith('.d.ts')
 const candidates=declarations
  ? [base.replace(/\.js$/,'.d.ts'),base,`${base}.d.ts`,`${base}.js`,join(base,'index.d.ts'),join(base,'index.js')]
  : [base,`${base}.js`,join(base,'index.js')]
 const target=candidates.find(isFile)
 if(!target)throw Error(`${file}: missing emitted import: ${specifier}`)
 if(declarations&&target.endsWith('.js'))throw Error(`${file}: missing emitted declaration: ${specifier}`)
 if(!inside(root,realpathSync(target)))throw Error(`${file}: import escapes emitted tree: ${specifier}`)
 const runtimeTarget=target.replace(/\.d\.ts$/,'.js')
 const targetURL=pathToFileURL(runtimeTarget)
 targetURL.search=url.search
 targetURL.hash=url.hash
 const path=relative(dirname(file),runtimeTarget).split(sep).map(encodeURIComponent).join('/')
 return {target,specifier:`${path.startsWith('../')?'':'./'}${path}${targetURL.search}${targetURL.hash}`,json:target.endsWith('.json')}
}

function jsonAttributes(node,file){
 if(node.type==='ImportExpression'){
  if(!node.options)return ", { with: { type: 'json' } }"
  const property=(object,name)=>object?.type==='ObjectExpression'?object.properties.find(row=>row.type==='ObjectProperty'&&(row.key.name??row.key.value)===name)?.value:undefined
  if(property(property(node.options,'with'),'type')?.value!=='json')throw Error(`${file}: JSON import requires with.type=json`)
 }else{
  if(!node.attributes?.length)return " with { type: 'json' }"
  if(!node.attributes.some(row=>(row.key.name??row.key.value)==='type'&&row.value.value==='json'))throw Error(`${file}: JSON import requires type=json`)
 }
 return ''
}

// Edits keep line breaks intact. Move generated columns while preserving the original
// TypeScript source, sourceRoot and source positions in both JS and declaration maps.
export function rewriteMap(map,source,edits){
 const lines=decode(map.mappings)
 const byLine=new Map()
 for(const edit of edits){
  const before=source.slice(0,edit.start)
  const line=before.split('\n').length-1
  const start=edit.start-(before.lastIndexOf('\n')+1)
  const row={start,end:start+edit.end-edit.start,length:edit.text.length}
  if(source.slice(edit.start,edit.end).includes('\n')||edit.text.includes('\n'))throw Error('module normalization cannot change line breaks')
  if(!byLine.has(line))byLine.set(line,[])
  byLine.get(line).push(row)
 }
 for(const [line,changes] of byLine){
  changes.sort((a,b)=>a.start-b.start)
  for(const segment of lines[line]??[]){
   const column=segment[0]
   let shift=0
   for(const change of changes){
    if(column<change.start)break
    if(column>=change.end)shift+=change.length-(change.end-change.start)
    else {shift+=Math.min(column-change.start,change.length)-(column-change.start);break}
   }
   segment[0]=column+shift
  }
 }
 return {...map,mappings:encode(lines)}
}

export function normalizeEmittedModules(directory){
 const root=realpathSync(directory)
 const files=[]
 const visit=at=>{for(const name of readdirSync(at).sort()){const file=join(at,name);if(statSync(file).isDirectory())visit(file);else if(file.endsWith('.js')||file.endsWith('.d.ts'))files.push(file)}}
 visit(root)
 const outputs=[]
 for(const file of files){
  const source=readFileSync(file,'utf8')
  const edits=[]
  for(const {specifier,literal,node} of moduleImports(source,file)){
   const resolved=emittedTarget(root,file,specifier)
   if(!resolved)continue
   if(resolved.specifier!==specifier)edits.push({start:literal.start,end:literal.end,text:JSON.stringify(resolved.specifier)})
   if(resolved.json&&node.type!=='TSImportType'&&node.type!=='TSExternalModuleReference'){
    const text=jsonAttributes(node,file)
    if(text)edits.push({start:literal.end,end:literal.end,text})
   }
  }
  if(!edits.length)continue
  edits.sort((a,b)=>a.start-b.start||a.end-b.end)
  let output=source
  for(const edit of [...edits].reverse())output=output.slice(0,edit.start)+edit.text+output.slice(edit.end)
  outputs.push([file,output])
  const mapFile=`${file}.map`
  if(existsSync(mapFile))outputs.push([mapFile,`${JSON.stringify(rewriteMap(JSON.parse(readFileSync(mapFile,'utf8')),source,edits))}\n`])
 }
 // Validate the entire tree before changing it; a missing module never leaves a partial rewrite.
 for(const [file,output] of outputs)writeFileSync(file,output)
 return files.length
}

if(process.argv[1]&&realpathSync(process.argv[1])===realpathSync(fileURLToPath(import.meta.url))){
 const count=normalizeEmittedModules(fileURLToPath(new URL('../dist',import.meta.url)))
 console.log(`native ESM: normalized ${count} emitted modules and declarations`)
}
