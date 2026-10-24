import type {Memory} from '../memories'
/** Hand-authored typed inputs; hashing uses the platform, independently of the harness digest helper. */
export async function typedMemory(text: string): Promise<Memory[]> {
  const digest = [...new Uint8Array(await crypto.subtle.digest('SHA-256',new TextEncoder().encode(text)))].map(byte=>byte.toString(16).padStart(2,'0')).join('')
  return [{id:'legacy-memory',kind:'fact',text,source:{run:'host',proposedBy:'host'},scope:'project',digest:`sha256:${digest}`,createdAt:'1970-01-01T00:00:00Z',state:'accepted'}]
}
