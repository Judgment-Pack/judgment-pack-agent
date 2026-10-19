import {afterEach,expect,it,vi} from 'vitest'
import {projectMemoryTools} from './tools'
import {memoryRequest} from './client'
vi.mock('./client',async()=>{const original=await vi.importActual<typeof import('./client')>('./client');return {...original,memoryRequest:vi.fn()}})
afterEach(()=>{vi.clearAllMocks()})
it('binds source to the current user message and saves it before the AI suggestion',async()=>{
 const order:string[]=[]
 vi.mocked(memoryRequest).mockImplementation(async()=>{order.push('request');return {saved:true,reviewRequired:true} as never})
 const tools=projectMemoryTools(()=>({chatId:'own-chat',turnId:'user-turn'}),async()=>{order.push('checkpoint')},()=>{order.push('changed')})
 await tools[1]!.execute({key:'preference',text:'Concise answers',source:{chatId:'forged'}},new AbortController().signal)
 expect(order).toEqual(['checkpoint','request','changed'])
 expect(memoryRequest).toHaveBeenCalledWith({action:'suggest',key:'preference',text:'Concise answers',source:{chatId:'own-chat',turnId:'user-turn'}},undefined,'/suggest',expect.any(AbortSignal))
 expect(tools[1]!.delegation).toBeUndefined()
 expect(tools[1]!.effect).toBe('memory')
})
