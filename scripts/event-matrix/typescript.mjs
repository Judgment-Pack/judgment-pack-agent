// Trace syntax and compiler-resolved bindings into event-typed publication calls and yields.
// Merely mentioning a kind, declaring a union, or constructing an unused object is not emission.
import {API,SymbolFlags,TypeFlags} from 'typescript/unstable/sync'
import * as ts from 'typescript/unstable/ast'
import {readdirSync} from 'node:fs'
import {join,relative} from 'node:path'

export function sourceFiles(root, extension) {
 const out=[]
 function walk(dir){for(const entry of readdirSync(dir,{withFileTypes:true}).sort((a,b)=>a.name.localeCompare(b.name))){
  if(['node_modules','dist','certification','testing','scripts','examples','spike'].includes(entry.name))continue
  const path=join(dir,entry.name)
  if(entry.isDirectory())walk(path)
  else if(path.endsWith(extension)&&!/(\.test\.ts|\.d\.ts|_test\.go)$/.test(path))out.push(path)
 }}
 walk(root);return out
}
export function analyzeTypeScript(root,kinds){
 const files=sourceFiles(join(root,'packages/harness'),'.ts')
 const api=new API({cwd:root})
 const snapshot=api.updateSnapshot({openProjects:[join(root,'packages/harness/tsconfig.json')]})
 const project=snapshot.getProjects()[0],program=project.program,checker=project.checker,known=new Set(kinds),emits=[],reads=[]
 try{
 const visit=(node,fn)=>{fn(node);node.forEachChild(n=>visit(n,fn))}
 const literal=n=>{while(n&&(ts.isAsExpression(n)||ts.isParenthesizedExpression(n)||ts.isNonNullExpression(n)))n=n.expression;return n&&ts.isStringLiteral(n)?n.text:undefined}
 function tag(type){
  if(type?.isUnionType()){const members=type.getTypes().filter(t=>!(t.flags&(TypeFlags.Null|TypeFlags.Undefined)));return members.length>0&&members.every(tag)}
  const prop=type&&checker.getPropertyOfType(type,'type');if(!prop)return false
  const value=checker.getTypeOfSymbolAtLocation(prop,(prop.valueDeclaration??prop.declarations?.[0]).resolve())
  const values=value.isUnionType()?value.getTypes():[value]
  return values.length>0&&values.every(v=>v.isStringLiteralType()&&known.has(v.value))
 }
 const assignments=new Map()
 const symbol=n=>checker.getSymbolAtLocation(ts.isPropertyAccessExpression(n)?n.name:n)
 for(const path of files)visit(program.getSourceFile(path),node=>{if(ts.isBinaryExpression(node)&&node.operatorToken.kind===ts.SyntaxKind.EqualsToken){const key=symbol(node.left);if(key){const values=assignments.get(key)??[];values.push(node.right);assignments.set(key,values)}}})
 function returned(decl,fn){
  if(!decl?.body)return
  const walk=n=>{if(n!==decl.body&&(ts.isFunctionDeclaration(n)||ts.isArrowFunction(n)||ts.isFunctionExpression(n)||ts.isMethodDeclaration(n)))return;if(ts.isReturnStatement(n))fn(n.expression);else n.forEachChild(walk)}
  if(ts.isBlock(decl.body))walk(decl.body);else fn(decl.body)
 }
 function member(node,name,seen=new Set()){
  if(!node||seen.has(node))return [];seen.add(node)
  if(ts.isObjectLiteralExpression(node))return node.properties.filter(p=>p.name?.getText()===name)
  if(ts.isIdentifier(node))return declarations(node).flatMap(d=>member(d.initializer,name,seen))
  if(ts.isCallExpression(node)){const out=[];returned(checker.getResolvedSignature(node)?.declaration?.resolve(),n=>out.push(...member(n,name,seen)));return out}
  return []
 }
 const site=(file,node)=>({file:relative(root,file.fileName),line:file.getLineAndCharacterOfPosition(node.getStart(file)).line+1})
 function record(list,kind,node){if(known.has(kind))list.push({kind,...site(node.getSourceFile(),node)})}
 function declarations(n){let symbol=checker.getSymbolAtLocation(n);if(symbol?.flags&SymbolFlags.Alias)symbol=checker.getAliasedSymbol(symbol);return (symbol?.declarations??[]).map(d=>d.resolve()).filter(Boolean)}
 function trace(node,seen=new Set()){
  if(!node||seen.has(node))return;seen.add(node)
  if(ts.isObjectLiteralExpression(node)){
   const type=node.properties.find(p=>ts.isPropertyAssignment(p)&&p.name.getText()==='type')
   if(type)record(emits,literal(type.initializer),type)
   // Public export/record APIs return the event alongside their output.
   for(const p of node.properties)if(ts.isPropertyAssignment(p)&&p.name.getText()==='event')trace(p.initializer,seen)
  }else if(ts.isIdentifier(node)){
   for(const d of declarations(node))if(ts.isVariableDeclaration(d)&&d.initializer)trace(d.initializer,seen)
   for(const value of assignments.get(symbol(node))??[])trace(value,seen)
  }else if(ts.isCallExpression(node)){
   const signature=checker.getResolvedSignature(node),decl=signature?.declaration?.resolve()
   returned(decl,n=>trace(n,seen))
   if(!decl?.body&&ts.isPropertyAccessExpression(node.expression))for(const method of member(node.expression.expression,node.expression.name.text))returned(method,n=>trace(n,seen))
  }else if(ts.isPropertyAccessExpression(node)){
   // Resolve an event-valued property of a locally built result, not its other payloads.
   for(const d of declarations(node.name))if(ts.isPropertyAssignment(d))trace(d.initializer,seen)
   for(const value of assignments.get(symbol(node))??[])trace(value,seen)
   if(node.name.text==='event')trace(node.expression,seen)
  }else if(ts.isConditionalExpression(node)){trace(node.whenTrue,seen);trace(node.whenFalse,seen)}
  else if(ts.isAsExpression(node)||ts.isAwaitExpression(node)||ts.isParenthesizedExpression(node)||ts.isNonNullExpression(node))trace(node.expression,seen)
 }
 for(const path of files){const file=program.getSourceFile(path);visit(file,node=>{
  if(ts.isYieldExpression(node))trace(node.expression)
  if(ts.isCallExpression(node)){
   const signature=checker.getResolvedSignature(node)
   signature?.getParameters().forEach((param,i)=>{
    let type=checker.getTypeOfSymbolAtLocation(param,node)
    if(checker.isArrayType(type))type=checker.getTypeArguments(type)[0]
    if(tag(type))trace(node.arguments[i])
   })
  }
  // Public functions returning a structured result whose event member has an event type.
  if(ts.isReturnStatement(node)&&node.expression&&ts.isObjectLiteralExpression(node.expression)){
   const event=node.expression.properties.find(p=>ts.isPropertyAssignment(p)&&p.name.getText()==='event')
   if(event&&tag(checker.getTypeAtLocation(event.initializer)))trace(event.initializer)
  }
  if(ts.isBinaryExpression(node)&&[ts.SyntaxKind.EqualsEqualsEqualsToken,ts.SyntaxKind.ExclamationEqualsEqualsToken,ts.SyntaxKind.EqualsEqualsToken,ts.SyntaxKind.ExclamationEqualsToken].includes(node.operatorToken.kind)){
   for(const [a,b] of [[node.left,node.right],[node.right,node.left]])if(ts.isPropertyAccessExpression(a)&&a.name.text==='type'&&tag(checker.getTypeAtLocation(a.expression)))record(reads,literal(b),b)
  }
  if(ts.isSwitchStatement(node)&&ts.isPropertyAccessExpression(node.expression)&&node.expression.name.text==='type'&&tag(checker.getTypeAtLocation(node.expression.expression))){for(const c of node.caseBlock.clauses)if(ts.isCaseClause(c))record(reads,literal(c.expression),c.expression)}
 })}
 return {emits,reads}
 }finally{snapshot.dispose();api.close()}
}
