import {expect,it} from 'vitest'
import vectors from '../../../contract/fixtures/history/capture.json'
import {CaptureCollector,ReplayUnavailable,sealedCapture,type CaptureAdmission} from './captureCollector'
const fresh=()=>{const owner={},collector=new CaptureCollector(owner,'r',vectors.root);return {owner,collector,root:collector.rootToken(owner)}}
it('orders private acknowledged rows by admission despite reverse child completion',()=>{
 const {owner,collector,root}=fresh(),tokens=vectors.admissions.map(a=>collector.admit(owner,a as CaptureAdmission))
 for(const i of [1,0]){collector.record(owner,tokens[i]!,vectors.frames[i]);collector.acknowledge(owner,tokens[i]!)}
 const seal=collector.seal(owner,root,vectors.terminal as never),material=sealedCapture(seal);expect(material.rows.map(r=>r.admission.identity.dispatchId)).toEqual(['d1','d2']);expect(material.rows.map(r=>r.frame)).toEqual(vectors.frames)
 ;(material.rows[0]!.frame as any).suffix='changed';expect((sealedCapture(seal).rows[0]!.frame as any).suffix).toBe('/one');expect(()=>sealedCapture({...seal})).toThrow(ReplayUnavailable)
})
for(const fault of ['owner','token','missing','unacknowledged','duplicate','admission','order','child-root'])it(`refuses capture ${fault} without manufacturing a complete closure`,()=>{
 const {owner,collector,root}=fresh(),first=vectors.admissions[0] as CaptureAdmission,t=collector.admit(owner,first)
 if(fault==='owner')expect(()=>collector.record({},t,vectors.frames[0])).toThrow(ReplayUnavailable)
 else if(fault==='token')expect(()=>collector.record(owner,{...t},vectors.frames[0])).toThrow(ReplayUnavailable)
 else if(fault==='duplicate'){collector.record(owner,t,vectors.frames[0]);expect(()=>collector.record(owner,t,vectors.frames[0])).toThrow(ReplayUnavailable)}
 else if(fault==='admission')expect(()=>collector.admit(owner,{...first,sequence:2})).toThrow(ReplayUnavailable)
 else if(fault==='order')expect(()=>collector.admit(owner,{...vectors.admissions[1],sequence:1,ordinal:0} as CaptureAdmission)).toThrow(ReplayUnavailable)
 else {if(fault!=='missing')collector.record(owner,t,vectors.frames[0]);if(fault!=='unacknowledged')collector.acknowledge(owner,t);expect(()=>collector.seal(owner,fault==='child-root'?({} as never):root,vectors.terminal as never)).toThrow(ReplayUnavailable)}
})

it('refuses caller marker owners and empty, missing or oversized admission material',()=>{
 expect(()=>new CaptureCollector('marker' as never,'r',vectors.root)).toThrow(ReplayUnavailable)
 for(const fault of ['empty','missing','oversized']){const {owner,collector}=fresh(),a=structuredClone(vectors.admissions[0]) as CaptureAdmission;if(fault==='empty')a.identity.dispatchId='';else if(fault==='missing')delete (a as Partial<CaptureAdmission>).identity;else a.request='x'.repeat(4*1024*1024+1);expect(()=>collector.admit(owner,a)).toThrow(ReplayUnavailable)}
})
