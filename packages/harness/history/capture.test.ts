import {expect,it} from 'vitest'
import vectors from '../../../contract/fixtures/history/capture.json'
import {readCapturedCorpus,readArchiveCapturedCorpus,ReplayUnavailable,type CaptureReadView} from './capture'
import {historyJSONBytes,historyBytesDigest,historyJSONDigest} from './codec'
import {readRunCapture} from './metadata'
function fixture(){const s=structuredClone(vectors.source),objects=new Map(Object.entries(s.objects).map(([digest,b])=>[digest,new Uint8Array(Buffer.from(b,'base64'))]));return {source:s,objects,view:{head:s.head,commits:s.commits,metadata:s.metadata,readObject:(r:{digest:string})=>objects.get(r.digest)!} as CaptureReadView}}
function mutateObject(f:ReturnType<typeof fixture>,digest:string,change:(v:any)=>void){const value=JSON.parse(new TextDecoder().decode(f.objects.get(digest)!));change(value);const bytes=historyJSONBytes(value),ref={digest:historyBytesDigest(bytes),bytes:bytes.length};f.objects.set(ref.digest,bytes);return ref}
function replaceFile(f:ReturnType<typeof fixture>,name:string,change:(v:any)=>void){
 const captureRef=f.source.metadata[0]!.record.capture,capture=JSON.parse(new TextDecoder().decode(f.objects.get(captureRef.digest)!)),file=capture.files.find((x:any)=>x.name===name),manifest=JSON.parse(new TextDecoder().decode(f.objects.get(file.manifest.digest)!)),parts=manifest.parts.flatMap((p:any)=>Array.from(f.objects.get(p.digest)!)),value=JSON.parse(new TextDecoder().decode(new Uint8Array(parts)));change(value)
 const bytes=historyJSONBytes(value),part={digest:historyBytesDigest(bytes),bytes:bytes.length};f.objects.set(part.digest,bytes);const encoded=historyJSONBytes({version:1,bytes:bytes.length,digest:part.digest,parts:[part]});file.manifest={digest:historyBytesDigest(encoded),bytes:encoded.length};f.objects.set(file.manifest.digest,encoded);const raw=historyJSONBytes(capture),ref={digest:historyBytesDigest(raw),bytes:raw.length};f.objects.set(ref.digest,raw);f.source.metadata[0]!.record.capture=ref
}
it('keeps historical native capture metadata exact and reports unavailable without original binding evidence',()=>{const f=fixture();expect(()=>readCapturedCorpus(f.view)).toThrow(ReplayUnavailable);expect(readRunCapture(historyJSONBytes(vectors.capture))).toEqual(vectors.capture);expect(Array.from(historyJSONBytes(readRunCapture(historyJSONBytes(vectors.capture))))).toEqual(Array.from(historyJSONBytes(vectors.capture)))})
for(const fault of ['missing','duplicate','order','unknown','digest','files','metadata','request-source','native-tools','manifest-extra','response-shape'])it(`refuses native capture closure ${fault}`,()=>{
 const f=fixture(),last=f.source.commits[1]!,captureRef=f.source.metadata[0]!.record.capture
 if(fault==='native-tools')replaceFile(f,'requests.json',rows=>{rows[1].response.body=JSON.stringify({steps:[{tool:{id:'invisible',name:'lookup',arguments:{}}}]})})
 else if(fault==='manifest-extra')replaceFile(f,'manifest.json',m=>{m.extra=true})
 else if(fault==='response-shape')replaceFile(f,'requests.json',rows=>{rows[0].response.headers=[]})
 else if(fault==='metadata')f.source.head.metadata.count=2
 else if(fault==='request-source'){const ref=mutateObject(f,last.observations[0]!.digest,o=>{o.source='foreign'});last.observations[0]=ref}
 else if(fault==='files')f.objects.delete(vectors.capture.files[0]!.manifest.digest)
 else if(fault==='missing')last.observations=last.observations.filter((_,i)=>i!==1)
 else if(fault==='duplicate')last.observations.push(last.observations[1]!)
 else if(fault==='order'){const ref=mutateObject(f,captureRef.digest,c=>{c.requests.reverse();c.requests.forEach((r:any,i:number)=>{r.captureIndex=i})});f.source.metadata[0]!.record.capture=ref}
 else if(fault==='unknown'){const ref=mutateObject(f,last.checkpoint!.digest,c=>{c.durable.dispatches[0].state='unknown'});last.checkpoint=ref;f.source.head.checkpoint=ref;replaceFile(f,'checkpoint.json',c=>{c.durable.dispatches[0].state='unknown'});f.source.metadata[0]!.record.capture=mutateObject(f,f.source.metadata[0]!.record.capture.digest,c=>{c.checkpoint=ref})}
 else {const ref=mutateObject(f,captureRef.digest,c=>{c.requests[0].requestDigest='sha256:'+'0'.repeat(64)});f.source.metadata[0]!.record.capture=ref}
 // Re-sign the containing native journal/metadata labels so the intended semantic safeguard is exercised.
 let prior:string|null=null;for(const c of f.source.commits){c.previousDigest=prior;const {digest:_,...body}=c;c.digest=historyJSONDigest(body);prior=c.digest}f.source.head.root=prior!;const row=f.source.metadata[0]!,{digest:_,...body}=row;row.digest=historyJSONDigest(body);f.source.head.metadata.root=row.digest
 expect(()=>readCapturedCorpus(f.view)).toThrow(ReplayUnavailable)
})

it('returns the fixed unavailable result for absent native or imported views',()=>{expect(()=>readCapturedCorpus(null as never)).toThrow(ReplayUnavailable);expect(()=>readArchiveCapturedCorpus(null as never)).toThrow(ReplayUnavailable)})
