import {QuestionFormatError} from './questionFailure'
import type { HostTool } from './engine'

export interface UserQuestion {
  title: string
  question: string
  selection: 'single' | 'multiple'
  options: { id: string; label: string; description: string }[]
  recommendedOptionId: string | null
  recommendation: string | null
}
export interface QuestionDraft { selected: string[]; text: string }
export interface SavedQuestion {
  id: string
  prompt: UserQuestion
  draft: QuestionDraft
  resolution?: { kind: 'answered' | 'help' | 'superseded'; at: string; turnId: string; continued?: boolean }
}
const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v)
const text = (v: unknown, max: number, empty = false): v is string => typeof v === 'string' && v.length <= max && (empty || !!v.trim())
const id = (v: unknown): v is string => typeof v === 'string' && /^[a-zA-Z0-9_-]{1,100}$/.test(v)
const exact = (v: Record<string, unknown>, names: string[]) => Object.keys(v).every(key => names.includes(key))
export function readUserQuestion(v: unknown): UserQuestion {
  if (!object(v) || !exact(v, ['title','question','selection','options','recommendedOptionId','recommendation'])) throw new QuestionFormatError('shape')
  if (!text(v.title, 40)) throw new QuestionFormatError('title')
  if (!text(v.question, 600)) throw new QuestionFormatError('question')
  if (!['single','multiple'].includes(String(v.selection))) throw new QuestionFormatError('selection')
  if (!Array.isArray(v.options) || v.options.length === 1 || v.options.length > 4
    || v.options.some(o => !object(o) || !exact(o, ['id','label','description']) || !id(o.id) || !text(o.label, 100) || !text(o.description, 240, true))
    || new Set(v.options.map(o => o.id)).size !== v.options.length
    || new Set(v.options.map(o => o.label.trim().toLowerCase())).size !== v.options.length) throw new QuestionFormatError('options')
  if (!(v.recommendedOptionId === null || id(v.recommendedOptionId) && v.options.some(o => o.id === v.recommendedOptionId))
    || !(v.recommendation === null || text(v.recommendation, 240))
    || (v.recommendedOptionId === null) !== (v.recommendation === null)) throw new QuestionFormatError('recommendation')
  return structuredClone(v) as unknown as UserQuestion
}
/** Normalize absent presentation fields at the model boundary; saved history stays strict. */
export function readModelQuestion(value: unknown): UserQuestion {
  if (!object(value)) return readUserQuestion(value)
  const v = structuredClone(value)
  const absent = (value: unknown) => value == null || typeof value === 'string' && !value.trim()
  if (absent(v.recommendedOptionId)) v.recommendedOptionId = null
  if (absent(v.recommendation)) v.recommendation = null
  if (Array.isArray(v.options)) v.options = v.options.map(option => object(option) && option.description == null ? {...option,description:''} : option)
  return readUserQuestion(v)
}
export function validQuestionDraft(prompt: UserQuestion, v: unknown): v is QuestionDraft {
  return object(v) && exact(v, ['selected','text']) && Array.isArray(v.selected)
    && v.selected.every(s => prompt.options.some(o => o.id === s)) && new Set(v.selected).size === v.selected.length
    && (prompt.selection === 'multiple' || v.selected.length <= 1) && text(v.text, 4000, true)
}
export function readSavedQuestions(v: unknown): SavedQuestion[] {
  if (v === undefined) return []
  if (!Array.isArray(v) || v.length > 128) throw Error('Saved questions are invalid. History was left untouched.')
  const items = v.map(value => {
    if (!object(value) || !exact(value, ['id','prompt','draft','resolution']) || !id(value.id)) throw Error('Saved question is invalid.')
    const prompt = readUserQuestion(value.prompt)
    if (!validQuestionDraft(prompt, value.draft)) throw Error('Saved question answer is invalid.')
    const r = value.resolution
    if (r !== undefined && (!object(r) || !exact(r, ['kind','at','turnId','continued']) || !['answered','help','superseded'].includes(String(r.kind)) || !id(r.turnId) || !text(r.at, 64) || !Number.isFinite(Date.parse(r.at)) || r.continued !== undefined && typeof r.continued !== 'boolean'
      || r.kind === 'answered' && !value.draft.selected.length && !value.draft.text.trim())) throw Error('Saved question response is invalid.')
    return structuredClone({ ...value, prompt }) as unknown as SavedQuestion
  })
  if (new Set(items.map(q => q.id)).size !== items.length || items.filter(q => !q.resolution).length > 1) throw Error('Saved questions are inconsistent.')
  return items
}
export const QUESTION_INSTRUCTIONS = 'INTERACTIVE QUESTIONS: When interviewing the person or asking for a preference, use ask_user for one focused question at a time. Offer 2–4 concise, distinct options when useful, with single or multiple selection. Free text is always available. Recommend one option only when context supports it, with a short honest reason; recommendations are never user decisions. Use no options for an open text question. Do not repeat the question in prose. Do not use questions as permission to save recipes, start research, evaluate records or perform other actions; existing review controls still apply. The question ends this model turn and waits for a real answer. On continuation use the saved answer, preserve the original task and constraints, and do not repeat completed tools or earlier proposals. If the person asks for help deciding, briefly explain the tradeoffs, then call ask_user again with the same choices or a more useful follow-up question. A prose-only list is not an interactive question. If ask_user returns a format error, correct the named fields and call it again before falling back to prose. Keep the title at most 40 characters; use distinct short option IDs and labels. Optional description and recommendation fields may be empty; a recommendation must name an offered option and include its reason. Do not choose for them or treat help as an accepted answer.'
const string = (maxLength: number) => ({ type: 'string', maxLength })
export const questionTool: HostTool = {
  name: 'ask_user',taxonomy:{operation:'ask',effect:'pure'}, interaction: 'question',
  description: 'Ask the person one question with suggested choices and optional free text. This pauses the conversation without selecting an answer or approving any action. Use it for interviews and clarification.',
  inputSchema: { type: 'object', additionalProperties: false, required: ['title','question','selection','options'], properties: {
    title: string(40), question: string(600), selection: {type:'string',enum:['single','multiple']},
    options: {type:'array',maxItems:4,items:{type:'object',additionalProperties:false,required:['id','label'],properties:{id:{...string(100),minLength:1,pattern:'^[a-zA-Z0-9_-]+$'},label:string(100),description:{type:['string','null'],maxLength:240,description:'Optional supporting detail; use an empty string or null when unused.'}}}},
    recommendedOptionId: {type:['string','null'],maxLength:100,description:'An offered option ID, not its label. Use null without a recommendation.'}, recommendation:{type:['string','null'],maxLength:240}
  }},
  execute: async () => { throw Error('User questions must be handled by the conversation engine.') }
}
