import {it,expect,vi} from 'vitest'
import {MemoryRunStore} from '../storage/run'
import {managedContext} from '../context/managed'
import type {AssistantSession} from '../engine'
import {HistoryInputInvalid} from './codec'
import {readRetainedRunHistory} from './view'
import {queryHistoryEvents,findHistoryRelations} from './query'
it('filters and pages actual retained events in sequence order and never invents job relations from text',async()=>{
 let tick=0;const store=new MemoryRunStore({now:()=>new Date(Date.UTC(2026,9,25,0,0,30-tick++))}),session:AssistantSession={prompt:'Work',testPrompt:'',tools:[],hostTools:[],callTool:vi.fn(async()=>({content:[]})),model:{family:'openai-compatible',model:'fixture',call:vi.fn()},signal:new AbortController().signal,context:{currentRequest:{message:'Work',input:'Work'},runStore:{store,id:'r'}}}
 for await(const _ of managedContext(session,async function*(){yield {type:'message',text:'job fake-123 is only prose'};yield {type:'message',text:'second'};yield {type:'end'}})){}
 const view=await readRetainedRunHistory(store,'r'),first=queryHistoryEvents(view,{types:['message'],limit:1});expect(first.items[0]!.event).toMatchObject({text:'job fake-123 is only prose'});expect(first.hasMore).toBe(true)
 const next=queryHistoryEvents(view,{types:['message'],afterSequence:first.nextSequence,limit:1});expect(next.items[0]!.event).toMatchObject({text:'second'});expect(next.items[0]!.sequence).toBeGreaterThan(first.nextSequence);expect(next.items[0]!.recordedAt<first.items[0]!.recordedAt).toBe(true);expect(next.hasMore).toBe(false)
 expect(queryHistoryEvents(view,{text:'fake-123'}).items).toHaveLength(1);expect(queryHistoryEvents(view,{types:['message'],from:first.items[0]!.recordedAt}).items).toHaveLength(1)
 expect(()=>queryHistoryEvents(view,{text:'😀'.repeat(1001)})).toThrow(HistoryInputInvalid);expect(()=>queryHistoryEvents(view,{text:'"\\uD800"'})).not.toThrow()
 expect(findHistoryRelations(view,'job','fake-123')).toEqual({kind:'unavailable',rows:[]})
 first.items[0]!.scope.role='changed';expect(queryHistoryEvents(view,{types:['message']}).items[0]!.scope.role).toBe('author')
 for(const bad of [{limit:101},{afterSequence:view.head.eventCount+1},{types:['message','message']},{from:'yesterday'},{step:{execution:'r',revision:'not-a-digest',id:'step'}},{unknown:true}])expect(()=>queryHistoryEvents(view,bad)).toThrow(HistoryInputInvalid)
})
