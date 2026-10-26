# Host feedback and pending corpus review

A host can attach `good` or `bad` feedback to a retained run or one finalized assistant message. The host supplies an actor and UTC timestamp. An assistant-message target includes its retained event sequence and original event digest. An edit keeps the same feedback ID and target, supplies the previous original feedback digest, and appends a new record. An absent remark remains distinct from an empty remark.

`writeRunFeedback` in TypeScript and `WriteRunFeedback` in Go use the history metadata transaction. The host pins both the composite history revision and metadata revision. Feedback changes no execution checkpoint, event, prompt, model request, allowance or dispatch. It stays outside prompt assembly, including compaction and explicit resume. The returned receipt is verified before review material is exposed.

```ts
const head = await store.readHistoryHead(runId)
const result = await writeRunFeedback(store, {
  runId, id: 'rating', commitId: 'rating-1',
  expectedHistoryRevision: head!.revision,
  expectedMetadataRevision: head!.metadata?.revision ?? null,
  previous: null, target: {kind: 'run'}, rating: 'bad',
  remark: 'The result omitted a required section.',
  actor: 'host-user', recordedAt: '2026-10-25T01:02:03.000Z',
}, {members: redactionPolicy, sink: candidateSink})
```

Bad ratings request a pending review candidate by default. Good ratings request one only with `proposeGood: true` (`ProposeGood` in Go). For a running or paused source, the feedback record retains `candidateRequested: true` and delivery is `deferred`; no terminal report or candidate is fabricated. The host must explicitly submit a feedback revision after the run becomes terminal. That revision revalidates the exact current source, prior rating and message identity. A good revision without opt-in cancels the current candidate intent; earlier records remain readable. There is no automatic retry or later background publication.

For a terminal source, preparation redacts detached logical records before staging a candidate privately. The existing metadata compare-and-swap publishes the rating and its complete candidate reference together. A stale prepared revision cannot attach its candidate after an edit. Replaying an already committed older receipt after an edit reports `superseded` and withholds delivery material. The deterministic candidate ID combines the run ID, original feedback digest and pinned archive digest.

A candidate is `pending-review`, never automatic corpus admission. `CorpusCandidateSink` receives bounded sanitized report/message excerpts separately from `CorpusSink`, which still requires a complete recorded entry. Candidate delivery is `pending` without a sink, `delivered` only after a successful sink return, or `unknown` after sink failure. The metadata and pending candidate remain durable when delivery fails. An unverifiable transaction acknowledgement raises `FeedbackOutcomeUnknown`; execution is never retried to resolve it.

`projectHistoryRunReport` (`ProjectHistoryRunReport`) adds stored feedback to report2. Old checkpoint1 and checkpoint2 reports retain their real source version; feature-free projections remain unchanged. Usage totals and route fields are permitted only for checkpoint3 sources and are reconciled with those sources during archive import.

Archive imports expose detached display data. Redaction gives displayed bytes their own digest and retains the original feedback digest as `sourceDigest`, so edited remarks preserve their original chain labels. Those labels and self-consistent source-to-derived mappings establish internal consistency only. They establish no original execution authenticity and grant no execution, store, provider, resume or corpus-admission authority. An optional externally pinned archive digest checks the archive bytes before exposure.

Explicit job, artifact and dialect relations bind actual retained scoped dispatch, request, result and host-observation evidence. Query and archive readers refuse missing or contradictory associations. Result prose and arbitrary object keys never invent a relation. A host assertion does not establish external success or provider authenticity.

This slice provides readable review candidates with `replay: {kind: 'unavailable', reason: 'not-recorded'}`. It preserves declared retained-history gaps and makes no complete-capture or replay-ready claim.
