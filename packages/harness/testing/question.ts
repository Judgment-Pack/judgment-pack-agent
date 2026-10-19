/**
 * The interview question the engine tests ask, read from the contract's vector
 * (`contract/vectors/question/interview.json`), which carries the Desk's own
 * fixture (`web/src/testing/question.ts`) at the pinned commit.
 */
import type { UserQuestion } from '../questions'
import vector from '../../../contract/vectors/question/interview.json'

export const interviewQuestion: UserQuestion = vector.items[0] as UserQuestion
