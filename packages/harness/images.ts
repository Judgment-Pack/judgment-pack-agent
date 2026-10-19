/**
 * Images in tool results and attachments (agent#5).
 *
 * The contract's image part is `{ kind: 'image', mediaType, bytes }` (base64)
 * or `{ kind: 'image', mediaType, reference }` (an image the execution retains,
 * by the id of the attachment or ledger item that holds it). It travels in a
 * tool result's `content`, beside its text parts, and as an attachment's value.
 * The MCP form of an image content item, `{ type: 'image', data, mimeType }`, is
 * read as the same part.
 *
 * **What the model is shown depends on one flag of its capability record**,
 * `vision`, and an image is never dropped in silence:
 *
 * - a model whose record says `vision` is shown the image as an image part, in
 *   the message after the result, and the result's text says it is there;
 * - any other model is shown the host's text rendering of it in its place
 *   (`renderImageToText`, the host's render adapter), headed as a rendering;
 * - an image that cannot be shown (malformed, too large, a reference to nothing
 *   retained, a rendering that failed) is a line in the result saying so.
 *
 * The image's bytes are never part of the result text, so a result with an
 * image in it stays within `toolResultBytes` like any other.
 */
import { renderImageToText } from './host'
import type { McpToolResult } from './engine'

export type ImageMediaType = 'image/png' | 'image/jpeg' | 'image/webp'
export const IMAGE_MEDIA_TYPES: readonly ImageMediaType[] = ['image/png', 'image/jpeg', 'image/webp']

/**
 * The most one image may hold, decoded: the Desk's bound on an attached
 * document's original (`internal/desk/attachments.go`, 16 MiB). An image the
 * ledger retains also counts against `retainedBytes`, as every item does.
 */
export const IMAGE_BYTES = 16 << 20
/** The longest reference an image part may carry. */
export const IMAGE_REFERENCE_LENGTH = 512

/** One image, as the contract states it. */
export interface ImagePart {
  kind: 'image'
  mediaType: ImageMediaType
  /** The image, base64. Either this or `reference`. */
  bytes?: string
  /** A retained image, by its attachment or ledger id. Either this or `bytes`. */
  reference?: string
}
/** An image whose bytes are in hand. */
export type HeldImage = ImagePart & { bytes: string }

/** Base64 characters, then at most two of padding: one character class, so the check is linear on any length. */
const BASE64 = /^[A-Za-z0-9+/]*={0,2}$/
const object = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value)

/** How many bytes a base64 text decodes to. */
export function decodedBytes(base64: string): number {
  return (base64.length / 4) * 3 - (base64.endsWith('==') ? 2 : base64.endsWith('=') ? 1 : 0)
}

/**
 * One value read as an image part: the part, a sentence saying why a value that
 * is meant as an image cannot be one, or null where it is not meant as one. The
 * contract's part is read closed; the MCP form is admitted beside it.
 */
export function readImagePart(value: unknown): { image: ImagePart } | { problem: string; mediaType?: string } | null {
  if (!object(value)) return null
  let part: Record<string, unknown>
  if (value.kind === 'image') {
    const extra = Object.keys(value).find((key) => !['kind', 'mediaType', 'bytes', 'reference'].includes(key))
    if (extra !== undefined) return { problem: `an image part does not carry ${JSON.stringify(extra)}` }
    part = value
  } else if (value.type === 'image') {
    part = { kind: 'image', mediaType: value.mimeType, bytes: value.data }
  } else return null
  const mediaType = part.mediaType
  if (!IMAGE_MEDIA_TYPES.includes(mediaType as ImageMediaType)) {
    return { problem: `an image must be ${IMAGE_MEDIA_TYPES.join(', ')}`, ...(typeof mediaType === 'string' ? { mediaType: mediaType.slice(0, 64) } : {}) }
  }
  const hasBytes = part.bytes !== undefined
  const hasReference = part.reference !== undefined
  if (hasBytes === hasReference) return { problem: 'an image carries its bytes or a reference, one of the two', mediaType: mediaType as string }
  if (hasReference) {
    if (typeof part.reference !== 'string' || !part.reference || part.reference.length > IMAGE_REFERENCE_LENGTH) {
      return { problem: `an image reference is a non-empty string of at most ${IMAGE_REFERENCE_LENGTH} characters`, mediaType: mediaType as string }
    }
    return { image: { kind: 'image', mediaType: mediaType as ImageMediaType, reference: part.reference } }
  }
  if (typeof part.bytes !== 'string' || !part.bytes || part.bytes.length % 4 !== 0 || !BASE64.test(part.bytes)) return { problem: 'an image’s bytes are base64', mediaType: mediaType as string }
  if (decodedBytes(part.bytes) > IMAGE_BYTES) return { problem: `an image holds at most ${IMAGE_BYTES} bytes`, mediaType: mediaType as string }
  return { image: { kind: 'image', mediaType: mediaType as ImageMediaType, bytes: part.bytes } }
}

/** How a run shows images: the model's `vision` flag, and the retained images a reference may name. */
export interface ImageRouting {
  vision: boolean
  /** A retained image by its reference, or null where the execution holds none by it. */
  resolve(reference: string): HeldImage | null
  signal: AbortSignal
}

/**
 * One tool result with its images taken out of its content and a line in each
 * one's place, and the images a vision model is to be shown after it. A result
 * that carries no image is returned as it is, the same object.
 */
export async function separateImages(result: McpToolResult, routing: ImageRouting): Promise<{ result: McpToolResult; images: HeldImage[] }> {
  const content = result.content ?? []
  const read = content.map((item) => readImagePart(item))
  if (read.every((held) => held === null)) return { result, images: [] }
  const images: HeldImage[] = []
  const parts: { type: string; text: string }[] = []
  let number = 0
  for (const [at, item] of content.entries()) {
    const held = read[at]
    if (held === null || held === undefined) {
      parts.push(item as { type: string; text: string })
      continue
    }
    number += 1
    parts.push({ type: 'text', text: await imageLine(number, held, routing, images) })
  }
  return { result: { ...result, content: parts }, images }
}

async function imageLine(number: number, held: { image: ImagePart } | { problem: string; mediaType?: string }, routing: ImageRouting, images: HeldImage[]): Promise<string> {
  if ('problem' in held) return `[image ${number}${held.mediaType ? ` (${held.mediaType})` : ''}: not shown, because ${held.problem}]`
  const part = held.image
  const image = part.bytes !== undefined ? (part as HeldImage) : routing.resolve(part.reference!)
  if (image === null) return `[image ${number} (${part.mediaType}): not shown, because ${JSON.stringify(part.reference)} names no image this execution retains]`
  const size = `${image.mediaType}, ${decodedBytes(image.bytes)} bytes`
  if (routing.vision) {
    images.push(image)
    return `[image ${number} (${size}): shown to you as an image with this result]`
  }
  try {
    routing.signal.throwIfAborted()
    const text = await renderImageToText(image, routing.signal)
    return `[image ${number} (${size}): this model is not shown images; the host's text rendering of it follows]\n${text}`
  } catch (cause) {
    if (routing.signal.aborted) throw cause
    return `[image ${number} (${size}): not shown, because this model is not shown images and the host could not render it as text (${String((cause as Error)?.message ?? cause).slice(0, 200)})]`
  }
}
