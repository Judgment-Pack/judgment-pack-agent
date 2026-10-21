import {describe,expect,it} from 'vitest'
import {compareCorpus,replayCorpus,writeCorpusEntry,type CorpusEntry} from './corpus'
import eventSchema from '../../contract/schemas/run-event.json'
import {argumentProblem} from './sourceArguments'

const manifest={id:'model-policy-refusal',packageVersion:'0.8.0',policy:{taskClass:'boundary'},files:{requests:'requests.json' as const,toolResults:'tool-results.json' as const,events:'events.json' as const,checkpoint:'checkpoint.json' as const},expected:{file:'expected.json' as const},tolerance:['/timings']}
const event={type:'guardrail' as const,tool:'pace',action:'refused' as const,reason:'pace warning' as const,detail:'warning'}

describe('evaluation corpus',()=>{
 it('redacts before the sink sees an entry and announces exact bytes',async()=>{
  let written=false
  const answer=await writeCorpusEntry({manifest,requests:[],toolResults:[],events:[event],checkpoint:{secret:'token'}},{value(value){return JSON.parse(JSON.stringify(value).replace('token','[REDACTED]'))}},{write(_id,files){written=true;expect(files['checkpoint.json']).toContain('[REDACTED]')}})
  expect(written).toBe(true);expect(answer.event.type).toBe('recorded');expect((answer.event as {bytes:number}).bytes).toBeGreaterThan(0)
 })
 it('writes nothing when redaction refuses',async()=>{
  for(const malformed of ['',undefined,'x'.repeat(1_000_000)]){let written=false
   const answer=await writeCorpusEntry({manifest,requests:[],toolResults:[],events:[event],checkpoint:{malformed}},{value(){throw Error('refuse')}},{write(){written=true}})
   expect(written).toBe(false);expect(answer.event).toMatchObject({type:'guardrail',reason:'record refused'});expect(argumentProblem(eventSchema.$defs.guardrail,answer.event)).toBeUndefined()
  }
 })
 it('honours only the manifest tolerance and names a tampered member',()=>{
  expect(compareCorpus({gateDecisions:[],outcomes:[],refusals:[],timings:1},{gateDecisions:[],outcomes:[],refusals:[],timings:2},['/timings'])).toBeUndefined()
  expect(compareCorpus({gateDecisions:[{action:'refused'}],outcomes:[],refusals:[]},{gateDecisions:[{action:'rewrote'}],outcomes:[],refusals:[]},[] )?.member).toBe('/gateDecisions/0/action')
 })
 it('replays through recorded transports and never compares prose',async()=>{
  const entry:CorpusEntry={manifest,requests:[],toolResults:[],events:[event,{type:'message',text:'old prose'}],checkpoint:{},expected:{gateDecisions:[{tool:'pace',action:'refused',reason:'pace warning'}],outcomes:[],refusals:[{tool:'pace',action:'refused',reason:'pace warning'}]}}
  await replayCorpus(entry,async()=>[event,{type:'message',text:'different prose'}])
 })
})
