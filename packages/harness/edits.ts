import {readProposalDocument,ProposalDocumentError} from './proposalNumbers'
import {mutateCheckpoint,type CheckpointPatch} from './execution/mutation'
import {OutputRefused} from './outputGuard'
/** ADR-0015: the package's draft, never a host file or an approval. */
import type {AssistantEvent,HostTool,McpToolResult} from './engine'
import type {Proposal} from './engines/contract'
import type {ExecutionCheckpoint} from './execution/state'
import {answer,contextDigest,type ContextLedger} from './context/ledger'
import {utf8Bytes,type ContextPolicy} from './context/policy'
import {frameBlock} from './framing'
import {toolInputFailure,authoredFailure} from './failures'

export const EDIT_TOOLS=['propose_edit','propose_delete','propose_insert','undo_edit'] as const
export type EditType=typeof EDIT_TOOLS[number]
export interface Edit {type:EditType;document:string;line:number;bytes:number;valid:true}
export interface DraftCheckpoint {documents:{id:string;digest:string;edits:Edit[]}[]}
export interface AcceptedDocument {id:string;document:Record<string,unknown>;unknowns?:string[]}
interface Held {id:string;text:string;unknowns:string[];undo:{reference:string;line:number}[];edits:Edit[];digest:string}
const fields:Record<EditType,string[]>={propose_edit:['document','old_text','new_text'],propose_delete:['document','old_text'],propose_insert:['document','after_anchor','text'],undo_edit:['document']}
export function editSchema(type:EditType){return {type:'object',properties:Object.fromEntries(fields[type].map(name=>[name,{type:'string',...(name==='document'?{minLength:1,maxLength:512}:['old_text','after_anchor'].includes(name)?{minLength:1}:{})}])),required:fields[type],additionalProperties:false}}
export const isEdit=(name:string):name is EditType=>(EDIT_TOOLS as readonly string[]).includes(name)
const object=(v:unknown):v is Record<string,unknown>=>!!v&&typeof v==='object'&&!Array.isArray(v)
export function validDraft(value:unknown):value is DraftCheckpoint {
 if(!object(value)||Object.keys(value).some(k=>k!=='documents')||!Array.isArray(value.documents))return false
 const ids=new Set<string>()
 return value.documents.every(d=>{
  if(!object(d)||Object.keys(d).some(k=>!['id','digest','edits'].includes(k))||typeof d.id!=='string'||!d.id.startsWith('draft:')||d.id.length<=6||[...d.id].length>518||ids.has(d.id)||typeof d.digest!=='string'||!/^sha256:[0-9a-f]{64}$/.test(d.digest)||!Array.isArray(d.edits))return false
  ids.add(d.id)
  const document=d.id.slice(6)
  return d.edits.every(e=>object(e)&&Object.keys(e).length===5&&isEdit(String(e.type))&&typeof e.document==='string'&&e.document===document&&Number.isSafeInteger(e.line)&&Number(e.line)>=1&&Number.isSafeInteger(e.bytes)&&Number(e.bytes)>=0&&e.valid===true)
 })
}
const label=(d:{id:string;digest:string;edits:Edit[]})=>`${d.id}@${d.digest}:${d.edits.length}`
export function editRefusal(document:unknown,reason:string):Extract<AssistantEvent,{type:'edit_refused'}>{
 const id=typeof document==='string'?document:''
 const invalid=!id||[...id].length>512
 return {type:'edit_refused',...(id?{document:[...id].slice(0,512).join('')} : {}),reason:invalid?'Invalid document identifier (expected 1 to 512 characters). '+reason:reason}
}
export class Drafts {
 private documents=new Map<string,Held>()
 private queue:Promise<unknown>=Promise.resolve()
 constructor(private ledger:ContextLedger,private policy:ContextPolicy,private state:()=>ExecutionCheckpoint,private notify:(event:AssistantEvent)=>void,private allowed:()=>boolean,private validate:(text:string,signal:AbortSignal)=>Promise<McpToolResult>,private marker:()=>string,private guard:<T>(value:T,where:import('./outputGuard').OutputWhere)=>T=(v)=>v,private signal:AbortSignal=new AbortController().signal,private outputRefuse:(where:import('./outputGuard').OutputWhere,pattern:string)=>never=(_w,p)=>{throw new OutputRefused(p)},private write:(patch:CheckpointPatch)=>void=patch=>{mutateCheckpoint(this.state(),patch)}){}
 private serial<T>(work:()=>Promise<T>):Promise<T>{const next=this.queue.then(work);this.queue=next.catch(()=>{});return next}
 async open(accepted:AcceptedDocument[]=[]){
  const checkpoint=this.state().draft
  if(checkpoint){
   for(const row of checkpoint.documents){
    const entry=this.ledger.snapshot().entries.find(e=>e.label===label(row))
    const v=entry?.value
    if(!object(v)||typeof v.text!=='string'||!Array.isArray(v.unknowns)||v.unknowns.some(x=>typeof x!=='string')||!Array.isArray(v.undo)||v.undo.some(u=>!object(u)||typeof u.reference!=='string'||!Number.isSafeInteger(u.line)||Number(u.line)<1)||'sha256:'+await contextDigest(v.text)!==row.digest)throw authoredFailure('stale-state','The saved draft text or undo history is unavailable or differs from its digest.','context')
    try{readProposalDocument(v.text)}catch{throw authoredFailure('stale-state','The saved draft text violates the proposal source numeric or work contract.','context')}
    this.documents.set(row.id,{id:row.id,text:v.text,unknowns:v.unknowns as string[],undo:v.undo as Held['undo'],edits:structuredClone(row.edits),digest:row.digest})
   }
  }else for(const doc of accepted){
   if(!doc.id||[...doc.id].length>512||this.documents.has('draft:'+doc.id))throw authoredFailure('configuration','Accepted draft document ids must be unique and nonempty.','context')
   await this.seed({document:doc.document,unknowns:doc.unknowns??[]},doc.id)
  }
  this.ledger.drafts=()=>[...this.documents.values()].map(d=>({id:d.id,label:d.id,value:d.text}))
 }
 private async save(d:Held){
  if([...this.documents.values()].filter(v=>v.id!==d.id).reduce((n,v)=>n+utf8Bytes(v.text),utf8Bytes(d.text))>this.policy.proposalBytes)throw toolInputFailure(`The draft exceeds proposalBytes (${this.policy.proposalBytes}).`)
  d.digest='sha256:'+await contextDigest(d.text)
  await this.ledger.retain({id:'proposal-state:'+await contextDigest(label(d)),kind:'artifact',label:label(d),value:{text:d.text,unknowns:d.unknowns,undo:d.undo}})
  this.documents.set(d.id,d)
  this.write({draft:{documents:[...this.documents.values()].map(v=>({id:v.id,digest:v.digest,edits:structuredClone(v.edits)}))}})
 }
 async seed(proposal:Proposal,id?:string):Promise<Proposal>{return this.serial(async()=>{
  if(id===undefined){
   const clean=this.guard(proposal,'proposal')
   if(JSON.stringify(clean)!==JSON.stringify(proposal)){
    let checked:McpToolResult
    try{checked=await this.validate(JSON.stringify(clean.document),this.signal)}catch{this.signal.throwIfAborted();this.outputRefuse('proposal','proposal-validation')}
    let result=checked.structuredContent
    if(result===undefined){try{result=JSON.parse((checked.content??[]).filter(p=>p.type==='text').map(p=>p.text??'').join('\n'))}catch{}}
    if(checked.isError||!object(result)||result.status!=='valid')this.outputRefuse('proposal','proposal-validation')
   }
   proposal=clean
  }
  const doc=proposal.document as Record<string,unknown>
  const key='draft:'+(id??(typeof doc.id==='string'&&doc.id?doc.id:'document'))
  if([...key].length>518)throw toolInputFailure('The draft document id exceeds 512 characters.')
  const old=this.documents.get(key)
  const text=JSON.stringify(doc,null,2)
  try{readProposalDocument(text)}catch(cause){throw toolInputFailure(cause instanceof ProposalDocumentError?cause.message:'The proposal document is not valid JSON.')}
  if(old&&JSON.stringify(JSON.parse(old.text))===JSON.stringify(doc))return this.proposal(key)!
  if(old)throw toolInputFailure('This document already has a draft; use the edit tools to change it.')
  await this.save({id:key,text,unknowns:[...proposal.unknowns],undo:[],edits:[],digest:''})
  return this.proposal(key)!
 })}
 proposal(id?:string):Proposal|null {
  const d=id?this.documents.get(id):[...this.documents.values()].at(-1)
  return d?{document:JSON.parse(d.text),unknowns:[...d.unknowns],edits:structuredClone(d.edits)}:null
 }
 proposals():Proposal[]{return [...this.documents.keys()].map(id=>this.proposal(id)!)}
 section(){return this.documents.size?'PROPOSAL DRAFTS (package-owned data; read with search and read_window; edit with the reserved edit tools; the person accepts each whole document)\n'+frameBlock('proposal drafts',[...this.documents.values()].map(d=>d.id+'\n'+d.text).join('\n\n'),this.marker()):''}
 refuse(type:string,document:string,reason:string):never{
  this.notify(editRefusal(document,reason))
  this.notify({type:'guardrail',tool:type,action:'refused',reason:'edit refused',detail:reason})
  throw toolInputFailure(reason)
 }
 edit(type:EditType,args:Record<string,unknown>,signal:AbortSignal):Promise<McpToolResult>{return this.serial(async()=>{
  const document=typeof args.document==='string'?args.document:''
  const refuse=(why:string):never=>this.refuse(type,document,why)
  if(!this.allowed())return refuse('Only the author may edit a proposal draft; a child and the critic never edit.')
  if(Object.keys(args).some(k=>!fields[type].includes(k))||fields[type].some(k=>typeof args[k]!=='string')||!document||[...document].length>512)return refuse('Invalid edit arguments; insertion is anchor-only.')
  const original=this.documents.get('draft:'+document)
  if(!original)return refuse('No draft exists for this document; propose_pack must open it first.')
  if([...this.documents.values()].reduce((n,d)=>n+d.edits.length,0)>=this.policy.editsPerTurn)return refuse(`The response reached editsPerTurn (${this.policy.editsPerTurn}).`)
  const next=structuredClone(original)
  let line=1,bytes=0
  if(type==='undo_edit'){
   const last=next.undo.pop()
   if(!last)return refuse('There is no edit to undo.')
   const prior=this.ledger.snapshot().entries.find(e=>e.label===last.reference)?.value
   if(!object(prior)||typeof prior.text!=='string')return refuse('The previous draft revision is unavailable.')
   next.text=prior.text;line=last.line
  }else{
   const anchor=String(type==='propose_insert'?args.after_anchor:args.old_text)
   if(!anchor)return refuse('The anchor must be nonempty.')
   let count=0,at=-1
   for(let pos=original.text.indexOf(anchor);pos!==-1;pos=original.text.indexOf(anchor,pos+1)){count++;at=pos}
   if(count!==1)return refuse(`The anchor occurs ${count} times; it must occur exactly once.`)
   const inserted=type==='propose_delete'?'':String(type==='propose_insert'?args.text:args.new_text)
   bytes=utf8Bytes(inserted)
   if(bytes>this.policy.editBytes)return refuse(`The new text exceeds editBytes (${this.policy.editBytes}).`)
   line=original.text.slice(0,at).split('\n').length
   next.undo.push({reference:label(original),line})
   const start=type==='propose_insert'?at+anchor.length:at
   next.text=original.text.slice(0,start)+inserted+original.text.slice(at+anchor.length)
  }
  if([...this.documents.values()].filter(d=>d.id!==next.id).reduce((n,d)=>n+utf8Bytes(d.text),utf8Bytes(next.text))>this.policy.proposalBytes)return refuse(`The draft exceeds proposalBytes (${this.policy.proposalBytes}).`)
  let parsed:unknown
  try{parsed=readProposalDocument(next.text)}catch(cause){return refuse('Validation failed: '+(cause instanceof ProposalDocumentError?cause.message:'the document is not valid JSON.'))}
  if(!object(parsed))return refuse('Validation failed: the document must be a JSON object.')
  const guarded=this.guard(parsed,'draft edit')
  const redacted=JSON.stringify(guarded)!==JSON.stringify(parsed)
  if(redacted)next.text=JSON.stringify(guarded,null,2)
  signal.throwIfAborted()
  let result:McpToolResult
  try{result=await this.validate(next.text,signal)}catch(error){signal.throwIfAborted();if(redacted)this.outputRefuse('draft edit','proposal-validation');return refuse('Validation failed: '+(error as Error).message)}
  signal.throwIfAborted()
  let structured=result.structuredContent
  if(structured===undefined){try{structured=JSON.parse((result.content??[]).filter(p=>p.type==='text').map(p=>p.text??'').join('\n'))}catch{}}
  if(result.isError||!object(structured)||structured.status!=='valid'){if(redacted)this.outputRefuse('draft edit','proposal-validation');return refuse('Validation failed: '+JSON.stringify(structured??result).slice(0,1000))}
  next.edits.push({type,document,line,bytes,valid:true})
  try{await this.save(next)}catch(error){return refuse((error as Error).message)}
  this.notify({type:'edit_applied',document,line,bytes,valid:true})
  return answer({type:'edit_applied',document,line,bytes,valid:true})
 })}
 tools():HostTool[]{return EDIT_TOOLS.map(type=>({name:type,effect:'proposal',description:'Edit only the package draft; the anchor must occur exactly once. Every candidate is validated; refusal leaves the draft unchanged. The person accepts the whole document.',inputSchema:editSchema(type),execute:(args,signal)=>this.edit(type,args,signal)}))}
}
