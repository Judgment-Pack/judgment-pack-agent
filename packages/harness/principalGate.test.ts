// @vitest-environment node
// ADR-0029: a delegated run's source gate admits each call under its chain before dispatch, and refuses
// by name: a tool, effect or source not granted, a limit, expiry, revocation; an approval never carries
// authority past the delegation; an undelegated gate asks nothing.
import {describe,it,expect,beforeEach} from 'vitest'
import chains from '../../contract/vectors/principal/chains.json'
import {declareSources,dispatchAdmission,SourceGate,toolListDigest,type ApprovalBook} from './toolSources'
import {openPrincipal,type AdmissionRead,type AdmissionAnswer} from './principal'
import {CONTEXT_DEFAULTS} from './context/policy'
import {GateViolation} from './toolGate'
import {configureHarness} from './host'
import {testHost} from './testing/host'
import type {HostTool,McpToolResult} from './engine'
import type {ApprovalRecord,SourceFigures} from './execution/state'

beforeEach(()=>configureHarness(testHost()))
const T='2026-10-30T01:00:00.000Z'
const ok=():McpToolResult=>({content:[{type:'text',text:'done'}]})
type Port={answer:AdmissionAnswer|'throw';answerFor?:(read:AdmissionRead)=>AdmissionAnswer;reads:AdmissionRead[];now:string}

async function tools(ran:string[],extra:Partial<Record<string,Partial<HostTool>>>={}):Promise<HostTool[]>{
 const notes=[['read_note','read','read'],['write_note','act','act'],['delete_note','act','act']] as const
 const base=(name:string,operation:string,effect:string,source:Record<string,unknown>):HostTool=>({name,description:name,inputSchema:{type:'object'},taxonomy:{operation,effect} as HostTool['taxonomy'],execute:async()=>{ran.push(name);return ok()},...(effect==='act'?{approve:()=>'allow' as const}:{}),source:source as unknown as HostTool['source'],...extra[name]})
 const notesTools=notes.map(([name,operation,effect])=>base(name,operation,effect,{kind:'custom',id:'notes',trust:'first-party'}))
 // A gateway's read is read-external: its receipt is checked by the host's verifier before anything of it is kept.
 const external=(name:string,source:Record<string,unknown>):HostTool=>({...base(name,'read','read-external',{...source,verifyReceipt:()=>true}),execute:async()=>{ran.push(name);return {...ok(),receipt:'r-1'} as McpToolResult}})
 const tickets=[external('query',{kind:'gateway',id:'tickets',trust:'connected',gatewayScope:{authority:'gateway:test',source:'tickets/live'}})]
 const unmapped=[external('lookup',{kind:'gateway',id:'records',trust:'connected'})]
 const pin=async(group:HostTool[])=>{const digest=await toolListDigest(group.map(t=>({name:t.name,description:t.description,inputSchema:t.inputSchema,taxonomy:t.taxonomy}))as never);return group.map(t=>({...t,source:{...t.source!,toolListDigest:digest}}))}
 return [...await pin(notesTools),...await pin(tickets),...await pin(unmapped)]
}
async function delegatedGate(port:Port,ran:string[],book?:ApprovalBook,extra?:Partial<Record<string,Partial<HostTool>>>,chain:'root'|'child'='root'){
 const declared=await declareSources(await tools(ran,extra),[])
 const notices:{tool:string;detail:string;reason?:string;refusal?:unknown}[]=[]
 const rows:SourceFigures[]=[]
 const gate=new SourceGate(declared.entries,declared.sources,CONTEXT_DEFAULTS,'marker',(tool,detail,reason,refusal)=>{notices.push({tool,detail,...(reason===undefined?{}:{reason}),...(refusal===undefined?{}:{refusal})});return new GateViolation(tool,detail)},()=>rows,'json-schema',undefined,book)
 gate.usePrincipal(await openPrincipal({chain:JSON.stringify(chain==='child'?chains.items[1]:chains.items[0]),keys:chains.keys,software:chains.software,audience:chains.audience,clock:()=>Date.parse(port.now),admission:read=>{port.reads.push(read);if(port.answerFor)return port.answerFor(read);if(port.answer==='throw')throw Error('port down');return port.answer}}))
 const byName=new Map(declared.hostTools.map(t=>[t.name,t]))
 // As the session runs a call: the final admission, at the dispatch, and then the host's execute.
 const call=(name:string,args:Record<string,unknown>={},callId?:string)=>gate.call(byName.get(name)!,args,new AbortController().signal,async(given,signal)=>{await dispatchAdmission(signal)?.();return byName.get(name)!.execute(given,signal)},callId)
 return {gate,notices,rows,call,byName}
}
const admit=():Port=>({answer:{admission:'admit'},reads:[],now:T})

describe('a delegated run admits each call before dispatch (ADR-0029)',()=>{
 it('runs a call within the scope, counted once at its dispatch',async()=>{
  const port=admit(),ran:string[]=[]
  const {call}=await delegatedGate(port,ran)
  await call('notes__read_note')
  expect(ran).toEqual(['read_note'])
  expect(port.reads.map(r=>[r.call.tool,r.call.count])).toEqual([['read_note',false],['read_note',true]])
 })
 it('refuses a tool the scope does not name before dispatch, by name, counting nothing',async()=>{
  const port=admit(),ran:string[]=[]
  const {call,notices,rows}=await delegatedGate(port,ran)
  await expect(call('notes__delete_note')).rejects.toBeInstanceOf(GateViolation)
  expect(ran).toEqual([])
  expect(notices.at(-1)).toMatchObject({reason:'delegation refused',refusal:{name:'out-of-scope',reason:'tool'}})
  // An act call: the decision's check before any approval, then the call's own; neither is counted.
  expect(port.reads.map(r=>r.call.count)).toEqual([false,false])
  expect(rows.find(r=>r.source==='notes')).toMatchObject({calls:0,refused:1})
 })
 it('refuses after expiry, asking nothing',async()=>{
  const port={...admit(),now:'2026-10-30T08:00:00.000Z'},ran:string[]=[]
  const {call,notices}=await delegatedGate(port,ran)
  await expect(call('notes__read_note')).rejects.toBeInstanceOf(GateViolation)
  expect(ran).toEqual([])
  expect(notices.at(-1)).toMatchObject({reason:'delegation refused',refusal:{name:'expired'}})
  expect(port.reads).toEqual([])
 })
 it('counts nothing for an act call revoked before its approval',async()=>{
  const port={...admit(),answer:{admission:'refuse',refusal:'revoked'} as AdmissionAnswer},ran:string[]=[]
  const {call,notices}=await delegatedGate(port,ran)
  await expect(call('notes__write_note')).rejects.toBeInstanceOf(GateViolation)
  expect(ran).toEqual([])
  expect(notices.at(-1)).toMatchObject({refusal:{name:'revoked'}})
  // The decision's check and the call's: the call is not dispatched, so neither asks the host to count it.
  expect(port.reads.map(r=>r.call.count)).toEqual([false,false])
 })
 it('refuses after revocation, at once, on the next admission',async()=>{
  const port=admit(),ran:string[]=[]
  const {call,notices}=await delegatedGate(port,ran)
  await call('notes__read_note')
  port.answer={admission:'refuse',refusal:'revoked'}
  await expect(call('notes__read_note')).rejects.toBeInstanceOf(GateViolation)
  expect(ran).toEqual(['read_note'])
  expect(notices.at(-1)).toMatchObject({refusal:{name:'revoked'}})
 })
 it('refuses a call past the calls limit the issuing host counts',async()=>{
  const port={...admit(),answer:{admission:'refuse',refusal:'out-of-scope',reason:'limit'} as AdmissionAnswer},ran:string[]=[]
  const {call,notices}=await delegatedGate(port,ran)
  await expect(call('notes__read_note')).rejects.toBeInstanceOf(GateViolation)
  expect(ran).toEqual([])
  expect(notices.at(-1)).toMatchObject({refusal:{name:'out-of-scope',reason:'limit'}})
 })
 it('refuses a call whose admission cannot be read',async()=>{
  const port={...admit(),answer:'throw' as const},ran:string[]=[]
  const {call,notices}=await delegatedGate(port,ran)
  await expect(call('notes__read_note')).rejects.toBeInstanceOf(GateViolation)
  expect(ran).toEqual([])
  expect(notices.at(-1)).toMatchObject({refusal:{name:'admission-unknown'}})
 })
 it("checks a gateway source against its gateway's entry without counting, and refuses one with no mapping",async()=>{
  const port=admit(),ran:string[]=[]
  const {call,notices}=await delegatedGate(port,ran)
  await call('tickets__query')
  expect(port.reads.map(r=>[r.audience,r.call.source,r.call.count])).toEqual([[{kind:'gateway',name:'gateway:test'},'tickets/live',false],[{kind:'gateway',name:'gateway:test'},'tickets/live',false]])
  await expect(call('records__lookup')).rejects.toBeInstanceOf(GateViolation)
  expect(notices.at(-1)).toMatchObject({refusal:{name:'out-of-scope',reason:'source'}})
  // A gateway source with no mapping names no audience: nothing is read for it.
  expect(port.reads.length).toBe(2)
  expect(ran).toEqual(['query'])
 })
 it('never puts a call the delegation refuses to the person, and checks before asking without counting',async()=>{
  const port=admit(),ran:string[]=[],asked:string[]=[]
  const policy={approve:()=>{asked.push('delete');return 'ask' as const}}
  const {gate}=await delegatedGate(port,ran,undefined,{delete_note:policy})
  expect((await gate.decide('notes__delete_note',{},'call-1')).answer).toBe('checked')
  expect(asked).toEqual([])
  expect(port.reads.map(r=>r.call.count)).toEqual([false])
 })
 it('admits again immediately before dispatch: a revocation after the check and before the call refuses it',async()=>{
  const port=admit(),ran:string[]=[]
  const {gate,call}=await delegatedGate(port,ran)
  expect((await gate.decide('notes__write_note',{},'call-2')).answer).toBe('allow')
  port.answer={admission:'refuse',refusal:'revoked'}
  await expect(call('notes__write_note',{},'call-2')).rejects.toBeInstanceOf(GateViolation)
  expect(ran).toEqual([])
  expect(port.reads.map(r=>r.call.count)).toEqual([false,false])
 })
 it('counts only at the dispatch, the last read of a call, whatever checks came before it',async()=>{
  const port=admit(),ran:string[]=[]
  const {gate,call}=await delegatedGate(port,ran)
  expect((await gate.decide('notes__write_note',{},'call-2')).answer).toBe('allow')
  await call('notes__write_note',{},'call-2')
  expect(ran).toEqual(['write_note'])
  expect(port.reads.map(r=>r.call.count)).toEqual([false,false,true])
 })
 it('refuses at the dispatch when its counted read does not admit it, and gives its slot back',async()=>{
  const port=admit(),ran:string[]=[]
  const {call,notices}=await delegatedGate(port,ran)
  port.answerFor=read=>read.call.count?{admission:'refuse',refusal:'out-of-scope',reason:'limit'}:{admission:'admit'}
  await expect(call('notes__read_note')).rejects.toBeInstanceOf(GateViolation)
  expect(ran).toEqual([])
  expect(notices.at(-1)).toMatchObject({refusal:{name:'out-of-scope',reason:'limit'}})
 })
 it("carries no authority past the delegation on an approval's continuation",async()=>{
  const port=admit(),ran:string[]=[]
  let current:ApprovalRecord|undefined
  const book:ApprovalBook={current:()=>current,ask:record=>{current=record},closed:()=>''}
  const {gate,byName}=await delegatedGate(port,ran,book,{write_note:{approve:()=>'ask'}})
  expect((await gate.decide('notes__write_note',{text:'x'},'call-3')).answer).toBe('ask')
  port.answer={admission:'refuse',refusal:'revoked'}
  const run=async(args:Record<string,unknown>,signal:AbortSignal)=>{await dispatchAdmission(signal)?.();return byName.get('notes__write_note')!.execute(args,signal)}
  const outcome=await gate.approved({...current!,state:'approved'},new AbortController().signal,run)
  expect(outcome.ran).toBe(false)
  expect(ran).toEqual([])
  expect(port.reads.map(r=>r.call.count)).toEqual([false,false])
 })
 it('does not ask the port about a call already answered checked',async()=>{
  const port=admit(),ran:string[]=[]
  const {gate}=await delegatedGate(port,ran)
  ;(gate as unknown as {changed:Set<string>}).changed.add('notes')
  expect((await gate.decide('notes__write_note',{},'call-4')).answer).toBe('checked')
  expect(port.reads).toEqual([])
 })
 it("refuses an approval's continuation at its dispatch: it did not run",async()=>{
  const port=admit(),ran:string[]=[]
  let current:ApprovalRecord|undefined
  const book:ApprovalBook={current:()=>current,ask:record=>{current=record},closed:()=>''}
  const {gate,byName}=await delegatedGate(port,ran,book,{write_note:{approve:()=>'ask'}})
  expect((await gate.decide('notes__write_note',{text:'x'},'call-3')).answer).toBe('ask')
  port.answerFor=read=>read.call.count?{admission:'refuse',refusal:'revoked'}:{admission:'admit'}
  const run=async(args:Record<string,unknown>,signal:AbortSignal)=>{await dispatchAdmission(signal)?.();return byName.get('notes__write_note')!.execute(args,signal)}
  const outcome=await gate.approved({...current!,state:'approved'},new AbortController().signal,run)
  expect(outcome.ran).toBe(false)
  expect(outcome.result.isError).toBe(true)
  expect(ran).toEqual([])
  expect(port.reads.map(r=>r.call.count)).toEqual([false,false,true])
 })
 it('lets an effect already dispatched finish when a revocation lands; the next call is refused (uncertain-action rules unchanged)',async()=>{
  const port=admit(),ran:string[]=[]
  let release!:()=>void
  const held=new Promise<void>(resolve=>{release=resolve})
  const {call}=await delegatedGate(port,ran,undefined,{read_note:{execute:async()=>{ran.push('read_note');await held;return ok()}}})
  const first=call('notes__read_note')
  await new Promise(resolve=>setTimeout(resolve,0))
  port.answer={admission:'refuse',refusal:'revoked'}
  release()
  expect(await first).toEqual(ok())
  await expect(call('notes__read_note')).rejects.toBeInstanceOf(GateViolation)
  expect(ran).toEqual(['read_note'])
 })
 it("a delegated child's gate admits under its parent's principal",async()=>{
  const port=admit(),ran:string[]=[]
  const parent=await delegatedGate(port,ran)
  const declared=await declareSources(await tools(ran),[])
  const child=new SourceGate(declared.entries,declared.sources,CONTEXT_DEFAULTS,'marker',(tool,detail)=>new GateViolation(tool,detail),()=>[],'json-schema',parent.gate)
  expect(child.principal).toBe(parent.gate.principal)
  const byName=new Map(declared.hostTools.map(t=>[t.name,t]))
  await expect(child.call(byName.get('notes__delete_note')!,{},new AbortController().signal,(a,s)=>byName.get('notes__delete_note')!.execute(a,s))).rejects.toBeInstanceOf(GateViolation)
  expect(ran).toEqual([])
 })
 it('an undelegated gate asks no admission and runs the call as before',async()=>{
  const ran:string[]=[]
  const declared=await declareSources(await tools(ran),[])
  const rows:SourceFigures[]=[]
  const gate=new SourceGate(declared.entries,declared.sources,CONTEXT_DEFAULTS,'marker',(tool,detail)=>new GateViolation(tool,detail),()=>rows)
  expect(gate.principal).toBeUndefined()
  const byName=new Map(declared.hostTools.map(t=>[t.name,t]))
  await gate.call(byName.get('notes__delete_note')!,{},new AbortController().signal,(a,s)=>byName.get('notes__delete_note')!.execute(a,s))
  expect(ran).toEqual(['delete_note'])
 })
})
