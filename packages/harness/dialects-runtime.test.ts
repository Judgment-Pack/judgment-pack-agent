import {describe,expect,it} from 'vitest'
import {mkdtempSync,writeFileSync} from 'node:fs'
import {tmpdir} from 'node:os'
import {join,resolve} from 'node:path'
import {spawnSync} from 'node:child_process'
import {dialectResearchIndex,dialectTools,type DialectReviewProposal} from './dialects'
import {dialectFixture} from './testing/dialects'
import fixture from '../../contract/testdata/dialect-authoring/authoring.json'
import generatedRead from '../../contract/testdata/dialect-authoring/generated-read.json'
import generatedExpected from '../../contract/testdata/dialect-authoring/generated-read-expected.json'
import {canonicalJSON} from './context/chat'
import retainedPolicy from '../../contract/testdata/dialect-authoring/retained-policy.json'
import {contextDigest} from './context/ledger'
const executable=process.env.JPACK_DIALECT_RUNTIME
// The host read/preview/retention ports remain fixtures. Only these semantic calls run Runtime.
describe('actual selected provisional Runtime CLI dialect transport',()=>{
 it.skipIf(!executable)('validates exact replacement bytes and distinguishes explicit omissions',async()=>{
  if(!process.env.TMPDIR)throw Error('Use initialized session scratch/TMPDIR for actual Runtime probes.')
  const selected=process.env.JPACK_DIALECT_RUNTIME_MODE
  if(selected!==undefined&&selected!=='generated')throw Error('Unsupported focused Runtime mode')
  const modes=selected===undefined?['text','decimal','number','boolean','research-value','omitted','generated','planned']:['generated']
  const invocations:string[]=[]
  const directory=mkdtempSync(join(tmpdir(),'dialect-runtime-')),packFile=join(directory,'pack.json')
  for(const mode of modes){
   const omitted=mode==='omitted'
   const x=dialectFixture(),retained:DialectReviewProposal[]=[]
   x.state.declarations=structuredClone(retainedPolicy.researchDeclarations)
   if(mode==='generated')x.state.snapshot.retainedRead={...generatedRead,occurrence:{kind:'source',name:'retained-generated'}}
   if(mode==='planned'){x.state.snapshot.recordCount=0;x.state.snapshot.columns.forEach(c=>{c.samples=[];delete c.research});x.state.declarations=null}
   const tools=dialectTools({...x.ports,runtime:async(name,args,signal)=>{
    signal.throwIfAborted();if(invocations.length>=modes.length*3)throw Error('Runtime probe call bound exceeded');const operation=name==='get_dialect_contract'?'contract':name==='dialect_validate'?'validate':'check'
    invocations.push(operation)
    if(operation==='check')writeFileSync(packFile,args.pack!,{mode:0o600})
    const reply=spawnSync(resolve(executable!),['dialect',operation,'--draft-contract',args.draftContract!,'--draft-profile',args.draftProfile!,...(operation==='contract'?[]:['-']),...(operation==='check'?['--pack',packFile]:[]),'--format','json'],{input:args.document,encoding:'utf8',timeout:5000,maxBuffer:4194304})
    if(process.env.JPACK_DIALECT_EVIDENCE)writeFileSync(join(process.env.JPACK_DIALECT_EVIDENCE,`ts-runtime-${String(invocations.length).padStart(2,'0')}.json`),JSON.stringify({mode,operation,args,status:reply.status,reportJson:reply.stdout},null,2))
    if(reply.error)throw reply.error
    signal.throwIfAborted();return {isError:reply.status!==0,content:[{type:'text',text:reply.stdout}]}
   },retain:async(proposal)=>{retained.push(proposal);return {fingerprint:proposal.fingerprint,receipt:'review:actual',status:'review-required',draft:proposal.draft}}})
   const read=await tools[0]!.execute(fixture.request,new AbortController().signal),shown=JSON.parse(read.content![0]!.text!)
   const args={...fixture.request,revision:fixture.snapshot.revision,packDigest:'sha256:'+await contextDigest(fixture.snapshot.packJson),dialectDigest:shown.dialectDigest,...fixture.bindings,...(omitted?{facts:[],evidence:[]}:{facts:[{target:'/amount',column:mode==='planned'?'missing':'typed',conversion:fixture.conversions.find(c=>c.kind===mode)??{kind:'decimal'}}]})}
   const result=await tools[1]!.execute(args,new AbortController().signal),body=JSON.parse(result.content![0]!.text!)
   expect(body.coverageStatus).toBe(omitted?'review-required':'complete');expect(retained).toHaveLength(1)
   expect(retained[0]!.packJson).toBe(fixture.snapshot.packJson)
   expect(retained[0]!.validation.status).toBe('document-valid')
   expect(retained[0]!.researchIndex).toEqual(await dialectResearchIndex(x.state.declarations))
   if(mode==='generated'){
    if(process.env.JPACK_DIALECT_EVIDENCE)writeFileSync(join(process.env.JPACK_DIALECT_EVIDENCE,'ts-generated-checkpoint.json'),JSON.stringify(x.state.drafts[0],null,2))
    const expected=canonicalJSON(generatedExpected.document),proposal=retained[0]!
    expect(proposal.documentJson).toBe(expected)
    for(const report of [proposal.validation,proposal.coverage]){expect(report.document).toBe(expected);expect(report.digest).toBe(generatedExpected.digest)}
   }
   if(omitted) expect(JSON.parse(retained[0]!.documentJson)).toMatchObject({unmapped:['/amount'],unmappedEvidence:['receipt']})
  }
  expect(invocations).toEqual(Array.from({length:modes.length},()=>['contract','validate','check']).flat())
 },30000)
})
