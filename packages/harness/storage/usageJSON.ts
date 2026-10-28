/** Strict response JSON with lexical number leaves. Only explicit usage leaves become counts;
 * unrelated tool argument fractions remain valid and are never rounded into usage. */
export class UsageJSONNumber {constructor(readonly text:string){}}
export function readUsageJSON(text:string):unknown {
 let at=0,nodes=0
 const invalid=():never=>{throw Error('Invalid provider usage JSON')}
 const space=()=>{while(/[\x20\t\n\r]/.test(text[at]??'\0'))at++}
 const string=()=>{const start=at++;while(at<text.length){const c=text[at++]!;if(c==='\\')at++;else if(c==='"'){const value:unknown=JSON.parse(text.slice(start,at));if(typeof value!=='string'||/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(value))return invalid();return value}}return invalid()}
 const value=(depth:number):unknown=>{
  if(depth>64||++nodes>4*1024*1024)return invalid()
  space();const c=text[at]
  if(c==='"')return string()
  if(c==='{'||c==='['){
   at++;space();const array=c==='[',end=array?']':'}',out:unknown[]|Record<string,unknown>=array?[]:Object.create(null)
   if(text[at]===end){at++;return out}
   for(;;){space();let key='';if(!array){if(text[at]!=='"')return invalid();key=string();if(Object.hasOwn(out,key))return invalid();space();if(text[at++]!==':')return invalid()}
    const next=value(depth+1);if(array)(out as unknown[]).push(next);else(out as Record<string,unknown>)[key]=next
    space();const token=text[at++];if(token===end)return out;if(token!==',')return invalid()
   }
  }
  for(const [token,result] of [['true',true],['false',false],['null',null]] as const)if(text.startsWith(token,at)){at+=token.length;return result}
  const match=/^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?/.exec(text.slice(at));if(!match)return invalid();at+=match[0].length;return new UsageJSONNumber(match[0])
 }
 const out=value(0);space();if(at!==text.length)return invalid();return out
}
/** Check decimal value before Number can hide a fractional or unsafe provider figure. */
export function usageJSONCount(value:unknown):number {
 if(!(value instanceof UsageJSONNumber))throw Error('Provider usage is not an explicit number')
 const parts=/^(-?)([0-9]+)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?$/.exec(value.text)!
 let digits=(parts[2]!+(parts[3]??'')).replace(/^0+/,'');if(!digits)return 0
 let scale=Number(parts[4]??0)-(parts[3]?.length??0)
 while(digits.endsWith('0')){digits=digits.slice(0,-1);scale++}
 if(parts[1]||!Number.isSafeInteger(scale)||scale<0||digits.length+scale>16)throw Error('Provider usage is not a nonnegative safe integer')
 const count=BigInt(digits)*10n**BigInt(scale);if(count>BigInt(Number.MAX_SAFE_INTEGER))throw Error('Unsafe provider usage')
 return Number(count)
}
