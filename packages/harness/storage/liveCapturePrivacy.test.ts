import {it,expect,vi} from 'vitest'
import {MemoryRunStore} from './run'
import {MemoryChatStore} from './chat'
import {planTools,acceptPlan,currentChat} from '../planning/tools'
import {vercel} from '../engines/vercel'
import {answerWith} from '../testing/scenarioDriver'
import type {AssistantSession,AssistantEvent} from '../engine'
import {readExecution} from '../execution/state'
import {readRetainedRunHistory} from '../history/view'
import {readCapturedCorpusView,readArchiveCapturedCorpusView} from '../history/capture'
import {exportRunArchive,readRunArchive} from '../history/archive'
import {replayCorpus,type CorpusManifest} from '../corpus'
const consume=async(stream:AsyncIterable<AssistantEvent>)=>{const events:AssistantEvent[]=[];for await(const event of stream)events.push(event);return events}
it('redacts original accepted Plan capture and archive without granting its display native resume authority',async()=>{
 const chatStore=new MemoryChatStore([{chat:{id:'live-private-plan',interaction:'plan',plans:[]}}]),signal=new AbortController().signal,resolver={resolve:async()=>{throw Error('No references in fixture')}}
 await planTools(chatStore,'live-private-plan',resolver)[0]!.execute({objective:'Read source.',steps:['Read SECRET-PLAN prose'],questions:[],references:[]},signal)
 const chat=(await currentChat(chatStore,'live-private-plan'))!;await acceptPlan(chatStore,chat,chat.plans!.at(-1)!,signal,resolver);const accepted=(await currentChat(chatStore,'live-private-plan'))!.plans!.at(-1)!
 const store=new MemoryRunStore(),sink={write:vi.fn()},manifest:CorpusManifest={id:'live-private-plan',packageVersion:'0.11.0',policy:{},files:{requests:'requests.json',toolResults:'tool-results.json',events:'events.json',checkpoint:'checkpoint.json'},expected:{file:'expected.json'},tolerance:[]},base:AssistantSession={prompt:'Read.',testPrompt:'',allowConversation:true,critic:'off',tools:[],hostTools:[],callTool:vi.fn(),signal,model:{family:'openai-compatible',model:'fixture',call:async()=>answerWith({say:'Done.'},'stream',0)}}
 const events=await consume(vercel.start({...base,record:true,corpus:{manifest,guard:{value:<T>(v:T)=>v},sink},policy:{version:2,records:{redactOnStore:false},outputPatterns:[{name:'plan-prose',pattern:'SECRET-PLAN'}]},context:{runStore:{store,id:'live-private-plan'},phase:'work',plan:accepted}}))
 expect(events.filter(e=>e.type==='error')).toEqual([]);expect(sink.write).toHaveBeenCalledTimes(1)
 const head=(await store.load('live-private-plan'))!,raw=readExecution(JSON.parse(new TextDecoder().decode(head.checkpoint!)))!,view=readCapturedCorpusView(await readRetainedRunHistory(store,'live-private-plan'))
 expect(raw.plan[0]!.step).toBe('Read SECRET-PLAN prose');expect(view.checkpoint.representation).toBe('derived');expect((view.checkpoint.document as typeof raw).planState!.revision).toBe(raw.planState!.revision);expect(JSON.stringify(view.entry)).not.toContain('SECRET-PLAN');expect(()=>readExecution(view.checkpoint.document)).toThrow();expect(()=>readExecution(view.checkpoint)).toThrow()
 for(const [,files]of sink.write.mock.calls as unknown as [string,Record<string,string>][])expect(Object.values(files).join('')).not.toContain('SECRET-PLAN')
 const members={outputPatterns:{value:[{name:'plan-prose',pattern:'SECRET-PLAN'}]}} as never,archive=await exportRunArchive(store,'live-private-plan',{members}),imported=readArchiveCapturedCorpusView(readRunArchive(archive.bytes,archive.digest));expect(imported.checkpoint).toEqual(view.checkpoint)
 for(const blob of JSON.parse(new TextDecoder().decode(archive.bytes)).objects)expect(Buffer.from(blob.bytes,'base64').toString()).not.toContain('SECRET-PLAN')
 await replayCorpus(imported.entry,async server=>consume(vercel.start({...base,model:{family:'openai-compatible',model:'fixture',call:(suffix,request)=>server.model(suffix,request)}})));expect(await store.load('live-private-plan')).toEqual(head)
})
