import {describe,it,expect} from 'vitest'
import {readFileSync} from 'node:fs'
import {StorageConformanceFailure} from './conformance'
import {GoalRefused,type GoalLifetime} from './goal'
import {checkpointBytesDigest} from './wait'
import {runStoreGoalConformance,type GoalRunStore,type GoalStoreFactory} from './goalConformance'
import type {JobLifetime} from './job'
import type {LeaseLifetime} from './lease'
import {MemoryRunStore} from './run'
import type {WaitLifetime} from './wait'

/* #230: the goal conformance kit over the memory kind, a real restart over a fresh store instance, and for every property a
 * store that leaves that one guarantee out. The Go twin is go/context/run_goal_conformance_test.go. */
class DurableGoalStore extends MemoryRunStore {override readonly leaseLifetime:LeaseLifetime='durable';override readonly waitLifetime:WaitLifetime='durable';override readonly jobLifetime:JobLifetime='durable';override readonly goalLifetime:GoalLifetime='durable'}
type Held=Record<string,any>
function restarted(old:MemoryRunStore,fresh:MemoryRunStore,drop:string[]=[]):MemoryRunStore{
 const from=old as unknown as Held,to=fresh as unknown as Held
 to.runs=structuredClone(from.runs)
 for(const table of ['leases','waits','journal','jobs','goals'])for(const [name,value] of Object.entries(from[table] as Held))if(typeof value!=='function'&&!drop.includes(`${table}.${name}`))(to[table] as Held)[name]=structuredClone(value)
 return fresh
}
type Fault=(store:Held,tables:{goals:Held;waits:Held;leases:Held;jobs:Held})=>void
const kind=(fault?:Fault,after?:{drop?:string[];then?:(fresh:Held)=>void;sameInstance?:boolean}):GoalStoreFactory=>(now,from)=>{
 if(from&&after?.sameInstance)return from
 const fresh=new DurableGoalStore({now}) as unknown as Held
 if(from){restarted(from as unknown as MemoryRunStore,fresh as unknown as MemoryRunStore,after?.drop);after?.then?.(fresh)}
 fault?.(fresh,{goals:fresh.goals,waits:fresh.waits,leases:fresh.leases,jobs:fresh.jobs})
 return fresh as unknown as GoalRunStore
}
const failsOn=async(create:GoalStoreFactory)=>{try{await runStoreGoalConformance(create);return undefined}catch(error){if(error instanceof StorageConformanceFailure)return error.property;throw error}}
const wrap=(store:Held,name:string,by:(original:(...args:any[])=>any)=>(...args:any[])=>any)=>{const original=store[name].bind(store);store[name]=by(original)}
/** The run's record as the table holds it, to break one rule in place. */
const rowOf=(goals:Held,resource:string):Held|undefined=>goals.rows.get(resource)
const currentOf=(goals:Held,resource:string):Held|undefined=>rowOf(goals,resource)?.objectives.at(-1)
const STAMP={command:'fault',actor:'fault',at:'2026-01-01T00:00:00.000Z'}
/** Before the command reaches the table, `before` changes the record or the input; a fault of one kind of command only. */
const ahead=(kinds:string[],before:(input:Held,goals:Held,tables:Held)=>Held|void):Fault=>(store,tables)=>wrap(store,'commandGoal',original=>(input,signal)=>{if(!kinds.includes(input.command.kind))return original(input,signal);const changed=before(input,tables.goals,tables)??input;return original(changed,signal)})
/** The answer as the store hands it back, changed by `after`. */
const answered=(kinds:string[],after:(answer:Held)=>void):Fault=>store=>wrap(store,'commandGoal',original=>async(input,signal)=>{const answer=await original(input,signal);if(kinds.includes(input.command.kind))after(answer);return answer})
/** A store that does not refuse with `reason`: it answers the record as it is. */
const swallows=(reason:string):Fault=>(store,{goals})=>wrap(store,'commandGoal',original=>async(input,signal)=>{try{return await original(input,signal)}catch(error){if(error instanceof GoalRefused&&error.reason===reason)return {goal:structuredClone(rowOf(goals,input.command.resource)),entry:{command:input.command.command,outcome:'accepted'}};throw error}})
/** What a goal transaction of these kinds reads of the run, changed by `edit`: a store that reads less than it holds. */
const reading=(kinds:string[],edit:(context:Held,goals:Held,input:Held)=>Held):Fault=>(_,{goals})=>{const original=goals.command.bind(goals);goals.command=(input:Held,context:(resource:string)=>Held)=>original(input,kinds.includes(input.command?.kind)?(resource:string)=>edit(context(resource),goals,input):context)}
/** A store that reads a woken wait as resolved. */
const withoutWoken=(context:Held)=>({...context,waits:context.waits.map((wait:Held)=>wait.state==='woken'?{...wait,state:'resumed'}:wait)})
/** For each property of the kit, a store that breaks exactly it. */
const FAULTS:[string,Fault][]=[
 // #318: M-4, a create over an abandon whose deliveries were never journaled; L-8, a create over an achieved objective.
 ['goal.create.unfinished',ahead(['create'],(input,goals)=>{const row=rowOf(goals,input.command.resource),current=currentOf(goals,input.command.resource);if(row&&current?.state==='abandoned'&&!row.journal.some((entry:Held)=>entry.command===current.abandoned.command&&entry.outcome==='delivered'))row.journal.push({command:current.abandoned.command,kind:'abandon',actor:'fault',at:STAMP.at,objective:current.id,outcome:'delivered',revision:row.revision,digest:'sha256:'+'f'.repeat(64),deliveries:[]})})],
 ['goal.create.achieved',reading(['create'],(context,goals,input)=>currentOf(goals,input.command.resource)?.state==='achieved'?{...context,waits:[]}:context)],
 // #313's review: H-1, H-2 and the seven stores of M-2, with M-1.
 ['goal.abandon.unleased',store=>wrap(store,'abandonJobs',original=>async(input,signal)=>await store.load(input.resource)===undefined&&await store.readLease(input.resource)===undefined?{jobs:[]}:original(input,signal))],
 ['goal.achieve.read',ahead(['achieve'],input=>({...input,read:input.command.expectedRevision}))],
 ['goal.repeat.current',store=>{const seen=new Map<string,unknown>();wrap(store,'commandGoal',original=>async(input,signal)=>{const key=JSON.stringify(input.command);if(seen.has(key))return structuredClone(seen.get(key));const answer=await original(input,signal);seen.set(key,structuredClone(answer));return answer})}],
 ['goal.unfenced.history',(store,{goals})=>wrap(store,'commitHistory',original=>async(input,signal)=>{const admit=goals.admitUnfenced;goals.admitUnfenced=()=>undefined;try{return await original(input,signal)}finally{goals.admitUnfenced=admit}})],
 ['goal.unfenced.terminal',(_,{goals})=>{const admit=goals.admitUnfenced.bind(goals);goals.admitUnfenced=(resource:string)=>{const state=currentOf(goals,resource)?.state;if(state==='active'||state==='blocked')admit(resource)}}],
 ['goal.achieve.unproven',reading(['achieve'],context=>({...context,jobs:context.jobs.filter((job:Held)=>job.state!=='unproven')}))],
 ['goal.achieve.woken',reading(['achieve'],withoutWoken)],
 ['goal.abandon.woken',reading(['abandon'],withoutWoken)],
 ['goal.create.woken',reading(['create'],withoutWoken)],
 ['goal.create.later',reading(['create'],(context,goals,input)=>{const left=new Set((currentOf(goals,input.command.resource)?.abandoned?.waits??[]).map((wait:Held)=>wait.wait));return {...context,waits:context.waits.filter((wait:Held)=>left.has(wait.wait))}})],
 ['goal.terminal.pause',(_,{goals})=>{const original=goals.command.bind(goals);goals.command=(input:Held,context:Held)=>{const paused=currentOf(goals,input.command?.resource)?.paused,answer=original(input,context),current=currentOf(goals,input.command.resource);if(paused&&current&&(current.state==='abandoned'||current.state==='achieved'))current.paused=paused;return answer}}],
 ['goal.declared',store=>{Object.defineProperty(store,'goalVersion',{value:2})}],
 ['goal.create.commits',store=>{store.readGoal=async()=>undefined}],
 ['goal.create.cas',ahead(['create'],(input,goals)=>({...input,command:{...input.command,expectedRevision:rowOf(goals,input.command.resource)?.revision??null}}))],
 ['goal.create.exists',ahead(['create'],(input,goals)=>{const current=currentOf(goals,input.command.resource);if(current&&rowOf(goals,input.command.resource)!.revision===input.command.expectedRevision&&(current.state==='active'||current.state==='blocked')){current.state='abandoned';delete current.paused;delete current.blocked;current.abandoned={...STAMP,waits:[],jobs:[]}}})],
 ['goal.repeat',(store,{goals})=>wrap(store,'commandGoal',original=>(input,signal)=>rowOf(goals,input.command.resource)?.journal.some((e:Held)=>e.command===input.command.command)?original({...input,command:{...input.command,command:input.command.command+'-again'}},signal):original(input,signal))],
 ['goal.conflict',(store,{goals})=>wrap(store,'commandGoal',original=>async(input,signal)=>{try{return await original(input,signal)}catch(error){if(error instanceof GoalRefused&&error.reason==='conflict'){const row=rowOf(goals,input.command.resource)!;return {goal:structuredClone(row),entry:structuredClone(row.journal.find((e:Held)=>e.command===input.command.command))}}throw error}})],
 ['goal.refusal.repeat',(store,{goals})=>wrap(store,'commandGoal',original=>(input,signal)=>{const row=rowOf(goals,input.command.resource);if(row)row.journal=row.journal.filter((e:Held)=>e.command!==input.command.command||e.outcome==='accepted');return original(input,signal)})],
 ['goal.unfenced',(_,{goals})=>{goals.admitUnfenced=()=>undefined}],
 ['goal.pause.commits',answered(['pause'],answer=>{delete answer.lease})],
 ['goal.pause.twice',ahead(['pause'],(input,goals)=>{delete currentOf(goals,input.command.resource)?.paused})],
 ['goal.pause.held',(store,{goals})=>wrap(store,'renewLease',original=>(fence,duration,signal)=>{if(currentOf(goals,fence.resource)?.paused)throw new GoalRefused('objective-paused');return original(fence,duration,signal)})],
 ['goal.pause.acquire',(_,{goals})=>{goals.admitLease=()=>undefined}],
 ['goal.resume.commits',(store,{leases})=>wrap(store,'commandGoal',original=>async(input,signal)=>{const answer=await original(input,signal);if(input.command.kind==='resume'){const row=leases.rows.get(input.command.resource);if(row)row.state='held'};return answer})],
 ['goal.resume.none',ahead(['resume'],(input,goals)=>{const current=currentOf(goals,input.command.resource);if(current&&!current.paused)current.paused={...STAMP}})],
 ['goal.block',answered(['block'],answer=>{delete answer.goal.objectives.at(-1).blocked.condition})],
 ['goal.block.twice',ahead(['block'],(input,goals)=>{const current=currentOf(goals,input.command.resource);if(current?.state==='blocked'){current.state='active';delete current.blocked}})],
 ['goal.block.achieve',ahead(['achieve'],(input,goals)=>{const current=currentOf(goals,input.command.resource);if(current?.state==='blocked'){current.state='active';delete current.blocked}})],
 ['goal.unblock',answered(['unblock'],answer=>{answer.goal.objectives.at(-1).state='blocked'})],
 ['goal.unblock.none',ahead(['unblock'],(input,goals)=>{const current=currentOf(goals,input.command.resource);if(current?.state==='active'){current.state='blocked';current.blocked={...STAMP,condition:'fault'}}})],
 ['goal.achieve.unbound',ahead(['achieve'],(input,goals)=>{const current=currentOf(goals,input.command.resource);if(current?.state==='active'&&!current.plan)current.plan={id:'fault-plan',revision:'sha256:'+'d'.repeat(64),steps:[{step:'step-1',index:0}],bound:{...STAMP}}})],
 ['goal.bind.head',(store)=>wrap(store,'commandGoal',original=>async(input,signal)=>{if(input.command.kind!=='bind')return original(input,signal);const head=await store.load(input.command.resource);return original({...input,command:{...input.command,head:{revision:head.revision,digest:checkpointBytesDigest(head.checkpoint)}}},signal)})],
 ['goal.bind.finding',ahead(['bind'],input=>{if(!input.refused)return;const {refused:_,...rest}=input;return {...rest,plan:{id:'fault-plan',revision:'sha256:'+'d'.repeat(64),steps:[{step:'step-1',index:0}]}}})],
 ['goal.bind.commits',answered(['bind'],answer=>{delete answer.goal.objectives.at(-1).plan.bound})],
 ['goal.bind.twice',ahead(['bind'],(input,goals)=>{delete currentOf(goals,input.command.resource)?.plan})],
 ['goal.achieve.head',(store)=>wrap(store,'commandGoal',original=>async(input,signal)=>{if(input.command.kind!=='achieve')return original(input,signal);const head=await store.load(input.command.resource);return original({...input,command:{...input.command,head:{revision:head.revision,digest:checkpointBytesDigest(head.checkpoint)}}},signal)})],
 ['goal.achieve.finding',ahead(['achieve'],input=>{const {refused:_,...rest}=input;return rest})],
 ['goal.achieve.commits',answered(['achieve'],answer=>{delete answer.goal.objectives.at(-1).achieved?.head})],
 ['goal.terminal',ahead(['pause'],(input,goals)=>{const current=currentOf(goals,input.command.resource);if(current?.state==='achieved'){current.state='active';delete current.achieved}})],
 ['goal.create.reused',ahead(['create'],(input,goals)=>{for(const objective of rowOf(goals,input.command.resource)?.objectives??[])if(objective.id===input.command.objective)objective.id=objective.id+'-renamed'})],
 ['goal.create.terminal',ahead(['create'],(input,goals)=>{if(currentOf(goals,input.command.resource)?.state==='achieved'&&!rowOf(goals,input.command.resource)!.objectives.some((o:Held)=>o.id===input.command.objective))throw new GoalRefused('objective-exists')})],
 ['goal.bind.reused',ahead(['bind'],(input,goals)=>{for(const objective of rowOf(goals,input.command.resource)?.objectives.slice(0,-1)??[])if(objective.plan)objective.plan.id=objective.plan.id+'-renamed'})],
 ['goal.achieve.jobs',ahead(['achieve'],(input,_,{jobs})=>{const open=(jobs.rows.get(input.command.resource)??[]).filter((job:Held)=>job.state==='unproven'||job.state==='started').map((job:Held)=>job.job);return open.length?{...input,command:{...input.command,waive:open}}:input})],
 ['goal.achieve.waiver',ahead(['achieve'],(input,_,{jobs})=>{if(!input.command.waive)return;const open=new Set((jobs.rows.get(input.command.resource)??[]).filter((job:Held)=>job.state==='unproven'||job.state==='started').map((job:Held)=>job.job));const waive=input.command.waive.filter((job:string)=>open.has(job));const {waive:_waive,...command}=input.command;return {...input,command:waive.length?{...command,waive}:command}})],
 ['goal.achieve.wait',(store,{waits})=>wrap(store,'commandGoal',original=>async(input,signal)=>{if(input.command.kind!=='achieve')return original(input,signal);const rows=(waits.rows.get(input.command.resource)??[]) as Held[],held=rows.map(row=>row.state);for(const row of rows)row.state='resumed';try{return await original(input,signal)}finally{rows.forEach((row,i)=>{row.state=held[i]})}})],
 ['goal.abandon.left',answered(['abandon'],answer=>{answer.goal.objectives.at(-1).abandoned.waits=[]})],
 ['goal.create.unresolved',ahead(['create'],(input,goals)=>{const current=currentOf(goals,input.command.resource);if(current?.abandoned&&rowOf(goals,input.command.resource)!.journal.some((entry:Held)=>entry.command===current.abandoned.command&&entry.outcome==='delivered'))current.abandoned.waits=[]})],
 ['goal.deliveries',(store,{goals})=>{store.recordGoalDeliveries=async(input:Held)=>structuredClone(rowOf(goals,input.resource))}],
 ['goal.deliveries.other',(store,{goals})=>wrap(store,'recordGoalDeliveries',original=>(input,signal)=>original({...input,command:currentOf(goals,input.resource)?.abandoned?.command??input.command},signal))],
 ['goal.pause.resume-wait',(store,{goals})=>wrap(store,'resumeWait',original=>async(claim,signal)=>{const held=goals.admitLease;goals.admitLease=()=>undefined;try{return await original(claim,signal)}finally{goals.admitLease=held}})],
 ['goal.none',(store,{goals})=>wrap(store,'commandGoal',original=>async(input,signal)=>{try{return await original(input,signal)}catch(error){if(!rowOf(goals,input.command.resource))goals.rows.set(input.command.resource,{version:1,kind:'goal',resource:input.command.resource,revision:1,objectives:[],journal:[]});throw error}})],
 ['goal.inspect.none',(store)=>wrap(store,'inspectGoal',original=>async(input,signal)=>(await original(input,signal))??{})],
 ['goal.inspect.count',(store,{goals})=>wrap(store,'inspectGoal',original=>async(input,signal)=>{const row=rowOf(goals,input.resource);if(row)row.revision++;return original(input,signal)})],
 ['goal.journal.bound',(store,{goals})=>wrap(store,'commandGoal',original=>(input,signal)=>{const row=rowOf(goals,input.command.resource);if(row&&row.journal.length>=64){row.journal.shift();row.discarded=(row.discarded??0)+1}return original(input,signal)})],
 ['goal.exhausted',ahead(['create'],(input,goals)=>{const row=rowOf(goals,input.command.resource);if(row&&row.objectives.length>=8)row.objectives.shift()})],
 ['goal.owner',(store,{goals})=>wrap(store,'readGoal',original=>async(resource,signal)=>(await original(resource,signal))??(resource==='run-other'?structuredClone([...goals.rows.values()].at(-1)):undefined))],
]
/** What a restart loses, each naming the property the kit must fail it on. */
const RESTARTS:[string,{drop?:string[];then?:(fresh:Held)=>void;sameInstance?:boolean}][]=[
 ['goal.restart.instance',{sameInstance:true}],
 ['goal.restart.record',{drop:['goals.rows']}],
 ['goal.restart.paused',{then:fresh=>{fresh.goals.admitLease=()=>undefined}}],
 ['goal.restart.unfenced',{then:fresh=>{fresh.goals.admitUnfenced=()=>undefined}}],
 ['goal.restart.repeat',{then:fresh=>wrap(fresh,'commandGoal',original=>(input,signal)=>original({...input,command:{...input.command,command:input.command.command+'-forgotten'}},signal))}],
 ['goal.restart.conflict',{then:fresh=>wrap(fresh,'commandGoal',original=>async(input,signal)=>{try{return await original(input,signal)}catch(error){if(error instanceof GoalRefused&&error.reason==='conflict')return {goal:structuredClone(fresh.goals.rows.get(input.command.resource))};throw error}})}],
 ['goal.restart.commits',{then:fresh=>wrap(fresh,'commandGoal',original=>(input,signal)=>{if(input.command.kind==='resume')throw new GoalRefused('not-paused');return original(input,signal)})}],
]
/** What a store does wrong beyond a single property, each naming the property the kit must fail it on. */
const STORES:[string,string,Fault][]=[
 ['A store that does not journal refusals','goal.create.cas',(store,{goals})=>wrap(store,'commandGoal',original=>async(input,signal)=>{const row=rowOf(goals,input.command.resource),length=row?.journal.length;try{return await original(input,signal)}catch(error){if(row&&length!==undefined)row.journal.length=length;throw error}})],
 ['A store that stamps a refusal with the caller\'s revision','goal.create.exists',(store,{goals})=>wrap(store,'commandGoal',original=>async(input,signal)=>{try{return await original(input,signal)}catch(error){const last=rowOf(goals,input.command.resource)?.journal.at(-1);if(last&&last.command===input.command.command)last.revision=(input.command.expectedRevision??0)+1;throw error}})],
 ['A store that drops a failed job from an achievement','goal.achieve.jobs',answered(['achieve'],answer=>{const done=answer.goal.objectives.at(-1).achieved;if(done?.unsuccessful)done.unsuccessful=done.unsuccessful.filter((job:Held)=>job.state!=='completed')})],
 ['A store that counts a contradiction as success','goal.achieve.jobs',answered(['achieve'],answer=>{const done=answer.goal.objectives.at(-1).achieved;if(done?.unsuccessful)done.unsuccessful=done.unsuccessful.filter((job:Held)=>!job.contradicted)})],
 ['A store that counts a repeated inspection','goal.inspect.count',(store,{goals})=>wrap(store,'inspectGoal',original=>(input,signal)=>{const row=rowOf(goals,input.resource);if(row?.inspections)row.inspections.last.command='forgotten';return original(input,signal)})],
 ['A store that refuses for size','goal.journal.bound',(store,{goals})=>wrap(store,'commandGoal',original=>(input,signal)=>{if((rowOf(goals,input.command.resource)?.journal.length??0)>=64)throw new GoalRefused('invalid');return original(input,signal)})],
 ['A store whose pause also stops a held lease from committing','goal.pause.held',(store,{goals})=>wrap(store,'commitFenced',original=>(input,fence,signal)=>{if(currentOf(goals,input.runId)?.paused)throw new GoalRefused('objective-paused');return original(input,fence,signal)})],
 ['A store that answers a refusal of the head','goal.bind.head',swallows('head-changed')],
]
const fails=(property:string)=>expect.objectContaining({name:'StorageConformanceFailure',property})
describe('the goal conformance kit (#230)',()=>{
 it('passes the memory kind held as a durable one, across a real restart',async()=>{expect(await failsOn(kind())).toBeUndefined()})
 for(const [property,fault] of FAULTS)it(`fails a store that breaks ${property}`,async()=>{await expect(runStoreGoalConformance(kind(fault))).rejects.toEqual(fails(property))})
 for(const [property,after] of RESTARTS)it(`fails a store whose restart breaks ${property}`,async()=>{await expect(runStoreGoalConformance(kind(undefined,after))).rejects.toEqual(fails(property))})
 for(const [name,property,fault] of STORES)it(`${name}: ${property}`,async()=>{await expect(runStoreGoalConformance(kind(fault))).rejects.toEqual(fails(property))})
 it('names every property of the kit with a store that breaks it',()=>{
  const source=readFileSync('storage/goalConformance.ts','utf8')
  const named=new Set([...source.matchAll(/'(goal\.[a-z.-]+)'/g)].map(m=>m[1]!).filter(name=>name!=='goal.operation'))
  expect([...named].filter(name=>![...FAULTS.map(f=>f[0]),...RESTARTS.map(r=>r[0])].includes(name))).toEqual([])
 })
})
