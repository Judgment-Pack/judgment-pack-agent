import { createRequire } from 'node:module'
import { pathToFileURL } from 'node:url'
const require = createRequire(import.meta.url)
export async function resolve(specifier, context, nextResolve) {
  if (!specifier.startsWith('.') && !specifier.startsWith('/') && !specifier.startsWith('node:') && !specifier.includes(':')) {
    return nextResolve(pathToFileURL(require.resolve(specifier)).href, context)
  }
  return nextResolve(specifier, context)
}
