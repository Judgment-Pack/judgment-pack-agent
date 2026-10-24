import type {PolicyMembers,PolicyMember,PolicyConnection} from '../policy'

/** The complete admitted member book. There is no raw-session fallback, including for a new key. */
export class RequestValues {
 readonly #members:PolicyMembers
 constructor(members:PolicyMembers,private readonly bindingFor?:(role:string)=>PolicyConnection|undefined){this.#members=structuredClone(members)}
 binding(role:string):PolicyConnection|undefined{return this.bindingFor?.(role)}
 member(name:string):PolicyMember|undefined {const member=this.#members[name];return member===undefined?undefined:structuredClone(member)}
 value(name:string):unknown{return this.member(name)?.value}
 reasoning(role:string):string|undefined {return this.value(`models.${role}.reasoning`) as string|undefined}
}
