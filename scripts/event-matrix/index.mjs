import {spawnSync} from 'node:child_process'
import {dirname,join} from 'node:path'
import {fileURLToPath} from 'node:url'
import {analyzeTypeScript} from './typescript.mjs'

export function requireCoverage(kinds,languages){
 for(const [language,data] of Object.entries(languages)){
  const missing=kinds.filter(kind=>!data.emits.some(site=>site.kind===kind))
  if(missing.length)throw Error(`events.md: ${language} has no emitter for ${missing.join(', ')}`)
 }
}
export function eventMatrix(root,kinds){
 const ts=analyzeTypeScript(root,kinds)
 const go=spawnSync('go',['run',join(dirname(fileURLToPath(import.meta.url)),'go.go'),root],{encoding:'utf8',timeout:120000})
 if(go.status!==0)throw Error(`event matrix Go analysis failed: ${go.stderr||go.error||go.status}`)
 const languages={TypeScript:ts,Go:JSON.parse(go.stdout)}
 requireCoverage(kinds,languages)
 return languages
}
export function locations(sites,kind){
 return [...new Set(sites.filter(site=>site.kind===kind).map(site=>`${site.file}:${site.line}`))].sort().map(where=>{
  const at=where.lastIndexOf(':'),file=where.slice(0,at),line=where.slice(at+1)
  return `[\`${where}\`](../../${file}#L${line})`
 }).join(', ')||'—'
}
