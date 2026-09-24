import { spawn } from 'node:child_process';
import { mkdir, writeFile, cp } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { createInterface } from 'node:readline';
import { fileURLToPath } from 'node:url';
import { createHash } from 'node:crypto';
import { buildImages, dockerfile, projectDockerfile, compileProject, describeBuildFailure } from './build.js';
import { k8sHostPath } from './library.js';

const root = fileURLToPath(new URL('../', import.meta.url));
// Docker --mount is CSV, even though exec passes the whole option as one argument.
export function bindMount(source, target, readOnly = true) {
  const csv = value => /[",\n]/.test(value) ? `"${value.replaceAll('"', '""')}"` : value;
  return ['type=bind', `src=${source}`, `dst=${target}`, ...(readOnly ? ['readonly'] : [])].map(csv).join(',');
}
export function command(bin, args, options = {}) {
  return new Promise((resolvePromise, reject) => {
    const child = spawn(bin, args, { ...options, stdio: ['pipe', 'pipe', 'pipe'] });
    let output = '';
    const timer = setTimeout(() => child.kill('SIGKILL'), options.timeout || 120000);
    child.stdout.on('data', b => { output = (output + b).slice(-16000); });
    child.stderr.on('data', b => { output = (output + b).slice(-16000); });
    child.on('error', err => { clearTimeout(timer); reject(err); });
    child.stdin.on('error', err => { clearTimeout(timer); reject(err); });
    child.on('close', code => {
      clearTimeout(timer);
      if (code === 0) resolvePromise(output);
      else reject(new Error(`${bin} ${args[0]} 失败 (${code}): ${output}`));
    });
    child.stdin.end(options.input || '');
  });
}

// One executable process per node; stdin/stdout is a JSON transport controlled by the coordinator.
// No node-to-node sockets can bypass the simulated network in Docker (--network none).
export class ContainerRuntime {
  constructor(run, directory, source, nodeSources = {}, project = null) {
    this.run = run;
    this.directory = resolve(directory);
    this.source = source;
    this.nodeSources = nodeSources;
    this.project = project;
    this.goProxy = process.env.DISTVIS_GOPROXY || 'https://proxy.golang.org,direct';
    this.prefix = `distvis-${run.id.slice(0, 8)}`;
    this.image = `${this.prefix}:local`;
    this.sessions = new Map();
    this.resources = [];
    this.expected = new Set();
    this.pending = new Map();
    this.closed = false;
    this.k8s = run.config.runtime === 'kubernetes';
    this.namespace = this.prefix;
    this.onDeliver = message => this.deliver(message);
    this.onCommand = ({ node, ...cmd }) => {
      if (!this.write(node, { type: 'command', ...cmd })) throw new Error('节点会话不可写');
    };
    run.on('deliver', this.onDeliver);
    run.on('command', this.onCommand);
  }
  kubectl(args, options) {
    return command('kubectl', ['--context', 'docker-desktop', ...args], options);
  }
  async start() {
    const context = join(this.directory, 'build');
    await mkdir(join(context, 'sdk'), { recursive: true });
    await cp(join(root, 'sdk'), join(context, 'sdk'), { recursive: true });
    await cp(join(root, 'go.mod'), join(context, 'go.mod'));
    await cp(join(root, 'go.sum'), join(context, 'go.sum'));
    const images = buildImages();
    if (!this.project) {
      for (const node of this.run.nodes) {
        await mkdir(join(context, 'cmd', node), { recursive: true });
        await writeFile(join(context, 'cmd', node, 'main.go'), this.nodeSources[node] || this.source);
      }
    }
    await writeFile(join(context, 'Dockerfile'), this.project ? projectDockerfile(images) : dockerfile(images));
    this.run.log('runtime', {
      execution: 'go',
      sourceSha256: this.run.config.project?.sha256 || createHash('sha256').update(this.source).digest('hex'),
      message: this.project ? `Go 项目：${this.project.name || this.project.directory}\n入口：${this.project.entry}\n源码快照只读挂载到 /protocol` : '节点程序：Go 源码（通过 distvis/sdk 接入 coordinator）',
    });
    this.run.log('runtime', { message: `正在构建 Go 节点镜像…\n编译镜像：${images.go}\n运行镜像：${images.runtime}`, images });
    try {
      await command('docker', ['build', '--progress=plain', '--build-arg', `GOPROXY=${this.goProxy}`, '-t', this.image, context], { timeout: 300000 });
    } catch (error) {
      throw new Error(describeBuildFailure(error), { cause: error });
    }
    if (this.k8s) {
      // Never use the current context: it may point to a production cluster.
      await this.kubectl(['create', 'namespace', this.namespace]);
      this.resources.push(this.namespace);
      const manifests = this.run.nodes.map(node => ({
        apiVersion: 'v1', kind: 'Pod', metadata: { name: node, namespace: this.namespace, labels: { app: this.prefix } },
        spec: {
          automountServiceAccountToken: false, restartPolicy: 'Never',
          terminationGracePeriodSeconds: 5,
          securityContext: { runAsUser: 10001, runAsGroup: 10001, fsGroup: 10001 },
          ...(this.project ? {
            initContainers: [{
              name: 'compile', image: this.image, imagePullPolicy: 'Never',
              command: ['sh','-c',compileProject],
              env: [{ name:'DISTVIS_ENTRY',value:this.project.nodes?.[node]?.entry || this.project.entry },{name:'GOPROXY',value:this.goProxy}],
              securityContext: { runAsUser:0, runAsGroup:0 },
              resources: { requests:{cpu:'100m',memory:'128Mi'},limits:{cpu:'2',memory:'1Gi'} },
              volumeMounts: [{name:'protocol',mountPath:'/protocol',readOnly:true},{name:'app',mountPath:'/app'}],
            }],
          } : {}),
          containers: [{
            name: 'node', image: this.image, imagePullPolicy: 'Never',
            ...(this.project ? {workingDir:'/protocol',env:[{name:'DISTVIS_STATE_DIR',value:'/state'}]} : {}),
            resources: { requests: { cpu: '50m', memory: '32Mi' }, limits: { cpu: '500m', memory: '128Mi' } },
            securityContext: { allowPrivilegeEscalation: false, capabilities: { drop: ['ALL'] } },
            volumeMounts: [{ name: 'state', mountPath: '/state' }, ...(this.project ? [
              {name:'protocol',mountPath:'/protocol',readOnly:true},
              {name:'library',mountPath:'/protocols',readOnly:true},
              {name:'app',mountPath:'/app',readOnly:true},
            ] : [])],
          }],
          volumes: [{ name: 'state', emptyDir: {} }, ...(this.project ? [
            {name:'protocol',hostPath:{path:k8sHostPath(this.project.nodes?.[node]?.snapshot || this.project.snapshot),type:'Directory'}},
            {name:'library',hostPath:{path:k8sHostPath(this.project.root),type:'Directory'}},
            {name:'app',emptyDir:{}},
          ] : [])],
        },
      }));
      await this.kubectl(['apply', '-f', '-'], { input: JSON.stringify({ apiVersion: 'v1', kind: 'List', items: manifests }) });
      try {
        await this.kubectl(['wait', '-n', this.namespace, '--for=condition=Ready', 'pod', '--all', '--timeout=180s'], {timeout:190000});
      } catch (error) {
        const diagnostic = await this.kubectl(['describe','pod','-n',this.namespace]).catch(e=>e.message);
        const logs = this.project ? await this.kubectl(['logs','-n',this.namespace,'node-1','-c','compile']).catch(e=>e.message) : '';
        throw new Error(`${error.message}\n${logs}\n${diagnostic}`);
      }
    } else {
      if (this.project) {
        for (const [target,project] of [['shared',this.project],...Object.entries(this.project.nodes || {})]) {
          const output = join(this.directory,'bin',target);
          await mkdir(output,{recursive:true});
          const compiler = `${this.prefix}-compile`;
          this.resources.push(compiler);
          this.run.log('runtime',{message:`正在编译多文件 Go 项目 · ${target}…`});
          await command('docker',['run','--rm','--name',compiler,'--user','0',
            '--mount',bindMount(project.snapshot,'/protocol'),
            '--mount',bindMount(output,'/app',false),
            '-e',`DISTVIS_ENTRY=${project.entry}`,'-e',`GOPROXY=${this.goProxy}`,
            this.image,'sh','-c',compileProject],{timeout:240000});
          this.resources.pop();
        }
      }
      for (const node of this.run.nodes) {
        const name = `${this.prefix}-${node}`;
        const args = ['run', '-d', '--name', name, '--label', `distvis.run=${this.run.id}`, '--network', 'none', '--memory', '128m', '--cpus', '0.5', '--pids-limit', '64', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges'];
        if (this.project) args.push(
          '--workdir','/protocol','-e','DISTVIS_STATE_DIR=/state',
          '--mount', bindMount(this.project.nodes?.[node]?.snapshot || this.project.snapshot,'/protocol'),
          '--mount', bindMount(this.project.root,'/protocols'),
          '--mount', bindMount(join(this.directory,'bin',this.project.nodes?.[node] ? node : 'shared'),'/app'));
        args.push(this.image);
        await command('docker', args);
        this.resources.push(name);
      }
    }
    this.run.start();
    for (const node of this.run.nodes) this.attach(node);
    this.run.log('runtime', { message: `${this.run.nodes.length} 个 Go 节点已启动` });
  }
  attach(node) {
    this.expected.delete(node);
    const executable = this.project ? '/app/node' : `/app/${node}`;
    const args = this.k8s
      ? ['--context', 'docker-desktop', 'exec', '-i', '-n', this.namespace, node, '--', executable]
      : ['exec', '-i', `${this.prefix}-${node}`, executable];
    const child = spawn(this.k8s ? 'kubectl' : 'docker', args, { stdio: ['pipe', 'pipe', 'pipe'] });
    this.sessions.set(node, child);
    // Prevent EPIPE from crashing the coordinator when a student process exits.
    child.stdin.on('error', () => {});
    let buffered = 0;
    child.stdout.on('data', b => {
      buffered += b.length;
      if (buffered > 4 * 1024 * 1024) {
        this.run.log('runtime', { node, message: '节点输出超过 4 MiB，已关闭该会话' });
        child.kill();
      }
    });
    createInterface({ input: child.stdout }).on('line', line => {
      if (this.closed || this.sessions.get(node) !== child || !this.run.online[node] || this.run.status !== 'running') return;
      try {
        if (Buffer.byteLength(line) > 65536) throw new Error('单条消息超过 64 KiB');
        const msg = JSON.parse(line);
        if (msg.type === 'send') this.run.send(node, msg.to, msg.payload);
        else if (msg.type === 'state') this.run.report(node, msg.state);
        else if (msg.type === 'input_schema') this.run.declareInputs(node, msg.schema);
        else if (msg.type === 'command_result') this.run.log('command_result', { node, commandId: msg.commandId, result: msg.result, error: msg.error || undefined });
        else if (msg.type === 'received') {
          const message = this.pending.get(msg.id);
          if (message?.to === node) { this.run.log('receive', message); this.pending.delete(msg.id); }
        } else this.run.log('stdout', { node, message: line.slice(0, 2000) });
      } catch (error) { this.run.log('stderr', { node, message: `协议输出错误: ${error.message}` }); }
    });
    child.stderr.on('data', data => {
      if (this.closed || this.sessions.get(node) !== child) return;
      const message = data.toString().slice(0, 4000);
      const expectedExit = this.expected.has(node) && /^command terminated with exit code 137\s*$/.test(message);
      this.run.log(expectedExit ? 'runtime' : 'stderr', { node, message, ...(expectedExit ? { action: 'injected-crash-exit' } : {}) });
    });
    child.on('error', error => this.run.log('runtime', { node, message: error.message }));
    child.on('close', code => {
      if (this.sessions.get(node) !== child) return;
      this.sessions.delete(node);
      if (!this.closed && !this.expected.has(node) && this.run.status === 'running') {
        this.run.online[node] = false;
        this.run.epochs[node]++;
        this.dropPending(node);
        this.run.log('node', { node, online: false, reason: `进程退出 (${code})` });
      }
    });
    this.write(node, { type: 'init', node, nodes: this.run.nodes, protocol: this.run.config.protocol });
  }
  write(node, msg) {
    const child = this.sessions.get(node);
    if (!child || !child.stdin.writable || child.stdin.writableLength > 1024 * 1024) return false;
    child.stdin.write(JSON.stringify(msg) + '\n');
    return true;
  }
  deliver(message) {
    if (this.write(message.to, { type: 'message', ...message })) {
      this.pending.set(message.id, message);
      this.run.log('deliver', message);
    } else this.run.log('drop', { ...message, reason: '节点会话不可写' });
  }
  async fault(fault) {
    this.run.validateFault(fault);
    const node = fault.node;
    if (fault.kind === 'crash' && this.run.online[node]) {
      this.expected.add(node);
      try {
        if (this.k8s) await this.kubectl(['exec', '-n', this.namespace, node, '--', 'killall', '-9', this.project ? 'node' : node]);
        else await command('docker', ['kill', `${this.prefix}-${node}`]);
      } catch (error) {
        this.expected.delete(node);
        throw error;
      }
      this.dropPending(node);
    }
    if (fault.kind === 'recover' && !this.run.online[node]) {
      if (!this.k8s) await command('docker', ['start', `${this.prefix}-${node}`]);
      this.run.fault(fault);
      this.attach(node);
      return;
    }
    this.run.fault(fault);
  }
  dropPending(node) {
    for (const [id, message] of this.pending) {
      if (message.to === node) {
        this.run.log('drop', { ...message, reason: '节点退出，尚未接收的消息已丢弃' });
        this.pending.delete(id);
      }
    }
  }
  cleanup() {
    this.cleanupPromise ||= this.cleanupResources();
    return this.cleanupPromise;
  }
  async cleanupResources() {
    if (this.closed) return;
    this.closed = true;
    this.run.off('deliver', this.onDeliver);
    this.run.off('command', this.onCommand);
    for (const child of this.sessions.values()) child.kill();
    this.pending.clear();
    const errors = [];
    if (this.k8s && this.resources.length) {
      // Wait until Pods release their image references before removing the image.
      try { await this.kubectl(['delete', 'namespace', this.namespace, '--wait=true', '--timeout=60s']); } catch (e) { errors.push(e.message); }
    } else {
      for (const name of this.resources) {
        try { await command('docker', ['rm', '-f', name]); } catch (e) { errors.push(e.message); }
      }
    }
    try { await command('docker', ['image', 'rm', this.image]); } catch (e) { if (this.resources.length) errors.push(e.message); }
    if (errors.length) this.run.log('runtime', { message: '资源清理未全部完成', errors });
  }
}
