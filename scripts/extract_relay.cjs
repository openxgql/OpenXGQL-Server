const fs=require('fs'),path=require('path');
const acorn=require('acorn');
const walk=require('acorn-walk');
const root=process.argv[2];
if(!root){console.error('Usage: node scripts/extract_relay.cjs /path/to/unmodified/x-web/x-web [output.json]');process.exit(1)}
const out=[];
function files(dir){return fs.readdirSync(dir,{withFileTypes:true}).flatMap(e=>e.isDirectory()?files(path.join(dir,e.name)):e.name.endsWith('.js')?[path.join(dir,e.name)]:[])}
for(const file of files(root)){
 const src=fs.readFileSync(file,'utf8');if(!src.includes('operationKind:'))continue;
 let ast;try{ast=acorn.parse(src,{ecmaVersion:'latest',sourceType:'module'})}catch{continue}
 walk.ancestor(ast,{ObjectExpression(node,parents){
  const props=Object.fromEntries(node.properties.filter(p=>p.type==='Property').map(p=>[p.key.name||p.key.value,p.value]));
  if(!props.params||!props.operation)return;
  let env=new Map();
  for(const parent of parents){
   if(parent.type==='Program'||parent.type==='BlockStatement')for(const st of parent.body||[])if(st.type==='VariableDeclaration')for(const d of st.declarations)if(d.id.type==='Identifier')env.set(d.id.name,d.init);
  }
  function val(n,seen=new Set()){
   if(!n)return null;
   if(n.type==='Literal')return n.value;
   if(n.type==='TemplateLiteral'&&n.expressions.length===0)return n.quasis[0].value.cooked;
   if(n.type==='Identifier'){if(seen.has(n.name))return null;return val(env.get(n.name),new Set([...seen,n.name]))}
   if(n.type==='ArrayExpression')return n.elements.map(x=>val(x,seen));
   if(n.type==='ObjectExpression')return Object.fromEntries(n.properties.filter(p=>p.type==='Property').map(p=>[p.key.name||p.key.value,val(p.value,seen)]));
   if(n.type==='UnaryExpression'&&n.operator==='!')return !val(n.argument,seen);
   return null;
  }
  const params=val(props.params);if(!params?.id||!params?.name)return;
  const op=val(props.operation);
  function fields(nodes){return(nodes||[]).flatMap(f=>!f?[]:f.kind==='LinkedField'||f.kind==='ScalarField'?[{name:f.name,alias:f.alias,kind:f.kind,type:f.concreteType,plural:f.plural,selections:fields(f.selections)}]:fields(f.selections));}
  out.push({name:params.name,id:params.id,kind:params.operationKind,file:path.basename(file),arguments:op?.argumentDefinitions||[],selections:fields(op?.selections)});
 }});
}
const unique=[...new Map(out.map(x=>[x.name+':'+x.id,x])).values()];
fs.writeFileSync(process.argv[3]||'reference/relay-schema.json',JSON.stringify(unique));
console.log('Relay schemas',unique.length);
for(const name of ['bookmarksQuery','notificationsQuery','blockUserMutation','listCreateSlotMutation','historyClearMutation','pinTweetMutation','communityJoinButtonJoinMutation']){const x=unique.find(x=>x.name===name);console.log(name,JSON.stringify(x?.selections||[]).slice(0,500))}
