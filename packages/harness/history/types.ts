import type {DurableScope} from '../storage/writer'
import type {HistoryObjectRef} from './codec'

/** Sidecar data is host evidence only. It is never prompt input or execution authority. */
export type HistoryObservation=
 | {version:1;kind:'genesis';scope:DurableScope;profile:HistoryCompleteness}
 | ({version:1;kind:'capture-request';captureKind:'model'|'agent'}&Omit<HistoryCaptureIdentity,'captureIndex'|'kind'>)
 | {version:1;kind:'original';scope:DurableScope;boundary:string;value:HistoryObjectRef;format:'messages'|'context';ordinal:number}
 | {version:1;kind:'compaction';scope:DurableScope;boundary:string;mode:'index'|'summary';originals:HistoryObjectRef[];replacement:HistoryObjectRef;prior:HistoryObjectRef[];legacyDigest:string}
 | {version:1;kind:'request';scope:DurableScope;dispatchId:string;source:string;tool:string;request:HistoryObjectRef;compactions:HistoryObjectRef[];originals:HistoryObjectRef[]}
 | {version:1;kind:'relation';scope:DurableScope;dispatchId:string;target:{kind:'job'|'artifact'|'dialect';id:string};evidence:HistoryObjectRef}
export interface HistoryCompleteness{version:1;profile:'full-history';genesis:true;originals:true;compactions:true;requests:true}
export interface HistoryGap{code:'legacy-genesis'|'missing-original'|'missing-compaction'|'missing-request'|'missing-capture';sequence?:number;identity?:string}
export type HistoryCoverage={kind:'complete';profile:HistoryCompleteness}|{kind:'incomplete';gaps:HistoryGap[]}
export interface HistoryCaptureIdentity{scope:DurableScope;dispatchId:string;kind:'model'|'agent';source:string;tool:string;requestDigest:string;surface:'session-model'|'native-aggregate';captureIndex:number}
export interface RunCapture{eventProjection?:'transcript-v1';version:1;runId:string;executionRevision:string;checkpoint:HistoryObjectRef;requests:HistoryCaptureIdentity[];files:{name:'manifest.json'|'requests.json'|'tool-results.json'|'events.json'|'checkpoint.json'|'expected.json';manifest:HistoryObjectRef}[];redacted:true}
export type FeedbackTarget={kind:'run'}|{kind:'assistant-message';eventSequence:number;eventDigest:string}
export interface RunFeedback{version:1;id:string;runId:string;sequence:number;previous:string|null;target:FeedbackTarget;rating:'good'|'bad';remark?:string;actor:string;recordedAt:string}
export type HistoryMetadata={version:1;kind:'capture';capture:HistoryObjectRef}|{version:1;kind:'feedback';feedback:RunFeedback;candidate?:HistoryObjectRef;candidateRequested?:true}
export interface HistoryMetadataEntry{version:1;executionRevision:string;commitSequence:number;sequence:number;recordedAt:string;previousDigest:string|null;record:HistoryMetadata;digest:string}
export interface HistoryMetadataHead{version:1;runId:string;revision:string;executionRevision:string;count:number;root:string|null}
export interface HistoryMetadataPage{revision:string;afterSequence:number;items:HistoryMetadataEntry[];nextSequence:number;hasMore:boolean;total:number}

export type {ArchiveRepresentation,ArchiveMember,ArchiveMapping as ArchiveSourceMapping,RunArchiveManifest,RunArchive} from './archive'
