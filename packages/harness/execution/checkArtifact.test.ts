import {expect,it,vi} from 'vitest'
import {checkArtifact} from './checkArtifact'
const signal=()=>new AbortController().signal
it('retains the exact candidate before checking and all findings before returning to the managed turn',async()=>{
 const events:string[]=[],candidate={version:'9007199254740993.0.0',value:false,missing:null}
 const finding={status:'refused',findings:[{code:'invalid',path:'/value'}]}
 const result=await checkArtifact({candidate,fingerprint:JSON.stringify,
  retain:async(c,check)=>{expect(c).toBe(candidate);events.push(check?'findings':'candidate');if(check)expect(check).toBe(finding)},
  check:async c=>{expect(c).toBe(candidate);events.push('check');return finding},passed:()=>false,
 },signal())
 expect(result).toBe(finding);expect(events).toEqual(['candidate','check','findings'])
})
it('does not dispatch a check when retaining fails, or return findings before durable retention',async()=>{
 const check=vi.fn(async()=>false),retain=vi.fn(async():Promise<void>=>{throw Error('Persistence unavailable')})
 await expect(checkArtifact({candidate:1,fingerprint:String,retain,check,passed:Boolean},signal())).rejects.toThrow('Persistence unavailable')
 expect(check).not.toHaveBeenCalled()
 retain.mockResolvedValueOnce(undefined)
 await expect(checkArtifact({candidate:1,fingerprint:String,retain,check,passed:Boolean},signal())).rejects.toThrow('Persistence unavailable')
 expect(check).toHaveBeenCalledOnce()
})
it('preserves interruption as a pending candidate and never starts correction or another check',async()=>{
 const controller=new AbortController(),events:string[]=[]
 await expect(checkArtifact({candidate:'candidate',fingerprint:String,
  retain:async()=>{events.push('retained')},check:async()=>{events.push('check');controller.abort();return false},passed:Boolean,
 },controller.signal)).rejects.toMatchObject({name:'AbortError'})
 expect(events).toEqual(['retained','check'])
})
