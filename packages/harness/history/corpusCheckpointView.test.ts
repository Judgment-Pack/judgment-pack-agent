import {expect,it} from 'vitest'
import vectors from '../../../contract/fixtures/history/corpus-checkpoint-view.json'
import {readCorpusCheckpointView} from './corpusCheckpointView'
import {historyJSONBytes,historyBytesDigest} from './codec'
import {readExecution} from '../execution/state'
import {ReplayUnavailable} from './capture'
it('reads handwritten exact and derived vectors without native authority',()=>{
 for(const name of ['exact','derived','legacy1','legacy2'] as const){const input=vectors[name],view=readCorpusCheckpointView(input);expect(view).toEqual(input);expect(view.derived).toEqual({digest:historyBytesDigest(historyJSONBytes(input.document)),bytes:historyJSONBytes(input.document).length});expect(()=>readExecution(view)).toThrow()}
 expect(()=>readExecution(vectors.derived.document)).toThrow()
 expect(vectors.derived.source.digest).toBe('sha256:b9098ae2eb22759ae29710cbeb4bc45744d3586aa171c6530c02f6afd611efc1')
 expect(vectors.derived.derived.digest).toBe('sha256:59e3cb4e2ad3cb55626448c50910e09f2ff119ae0171580e35e87c2682fc0b40')
})
for(const fault of vectors.refusals)it(`refuses checkpoint view ${fault}`,()=>{
 const v:any=structuredClone(vectors.derived)
 if(fault==='derived-digest')v.derived.digest='sha256:'+'0'.repeat(64)
 if(fault==='derived-bytes')v.derived.bytes++
 if(fault==='source-version')v.sourceVersion=2
 if(fault==='representation')v.representation='exact'
 if(fault==='sanitized-exact'){v.source=structuredClone(v.derived);v.representation='exact'}
 if(fault==='wrapper-member')v.id='native-looking'
 if(fault==='document-member')v.document.unknown=true
 if(fault==='legacy-current-member'){v.document.version=2;v.sourceVersion=2}
 if(['document-member','legacy-current-member'].includes(fault)){const bytes=historyJSONBytes(v.document);v.derived={digest:historyBytesDigest(bytes),bytes:bytes.length}}
 expect(()=>readCorpusCheckpointView(v)).toThrow(ReplayUnavailable)
})
