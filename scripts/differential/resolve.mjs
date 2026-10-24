// Resolve the test bundle's external dependencies from the checkout, not its disposable directory.
import { register } from 'node:module'
register(new URL('./resolve-hook.mjs', import.meta.url))
