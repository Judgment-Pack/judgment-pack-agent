/** Numeric admission for proposal source text. This is not acceptance or output-guard authority. */
export const PROPOSAL_SOURCE_BYTES = 4_194_304
export const PROPOSAL_SOURCE_DEPTH = 128
export const PROPOSAL_EXPONENT_DIGITS = 7
export class ProposalDocumentError extends Error {
 constructor(readonly kind:'invalid-json'|'number-loss'|'work-bound'|'not-object',message:string){super(message);this.name='ProposalDocumentError'}
}
const invalid=()=>new ProposalDocumentError('invalid-json','The proposal document is not valid JSON.')
const bound=()=>new ProposalDocumentError('work-bound','The proposal source exceeds its byte, depth or exponent work bound.')
const loss=()=>new ProposalDocumentError('number-loss','The proposal contains a number whose numerical value changes during canonical JSON serialization.')

/** Decimal identity without powers, expanded exponent zeroes or arbitrary-precision arithmetic. */
function decimal(token:string):string {
 const negative=token.startsWith('-')
 const unsigned=negative?token.slice(1):token
 const at=unsigned.search(/[eE]/)
 const mantissa=at<0?unsigned:unsigned.slice(0,at)
 const exponent=at<0?'0':unsigned.slice(at+1)
 const exponentDigits=exponent.replace(/^[+-]/,'').replace(/^0+/,'')
 // Seven significant exponent digits cover cancellation by every coefficient in 4 MiB.
 if(exponentDigits.length>PROPOSAL_EXPONENT_DIGITS)throw bound()
 const dot=mantissa.indexOf('.')
 let digits=mantissa.replace('.','').replace(/^0+/,'')
 if(!digits)return '0'
 const stripped=digits.replace(/0+$/,'')
 const power=Number(exponent)-(dot<0?0:mantissa.length-dot-1)+digits.length-stripped.length
 digits=stripped
 return `${negative?'-':''}${digits}e${power}`
}

/**
 * Validate source numbers in one subtree, then return JSON.parse's ordinary value.
 * Every matching subtree occurrence is checked, including duplicate keys; parsed objects still
 * keep the existing last-member-wins and member enumeration semantics.
 */
export function parseProposalJSON(text:string,path:readonly string[]=[]):unknown {
 if(typeof text!=='string')throw invalid()
 if(text.length>PROPOSAL_SOURCE_BYTES||new TextEncoder().encode(text).length>PROPOSAL_SOURCE_BYTES)throw bound()
 let parsed:unknown
 try{parsed=JSON.parse(text)}catch{throw invalid()}
 let at=0
 const space=()=>{while(at<text.length&&/[\t\n\r ]/.test(text[at]!))at++}
 const string=():string=>{
  const start=at++
  while(at<text.length){const c=text[at++]!;if(c==='\\')at++;else if(c==='"')break}
  return text.slice(start,at)
 }
 const walk=(depth:number,matched:number,checking:boolean):void=>{
  space()
  const c=text[at]!
  if(c==='{'||c==='['){
   if(depth>=PROPOSAL_SOURCE_DEPTH)throw bound()
   at++;space()
   const end=c==='{'?'}':']'
   if(text[at]===end){at++;return}
   for(;;){
    let next=-1
    if(c==='{'){
     const key=JSON.parse(string()) as string
     space();at++
     if(matched>=0&&key===path[matched])next=matched+1
    }
    walk(depth+1,next,checking||next===path.length)
    space()
    if(text[at++]===end)return
    space()
   }
  }
  if(c==='"'){string();return}
  const start=at
  while(at<text.length&&!/[\t\n\r ,}\]]/.test(text[at]!))at++
  if(checking&&(c==='-'||c>='0'&&c<='9')){
   const token=text.slice(start,at),source=decimal(token),number=Number(token)
   if(!Number.isFinite(number)||source!==decimal(JSON.stringify(number)))throw loss()
  }
 }
 walk(0,0,path.length===0)
 return parsed
}

/**
 * Read a document only when canonical JSON preserves every source numeric value. Decimal/exponent
 * spelling and negative zero may normalize. This does not guard output, accept a proposal, or
 * recover source precision from an already parsed object; the accepted proposal event is authority.
 */
export function readProposalDocument(text:string):Record<string,unknown>{
 const value=parseProposalJSON(text)
 if(!value||typeof value!=='object'||Array.isArray(value))throw new ProposalDocumentError('not-object','The proposal must contain a document object.')
 return value as Record<string,unknown>
}
