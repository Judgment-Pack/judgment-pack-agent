import {readFileSync,readdirSync} from 'node:fs'
import {join,resolve} from 'node:path'
import {describe,expect,it} from 'vitest'
import {AGENT_ENGINES,MODEL_ENGINES} from '../engines'
import {loadEngine} from '../engines'
import {recordSession,replayCorpus,type CorpusEntry} from '../corpus'
import type {AssistantEvent,EngineSession} from '../engine'
import {normalize} from '../thinking'
import {runOnAgentEngine,runOnModelEngine} from '../testing/scenarioDriver'

const root=resolve(process.env.JP_CORPUS_ROOT??resolve(import.meta.dirname,'../../../corpus'))
const entries=()=>readdirSync(root,{withFileTypes:true}).filter(row=>row.isDirectory()).map(row=>{
 const dir=join(root,row.name),read=(name:string)=>JSON.parse(readFileSync(join(dir,name),'utf8'))
 const manifest=read('manifest.json')
 return {manifest,requests:read(manifest.files.requests),toolResults:read(manifest.files.toolResults),events:read(manifest.files.events),checkpoint:read(manifest.files.checkpoint),expected:read(manifest.expected.file)} as CorpusEntry
})
const releaseRun=async(engineId:string,server:import('../corpus').ScriptedCorpusServer)=>{
 const engine=await loadEngine(engineId)
 const transport={engine,handle:(call:{name:string;args:Record<string,unknown>})=>server.tool(call.name,call.args),session:{critic:'off' as const}}
 if(engineId==='codex')return (await runOnAgentEngine((request,callbacks)=>server.agent(request,callbacks),transport)).events
 return (await runOnModelEngine(request=>server.model('chat/completions',{body:JSON.stringify(request.body)}),{...transport,session:{...transport.session,thinking:normalize('off','openai-compatible')}})).events
}

describe.each([...MODEL_ENGINES,...AGENT_ENGINES])('scenario 22 · %s',engine=>{
 it('replays both seed entries without a provider',async()=>{for(const entry of entries().filter(row=>engine==='codex'?row.manifest.id.startsWith('agent-'):row.manifest.id.startsWith('model-')))await replayCorpus(entry,server=>releaseRun(engine,server))})
 it('names a tampered expectation member',async()=>{const entry=structuredClone(entries().find(row=>engine==='codex'?row.manifest.id.startsWith('agent-'):row.manifest.id.startsWith('model-'))!);entry.expected.outcomes=[{type:'proposal'}];await expect(replayCorpus(entry,server=>releaseRun(engine,server))).rejects.toThrow('/outcomes/length differs')})
 it('record true round-trips through the output guard',async()=>{const seed=entries()[0]!,files:Record<string,string>={},session={record:true,corpus:{manifest:seed.manifest,guard:{value:<T>(value:T)=>value},sink:{write(_id:string,written:Record<string,string>){Object.assign(files,written)}}},model:{family:'openai-compatible',model:'scripted',call:async()=>new Response('{"ok":true}',{status:200})}} as unknown as EngineSession
  const recording=recordSession(session);if(!('model'in recording.session))throw Error('model session required');await recording.session.model.call('chat/completions',{body:'{}'})
  const source=async function*(){for(const event of seed.events)yield event};const output:AssistantEvent[]=[];for await(const event of recording.stream(source()))output.push(event)
  expect(output.at(-2)?.type).toBe('recorded');const manifest=JSON.parse(files['manifest.json']!),entry={manifest,requests:JSON.parse(files[manifest.files.requests]!),toolResults:JSON.parse(files[manifest.files.toolResults]!),events:JSON.parse(files[manifest.files.events]!),checkpoint:JSON.parse(files[manifest.files.checkpoint]!),expected:JSON.parse(files[manifest.expected.file]!)} as CorpusEntry
  await replayCorpus(entry,async server=>{await server.model('chat/completions',{body:'{}'});return structuredClone(entry.events)})
 })
})

it('records and replays a real Codex run with ordered events and tool callbacks',async()=>{
 const engine=await loadEngine('codex'),files:Record<string,string>={}
 const manifest={...entries().find(row=>row.manifest.id.startsWith('agent-'))!.manifest,id:'agent-recorded-round-trip'}
 const original=await runOnAgentEngine(async(request,callbacks)=>{
  await callbacks.event({type:'text',id:'intro',text:'Inspecting.'})
  const tool=request.tools.find(tool=>tool.name==='get_schema'||tool.description.startsWith('get_schema:'))!
  await callbacks.tool({id:'schema-call',name:tool.name,arguments:{}},callbacks.signal)
  await callbacks.event({type:'message',id:'intro',text:'Inspection complete.',phase:'final'})
 },{engine,session:{critic:'off',record:true,corpus:{manifest,guard:{value:<T>(value:T)=>value},sink:{write(_id,written){Object.assign(files,written)}}}}})
 expect(original.events.filter(e=>e.type==='error')).toEqual([])
 expect(original.events.some(e=>e.type==='recorded')).toBe(true)
 const entry:CorpusEntry={manifest:JSON.parse(files['manifest.json']!),requests:JSON.parse(files['requests.json']!),toolResults:JSON.parse(files['tool-results.json']!),events:JSON.parse(files['events.json']!),checkpoint:JSON.parse(files['checkpoint.json']!),expected:JSON.parse(files['expected.json']!)}
 expect(JSON.parse(entry.requests[0]!.response.body).steps.map((step:object)=>Object.keys(step)[0])).toEqual(['event','tool','event'])
 expect(entry.toolResults).toHaveLength(1)
 await replayCorpus(entry,server=>releaseRun('codex',server))
})
