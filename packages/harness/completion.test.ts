import {it,expect} from 'vitest'
import {canFinishResponse,modelCompletion,readCompletion} from './completion'
import {readFailure,failure} from './failures'
it('keeps normalized finish reasons and finite counters without provider prose or private reasoning',()=>{
 expect(modelCompletion('length',{inputTokens:20,outputTokens:100,outputTokenDetails:{reasoningTokens:90},raw:'PRIVATE'})).toEqual({finishReason:'length',inputTokens:20,outputTokens:100,reasoningTokens:90})
 expect(modelCompletion('PRIVATE',{inputTokens:-1,outputTokens:NaN})).toEqual({finishReason:'other'})
 expect(readCompletion({finishReason:'stop',outputTokens:0,raw:'PRIVATE'})).toEqual({finishReason:'stop',outputTokens:0})
 expect(readCompletion({finishReason:'stop',reasoningTokens:-1})).toBeUndefined()
 const safe=readFailure({...failure('output-limit','model'),completion:{finishReason:'length',outputTokens:8192,raw:'PRIVATE'}})
 expect(safe?.completion).toEqual({finishReason:'length',outputTokens:8192})
 expect(readFailure({...failure('output-limit'),completion:{finishReason:'PRIVATE'}})).toBeUndefined()
})
it('only finishes a known empty or truncated response once, within the existing allowance and without pending tools',()=>{
 expect(canFinishResponse({finishReason:'stop'},'',0,1,false)).toBe(true)
 expect(canFinishResponse({finishReason:'length'},'partial',0,1,false)).toBe(true)
 for(const reason of ['other','error','content-filter','tool-calls'] as const)expect(canFinishResponse({finishReason:reason},'',0,1,false)).toBe(false)
 expect(canFinishResponse(undefined,'',0,1,false)).toBe(false)
 expect(canFinishResponse({finishReason:'stop'},'complete',0,1,false)).toBe(false)
 expect(canFinishResponse({finishReason:'length'},'',1,1,false)).toBe(false)
 expect(canFinishResponse({finishReason:'stop'},'',0,0,false)).toBe(false)
 expect(canFinishResponse({finishReason:'stop'},'',0,1,true)).toBe(false)
})
