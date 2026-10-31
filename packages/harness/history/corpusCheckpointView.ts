import {readExecution} from '../execution/state'
import {checkRecord} from '../recordReader'
import {storageId} from '../storage/common'
import {canonicalJSON} from '../context/chat'
import {historyJSONBytes,historyBytesDigest,validHistoryRef,type HistoryObjectRef} from './codec'
import {ReplayUnavailable} from './captureCollector'
export interface CorpusCheckpointView {version:1;kind:'corpus-checkpoint-view';source:HistoryObjectRef;sourceVersion:1|2|3|4;derived:HistoryObjectRef;representation:'exact'|'derived';document:unknown}
/** Internal projection after complete capture closure validation. This never issues native progress. */
export function corpusCheckpointView(source:HistoryObjectRef,document:unknown):CorpusCheckpointView {
 try{
 if(!validHistoryRef(source))throw new ReplayUnavailable()
 checkRecord('checkpoint',document)
 const cp=document as {version:1|2|3|4;id:string}
 if(!storageId(cp.id)||![1,2,3,4].includes(cp.version))throw new ReplayUnavailable()
 const bytes=historyJSONBytes(document),derived={digest:historyBytesDigest(bytes),bytes:bytes.length},exact=source.digest===derived.digest&&source.bytes===derived.bytes
 if(!validHistoryRef(derived)||exact&&!readExecution(document))throw new ReplayUnavailable()
 return {version:1,kind:'corpus-checkpoint-view',source:{...source},sourceVersion:cp.version,derived,representation:exact?'exact':'derived',document:structuredClone(document)}
 }catch{throw new ReplayUnavailable()}
}
/** Self-consistency only. Provenance is supplied by a validated archive/capture view, never this reader. */
export function readCorpusCheckpointView(value:unknown):CorpusCheckpointView {
 try{
 if(!value||typeof value!=='object'||Array.isArray(value))throw new ReplayUnavailable()
 const v=value as CorpusCheckpointView
 if(Object.keys(v).sort().join(',')!=='derived,document,kind,representation,source,sourceVersion,version'||v.version!==1||v.kind!=='corpus-checkpoint-view'||!validHistoryRef(v.derived))throw new ReplayUnavailable()
 const checked=corpusCheckpointView(v.source,v.document)
 if(canonicalJSON(checked.derived)!==canonicalJSON(v.derived)||v.sourceVersion!==checked.sourceVersion||v.representation!==checked.representation)throw new ReplayUnavailable()
 return checked
 }catch{throw new ReplayUnavailable()}
}
