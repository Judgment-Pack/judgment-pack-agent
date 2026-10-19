import {sourceMessage} from './host'

// Persist only these Desk-owned reasons, never raw callback errors or arguments.
export type QuestionFailureCode = 'shape' | 'title' | 'question' | 'selection' | 'options' | 'recommendation' | 'unavailable'
export function questionFailureMessage(code: unknown): string | undefined {
 switch(code) {
  case 'shape': return sourceMessage('The question format was invalid. Use only title, question, selection, options, recommendedOptionId and recommendation.')
  case 'title': return sourceMessage('Use a question title between 1 and 40 characters.')
  case 'question': return sourceMessage('Use one question between 1 and 600 characters.')
  case 'selection': return sourceMessage('Set selection to single or multiple.')
  case 'options': return sourceMessage('Use two to four distinct choices, or none for free text. Each choice needs a unique ID (letters, numbers, hyphens or underscores), a label up to 100 characters, and an optional description up to 240 characters.')
  case 'recommendation': return sourceMessage('A recommendation needs an existing choice ID and a reason up to 240 characters. Otherwise leave both recommendation fields empty.')
  case 'unavailable': return sourceMessage('This chat could not accept the question. You can answer in the message field.')
 }
}
export class QuestionFormatError extends Error {
 constructor(readonly code: QuestionFailureCode) { super(questionFailureMessage(code)) }
}
export function questionFailureResult(cause: unknown) {
 const code: QuestionFailureCode = cause instanceof QuestionFormatError ? cause.code : 'unavailable'
 const correction = cause instanceof QuestionFormatError ? ' Correct these arguments and call ask_user again in this turn. Do not claim the interface is unavailable, choose an answer, or perform unrelated work.' : ''
 return {isError:true as const,content:[{type:'text' as const,text:questionFailureMessage(code)!+correction}],structuredContent:{questionFailure:code}}
}
