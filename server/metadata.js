// Optional project metadata lives in one Go file's leading comments.
// Only comments before "package main" are inspected: strings and function bodies
// cannot accidentally change the build entry or the protocol description.
function header(source) {
  let rest=source.replace(/^\uFEFF/,'');
  const comments=[];
  for(;;){
    rest=rest.trimStart();
    if(rest.startsWith('//')){
      const end=rest.indexOf('\n');
      comments.push(rest.slice(2,end<0?undefined:end));
      rest=end<0?'':rest.slice(end+1);
    }else if(rest.startsWith('/*')){
      const end=rest.indexOf('*/',2);
      if(end<0)throw new Error('Go 文件头注释未闭合');
      comments.push(rest.slice(2,end));rest=rest.slice(end+2);
    }else break;
  }
  return /^\s*package\s+main\b/.test(rest)?comments:null;
}

export function protocolMetadata(files, legacy={}, explicitEntry) {
  if(!legacy || typeof legacy!=='object' || Array.isArray(legacy))throw new Error('协议说明应为对象');
  const annotations={}, mainDirs=new Set();
  for(const file of files){
    if(!file.path.endsWith('.go') || file.path.endsWith('_test.go'))continue;
    const comments=header(file.content);
    if(!comments)continue;
    const slash=file.path.lastIndexOf('/');
    mainDirs.add(slash<0?'.':'./'+file.path.slice(0,slash));
    for(const block of comments)for(const line of block.split('\n')){
      const text=line.trim().replace(/^\*\s?/,'');
      const match=text.match(/^distvis:(name|description|entry)\s+(.+)$/);
      if(!match){
        if(text.startsWith('distvis:'))throw new Error(`${file.path}: 无效的 distvis 注释（支持 name、description、entry）`);
        continue;
      }
      if(Object.hasOwn(annotations,match[1]))throw new Error(`重复的 distvis:${match[1]}，请集中在一个 Go 文件头声明`);
      annotations[match[1]]=match[2].trim();
    }
  }
  const metadata={...legacy,...annotations};
  for(const [key,limit] of [['name',100],['description',1000]]){
    if(metadata[key]!==undefined && (typeof metadata[key]!=='string' || !metadata[key].trim() || metadata[key].length>limit))throw new Error(`${key} 不合法`);
  }
  const entry=explicitEntry || metadata.entry || (mainDirs.has('.')?'.':mainDirs.has('./cmd/node')?'./cmd/node':mainDirs.size===1?[...mainDirs][0]:'');
  if(typeof entry!=='string' || !/^\.(?:\/[a-zA-Z0-9_.-]+)*$/.test(entry) || entry.split('/').includes('..'))throw new Error('找不到唯一 main 包；请在 Go 文件头用 // distvis:entry ./cmd/node 指定入口');
  if(!mainDirs.has(entry))throw new Error('入口目录没有 package main');
  return {...metadata,entry};
}
