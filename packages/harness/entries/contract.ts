/**
 * The contract a host sees: the engine slot's types and events (`engine.ts`),
 * what every adapter shares (`engines/contract.ts`), the registry a host loads
 * an adapter through (`engines/index.ts`), the host's own side, its contract
 * values and injection points (`host.ts`), the capability record a host
 * answers with (`capabilities.ts`), the image part (`images.ts`), the
 * chat-scoped context a host makes once per chat (`context/chat.ts`, agent#8)
 * and what a host hands and reads of the critic (`refutation.ts`: its
 * allow-list, the cases it declares, the comparisons and the figures; agent#10).
 */
export * from '../engine'
export * from '../engines/contract'
export * from '../engines/index'
export * from '../host'
export * from '../capabilities'
export * from '../images'
export { ChatContext, canonicalJSON, readCacheKey, type ChatScope, type ReadCacheKey, type RuntimeIdentity } from '../context/chat'
export type { ContextInput } from '../context/session'
export { CRITIC_TOOLS, readCriticCases, criticMatrix } from '../refutation'
// ADR-0010: the critic's switch, which replaced criticRuns.
export { criticDecision, criticMode, criticTier, CRITIC_MODES, CRITIC_REASONS, REQUEST_REVIEW } from '../criticSwitch'
export type { CriticDecision, CriticMode, CriticReason, CriticTier, CriticTrigger } from '../criticSwitch'
export type { CriticCase, CaseComparison, CaseOutcome, CriticFigures } from '../refutation'

export {resolvePolicy, PolicyRefusal, failoverRefusal} from '../policy'
export type {ProviderFamily,ProviderRecord,CrossProvider,PolicyRole,PolicyLayer,PolicyMembers,PolicyConnection} from '../policy'
export {exportRecords,recordDigest,recordsPolicy,refuseExpired,writeRecord,RecordsExpired,RECORD_DEFAULTS,CHECKPOINT_SCHEMA,PROVENANCE_SCHEMA,CRITIQUE_SCHEMA,EXPORT_SCHEMA} from '../records'
export type {RecordsPolicy,RecordMembers,RecordGuard,ExportEntry,ExportManifest,RecordsExport,ExportWritten} from '../records'

export type { Memory, MemoryAnswer, MemoryRecall, Memories } from '../memories'
export { MEMORY_KINDS, REMEMBER, REMEMBER_SCHEMA } from '../memories'
export type { Edit, EditType, DraftCheckpoint, AcceptedDocument } from '../edits'
export { EDIT_TOOLS, editSchema } from '../edits'

export {readRunEvent,readPolicyLayer,RECORD_VERSIONS,UnsupportedRecordVersion,UnknownRecordMember} from '../recordReader'

// Shared Gateway descriptor snapshot pins (agent#168).
export { DESCRIPTOR_SNAPSHOT_BYTES, descriptorSnapshotDigest, readDescriptorSnapshot } from '../descriptorSnapshot'
export type { DescriptorSnapshot, DescriptorTool } from '../descriptorSnapshot'
export * from '../storage/run'
export * from '../storage/common'
export * from '../storage/conformance'
export {MemoryChatStore} from '../storage/chat'
export type {ChatEntry,ChatListRequest,ChatPage,StoredChat,ChatPatch} from '../storage/chat'
export type {DurableRunInput,DurableScope,ArtifactObservation,DurableObservation} from '../storage/writer'
export {ContinuationNotRecoverable,ContinuationIdentityChanged,HostOutcomeUnknown,StorageAcknowledgementInvalid,StorageOutcomeUnknown} from '../storage/writer'

export * from '../planning/references'
export type {ChatPlan,PlanReference,CurrentPlanReference,LegacyPlanReference} from '../planning/model'
export {planTools,acceptPlan,checkPlan} from '../planning/tools'

export {readStoredRunEvent} from '../storage/writer'
export {legacyChatStoreAdapter} from '../storage/legacyChat'

export {ProviderReleaseUnknown} from '../storage/release'
export type {ProviderRelease} from '../storage/release'

export type {TaskStep,ExecutionCheckpoint} from '../execution/state'
export type {PlanState,PlanRevision,PlanChange,PlanOrigin,StepEvidence,AcceptedPlan,Departure,DepartureKind,PlanRefusal,PlanDecision,PendingDeparture} from '../execution/plan'

export * from '../runReport'
