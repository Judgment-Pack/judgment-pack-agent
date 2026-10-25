import {canonicalJSON} from '../context/chat'
import type {Chat,LegacyChatStore} from '../host'
import {ChatRevisionConflict,ChatNotFound} from './common'
import {MemoryChatStore,type ChatStore} from './chat'
/** Explicit single-writer migration only. External writers MUST be stopped. The legacy callback
 * cannot implement cross-process CAS or atomic durable listing/update; do not certify it as a kind. */
export function legacyChatStoreAdapter(legacy:LegacyChatStore):ChatStore {
 const snapshot=legacy.getSnapshot(),memory=new MemoryChatStore([...snapshot.chats.map(chat=>({chat})),...snapshot.drafts.map(chat=>({chat,kind:'draft' as const}))])
 let tail:Promise<unknown>=Promise.resolve()
 return {consistency:'single-writer-legacy',get:memory.get.bind(memory),list:memory.list.bind(memory),update(id,patch,expectedRevision,signal){
  const frozen=structuredClone(patch)
  const next=tail.then(async()=>{
   signal?.throwIfAborted();const old=await memory.get(id,signal),current=legacy.getSnapshot(),held=[...current.chats,...current.drafts].find(chat=>chat.id===id)
   if(old&&held&&canonicalJSON(old.chat)!==canonicalJSON(held))throw new ChatRevisionConflict(expectedRevision,old.revision)
   if(!old||old.revision!==expectedRevision)return memory.update(id,frozen,expectedRevision,signal)
   if(!held)throw new ChatNotFound()
   const trial=new MemoryChatStore([{chat:old.chat,kind:old.kind}]),candidate=(await trial.get(id))!
   await trial.update(id,frozen,candidate.revision,signal)
   legacy.update(id,{...(frozen.interaction===undefined?{}:{interaction:frozen.interaction}),...(frozen.plans===undefined?{}:{plans:frozen.plans as Chat['plans']})})
   try{await legacy.checkpoint(signal)}catch(error){legacy.update(id,{interaction:old.chat.interaction,plans:old.chat.plans as Chat['plans']});throw error}
   return memory.update(id,frozen,expectedRevision,signal)
  });tail=next.catch(()=>undefined);return next
 }}
}
