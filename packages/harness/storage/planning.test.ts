import {describe,it,expect,vi} from 'vitest'
import {MemoryChatStore,type ChatStore} from './chat'
import {ChatRevisionConflict} from './common'
import {planTools,acceptPlan} from '../planning/tools'
import {resolvePlanReference,LegacyPlanReferenceUnresolved,PlanReferenceChanged,type PlanResolver} from '../planning/references'
import {legacyChatStoreAdapter} from './legacyChat'
import type {Chat} from '../host'
import {canonicalJSON} from '../context/chat'
const signal=()=>new AbortController().signal
const pin='a'.repeat(64)
const resolver:PlanResolver={resolve:async ref=>({...ref,digest:pin}),legacy:async()=>({kind:'artifact',id:'migrated-artifact',digest:pin})}
const args={objective:'Review object',steps:['Inspect content'],questions:[],references:[{kind:'artifact',id:'opaque-other'}]}
describe('async pathless planning storage',()=>{
 it('proposes, reloads and accepts canonical plan records with only opaque ids',async()=>{
  const store=new MemoryChatStore([{chat:{id:'opaque-chat',interaction:'plan',pack:{id:'opaque-artifact'},hostField:{unchanged:true}}}])
  await planTools(store,'opaque-chat',resolver)[0]!.execute(args,signal())
  const loaded=(await store.get('opaque-chat'))!,chat=loaded.chat as Chat,plan=chat.plans![0]!
  expect(plan.references).toEqual([{kind:'artifact',id:'opaque-artifact',digest:pin},{kind:'artifact',id:'opaque-other',digest:pin}])
  expect(canonicalJSON(JSON.parse(canonicalJSON(plan)))).toBe(canonicalJSON(plan))
  await acceptPlan(store,chat,structuredClone(plan),signal(),resolver)
  const accepted=(await store.get('opaque-chat'))!
  expect(accepted.revision).not.toBe(loaded.revision);expect(accepted.chat.interaction).toBe('work');expect(accepted.chat.hostField).toEqual({unchanged:true})
 })
 it('migrates explicitly resolved legacy references on the next revision',async()=>{
  const old={id:'legacy-plan',revision:1,objective:'Old',steps:['Read'],questions:[],references:[{kind:'file',id:'legacy-locator',revision:pin}],status:'proposed',createdAt:'2026-10-14T12:00:00Z'}
  const store=new MemoryChatStore([{chat:{id:'legacy-chat',interaction:'plan',pack:{path:'legacy-locator'},plans:[old]}}])
  await planTools(store,'legacy-chat',resolver)[0]!.execute({...args,references:[]},signal())
  const plans=(await store.get('legacy-chat'))!.chat.plans as Chat['plans']
  expect(plans![0]!.references[0]).toEqual(old.references[0]);expect(plans![1]!.references).toEqual([{kind:'artifact',id:'migrated-artifact',digest:pin}])
  await expect(resolvePlanReference(old.references[0] as any,{resolve:resolver.resolve},signal())).rejects.toBeInstanceOf(LegacyPlanReferenceUnresolved)
  await expect(resolvePlanReference({kind:'artifact',id:'opaque',digest:'b'.repeat(64)},resolver,signal())).rejects.toBeInstanceOf(PlanReferenceChanged)
 })
 it('loses a concurrent proposal CAS without overwriting the winner',async()=>{
  const store=new MemoryChatStore([{chat:{id:'race',interaction:'plan',hostField:'kept'}}]);let resolve!:()=>void,arrived=0
  const barrier=new Promise<void>(done=>resolve=done)
  const paused:PlanResolver={resolve:async ref=>{if(++arrived===2)resolve();await barrier;return {...ref,digest:pin}}}
  const tool=planTools(store,'race',paused)[0]!
  const outcomes=await Promise.allSettled([tool.execute(args,signal()),tool.execute(args,signal())])
  expect(outcomes.filter(o=>o.status==='fulfilled')).toHaveLength(1)
  expect((outcomes.find(o=>o.status==='rejected') as PromiseRejectedResult).reason).toBeInstanceOf(ChatRevisionConflict)
  expect((await store.get('race'))!.chat.plans).toHaveLength(1);expect((await store.get('race'))!.chat.hostField).toBe('kept')
 })
 it('surfaces a stale acceptance CAS and preserves the concurrent plan revision',async()=>{
  const memory=new MemoryChatStore([{chat:{id:'accept-race',interaction:'plan'}}]);await planTools(memory,'accept-race',resolver)[0]!.execute(args,signal())
  const current=(await memory.get('accept-race'))!,chat=current.chat as Chat
  const store:ChatStore={get:memory.get.bind(memory),list:memory.list.bind(memory),update:async(id,patch,revision,s)=>{const now=(await memory.get(id))!;await memory.update(id,{interaction:'plan'},now.revision);return memory.update(id,patch,revision,s)}}
  await expect(acceptPlan(store,chat,chat.plans![0]!,signal(),resolver)).rejects.toBeInstanceOf(ChatRevisionConflict)
  expect((await memory.get('accept-race'))!.chat.interaction).toBe('plan')
 })
 it('names the single-writer legacy adapter limits and awaits acknowledgement',async()=>{
  let chat={id:'legacy',interaction:'plan' as const,hostField:'retained'} as Chat
  const checkpoint=vi.fn(async()=>{}),legacy={getSnapshot:()=>({chats:[chat],drafts:[]}),update:(_id:string,patch:Partial<Chat>)=>{chat={...chat,...patch}},checkpoint,running:''}
  const adapter=legacyChatStoreAdapter(legacy),head=(await adapter.get('legacy'))!
  expect(adapter.consistency).toBe('single-writer-legacy')
  await adapter.update('legacy',{interaction:'work'},head.revision);expect(checkpoint).toHaveBeenCalledOnce();expect((await adapter.get('legacy'))!.chat.hostField).toBe('retained')
 })
})
