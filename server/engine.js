import { EventEmitter } from 'node:events';
import { randomUUID } from 'node:crypto';
import { createActor } from './protocols.js';
import { validateSchema, validateValues } from './inputs.js';

export const protocols = {
  raft: { name: 'Raft 选主', description: '观察任期、投票与心跳，以及故障后的重新选举。此示例不包含日志复制。' },
  token: { name: '令牌环互斥', description: '只有持有令牌的节点进入临界区；节点崩溃可能导致令牌丢失，需要协议自行恢复。' },
  gossip: { name: 'LWW 副本存储', description: '周期反熵传播键值，以 (逻辑版本, 节点 ID) 决胜；分区恢复后最终收敛。' },
  custom: { name: '编辑器中的 Go 项目', description: '运行协议工作区保存的多文件项目；lab.Main 管理节点，标准 RPC 调用自动记录。' },
};

export function validateConfig(input) {
  const config = {
    name: String(input.name || '未命名实验').slice(0, 100),
    protocol: input.protocol || 'raft',
    runtime: input.runtime || 'docker',
    nodeCount: Number(input.nodeCount ?? 5),
    seed: Number(input.seed ?? 42),
    latency: Number(input.latency ?? 80),
    bandwidth: Number(input.bandwidth ?? 128),
    delayModel: input.delayModel || 'fixed',
    jitter: Number(input.jitter ?? 0),
    ...(input.experimentId ? {experimentId:input.experimentId} : {}),
    ...(input.protocolId ? {protocolId:input.protocolId,protocolRevision:input.protocolRevision} : {}),
    ...(input.project ? { project: structuredClone(input.project) } : {}),
  };
  if (!protocols[config.protocol] && !/^directory:[^/\\]+$/.test(config.protocol)) throw new Error('未知协议');
  if (!['simulation', 'docker', 'kubernetes'].includes(config.runtime)) throw new Error('未知运行环境');
  for (const [key, min, max] of [['nodeCount', 2, 12], ['seed', 1, 2147483647], ['latency', 0, 30000], ['bandwidth', 1, 100000], ['jitter', 0, 30000]]) {
    if (!Number.isInteger(config[key]) || config[key] < min || config[key] > max) throw new Error(`${key} 应为 ${min}–${max} 的整数`);
  }
  validateDelayModel(config);
  if (config.delayModel === 'fixed') config.jitter = 0;
  if (config.runtime === 'simulation' && (config.protocol === 'custom' || config.protocol.startsWith('directory:'))) throw new Error('自定义 Go 程序请选择 Docker 或 Kubernetes');
  return config;
}

export const delayModels = ['fixed', 'exponential'];
function validateDelayModel(rule) {
  if (!delayModels.includes(rule.delayModel)) throw new Error('delayModel 应为 fixed 或 exponential');
  if (rule.delayModel === 'exponential' && rule.jitter < 1) throw new Error('指数分布抖动的均值 jitter 应为 1–30000 毫秒');
}

// All protocol traffic traverses this transport. No protocol-specific fault rules.
export class Experiment extends EventEmitter {
  constructor(config, { id = randomUUID(), createdAt = new Date().toISOString(), record = () => {} } = {}) {
    super();
    this.id = id;
    this.createdAt = createdAt;
    this.config = validateConfig(config);
    this.record = record;
    this.events = [];
    this.time = 0;
    this.status = 'created';
    this.nodes = Array.from({ length: this.config.nodeCount }, (_, i) => `node-${i + 1}`);
    this.online = Object.fromEntries(this.nodes.map(n => [n, true]));
    this.epochs = Object.fromEntries(this.nodes.map(n => [n, 0]));
    this.states = {};
    this.links = {};
    this.queue = [];
    this.serial = 0;
    this.messageSeq = 0;
    this.randomState = this.config.seed;
    this.nextWire = {};
    this.lastArrival = {};
    this.actors = {};
    this.inputSchemas = {};
    this.durationLimit = 600000;
  }
  random() {
    this.randomState = (Math.imul(this.randomState, 1664525) + 1013904223) >>> 0;
    return this.randomState / 4294967296;
  }
  log(type, details = {}) {
    if (this.status === 'running' && this.events.length >= 59999 && type !== 'lifecycle') {
      this.stop('事件上限 60,000');
      return;
    }
    const event = { ...structuredClone(details), seq: this.events.length + 1, time: this.time, timestamp: new Date(Date.parse(this.createdAt) + this.time).toISOString(), type };
    this.record(event); // Persist before publication.
    this.events.push(event);
    this.emit('event', event);
    return event;
  }
  info() {
    return { id: this.id, createdAt: this.createdAt, config: this.config, status: this.status, time: this.time, eventCount: this.events.length };
  }
  schedule(delay, fn) {
    this.queue.push({ at: this.time + Math.max(0, delay), order: this.serial++, fn });
    this.queue.sort((a, b) => a.at - b.at || a.order - b.order);
  }
  advance(ms) {
    if (this.status !== 'running') return;
    const target = Math.min(this.time + ms, this.durationLimit);
    while (this.queue.length && this.queue[0].at <= target && this.status === 'running') {
      const next = this.queue.shift();
      this.time = next.at;
      next.fn();
      if (this.events.length >= 60000) this.stop('事件上限 60,000');
    }
    this.time = target;
    if (this.time >= this.durationLimit) this.stop('运行时长上限 10 分钟');
  }
  report(node, state) {
    if (!this.online[node] || this.status !== 'running') return;
    this.states[node] = structuredClone(state);
    this.log('state', { node, state });
  }
  send(from, to, payload) {
    if (this.status !== 'running' || !this.online[from]) return;
    if (!this.nodes.includes(to)) throw new Error(`未知目标节点 ${to}`);
    const id = `${this.id.slice(0, 8)}-${++this.messageSeq}`;
    const key = `${from}>${to}`;
    const link = this.links[key] || {};
    const bytes = Buffer.byteLength(JSON.stringify(payload));
    const bandwidth = link.bandwidth ?? this.config.bandwidth; // KiB/s, per directed link
    const wireStart = Math.max(this.time, this.nextWire[key] || 0);
    const wireEnd = wireStart + Math.ceil(bytes / (bandwidth * 1024) * 1000);
    this.nextWire[key] = wireEnd;
    const model = link.delayModel ?? this.config.delayModel, mean = link.jitter ?? this.config.jitter;
    // Exponential jitter on top of the fixed latency; seeded, so a model run stays reproducible.
    const jitter = model === 'exponential' ? Math.round(-mean * Math.log(1 - this.random())) : 0;
    // Each directed link stays FIFO like a TCP connection: jitter delays, never reorders.
    const arrival = Math.max(wireEnd + (link.latency ?? this.config.latency) + jitter, this.lastArrival[key] || 0);
    this.lastArrival[key] = arrival;
    const delay = arrival - this.time;
    const message = { id, from, to, payload: structuredClone(payload), bytes, delay, ...(model === 'exponential' ? { jitter } : {}) };
    this.log('send', message);
    if (this.status !== 'running') return;
    const sourceEpoch = this.epochs[from], targetEpoch = this.epochs[to];
    if (link.blocked || !this.online[to]) {
      this.log('drop', { ...message, reason: link.blocked ? '链路中断' : '目标节点离线' });
      return id;
    }
    this.schedule(delay, () => {
      // In-flight packets are also affected by a newly injected partition/crash.
      if (!this.online[to] || !this.online[from] || this.links[key]?.blocked || this.epochs[from] !== sourceEpoch || this.epochs[to] !== targetEpoch) {
        this.log('drop', { ...message, reason: '传输期间发生故障' });
        return;
      }
      if (this.config.runtime === 'simulation') {
        this.log('receive', message);
        this.actors[to].receive(from, message.payload);
      } else {
        this.emit('deliver', message);
      }
    });
    return id;
  }
  start() {
    this.status = 'running';
    this.log('lifecycle', { action: 'start', config: this.config, nodes: this.nodes });
    if (this.config.runtime === 'simulation') {
      for (const node of this.nodes) this.actors[node] = createActor(this, node);
      for (const node of this.nodes) this.declareInputs(node, this.actors[node].inputs);
      for (const node of this.nodes) this.actors[node].start();
    }
  }
  stop(reason = '用户结束实验') {
    if (['completed', 'failed', 'interrupted'].includes(this.status)) return;
    this.status = 'completed';
    this.queue = [];
    this.log('lifecycle', { action: 'stop', reason });
    this.emit('stop');
  }
  validateFault(fault) {
    if (this.status !== 'running') throw new Error('实验未在运行');
    if (!['crash', 'recover', 'link', 'heal'].includes(fault.kind)) throw new Error('未知故障类型');
    if (['crash', 'recover'].includes(fault.kind) && !this.nodes.includes(fault.node)) throw new Error('未知节点');
    if (fault.kind === 'link') {
      if (!this.nodes.includes(fault.from) || !this.nodes.includes(fault.to) || fault.from === fault.to) throw new Error('请选择两个不同的节点');
      for (const [key, min, max] of [['latency', 0, 30000], ['bandwidth', 1, 100000]]) {
        if (!Number.isInteger(fault[key]) || fault[key] < min || fault[key] > max) throw new Error(`${key} 超出范围`);
      }
      if (typeof fault.blocked !== 'boolean' || typeof fault.bidirectional !== 'boolean') throw new Error('链路选项应为布尔值');
      if (fault.delayModel !== undefined || fault.jitter !== undefined) {
        if (!Number.isInteger(fault.jitter ?? 0) || (fault.jitter ?? 0) < 0 || fault.jitter > 30000) throw new Error('jitter 超出范围');
        validateDelayModel({ delayModel: fault.delayModel ?? 'fixed', jitter: fault.jitter ?? 0 });
      }
    }
  }
  fault(fault) {
    this.validateFault(fault);
    this.log('fault', { fault });
    if (fault.kind === 'crash' || fault.kind === 'recover') {
      const on = fault.kind === 'recover';
      if (this.online[fault.node] === on) return;
      this.online[fault.node] = on;
      this.epochs[fault.node]++;
      this.log('node', { node: fault.node, online: on });
      if (this.config.runtime === 'simulation') {
        // Actors retain local protocol state (Raft term/votedFor included) across crash.
        this.actors[fault.node].generation++;
        if (on) this.actors[fault.node].start(true);
      }
    } else if (fault.kind === 'heal') {
      this.links = {};
    } else {
      const rule = { latency: fault.latency, bandwidth: fault.bandwidth, blocked: fault.blocked };
      // Omitted delay model inherits the run's; an explicit fixed model disables jitter on this link.
      if (fault.delayModel !== undefined) Object.assign(rule, { delayModel: fault.delayModel, jitter: fault.delayModel === 'fixed' ? 0 : fault.jitter });
      this.links[`${fault.from}>${fault.to}`] = rule;
      if (fault.bidirectional) this.links[`${fault.to}>${fault.from}`] = rule;
    }
  }
  declareInputs(node, schema) {
    if (!this.nodes.includes(node) || this.status !== 'running') throw new Error('节点未运行');
    validateSchema(schema);
    this.inputSchemas[node] = structuredClone(schema);
    this.log('input_schema', { node, schema });
  }
  // Validation shared by single and batched inputs; nothing is logged until every input is valid.
  prepareCommand(node, key, value, action = 'write', values) {
    if (this.status !== 'running' || !this.nodes.includes(node) || !this.online[node]) throw new Error(`${node} 未运行`);
    if (this.mutating) throw new Error('故障操作正在执行，请稍后提交');
    const schema = this.inputSchemas[node]?.find(s => s.action === action);
    if (!schema) throw new Error(`${node} 未声明此输入动作`);
    // Retain the original key/value API for existing clients.
    if (values === undefined) values = { ...(key === undefined ? {} : { key }), ...(value === undefined ? {} : { value }) };
    validateValues(schema, values);
    const cmd = { node, action, values: structuredClone(values) };
    if (typeof values.key === 'string') cmd.key = values.key;
    if (typeof values.value === 'string') cmd.value = values.value;
    return cmd;
  }
  dispatch(cmd, extra = {}) {
    const event = this.log('command', { ...cmd, ...extra });
    if (!event || this.status !== 'running') throw new Error('实验已结束');
    cmd.id = `input-${event.seq}`;
    try {
      if (this.config.runtime === 'simulation') this.actors[cmd.node].command(cmd);
      else if (!this.emit('command', cmd)) throw new Error('节点会话不可写');
    } catch (error) {
      this.log('command_error', { node: cmd.node, commandSeq: event.seq, message: error.message });
      throw error;
    }
    return { commandSeq: event.seq, id: cmd.id };
  }
  command(node, key, value, action = 'write', values) {
    return this.dispatch(this.prepareCommand(node, key, value, action, values));
  }
  // Concurrent inputs: all are validated first, then delivered at the same coordinator time,
  // so the nodes start their operations without any causal relation between them.
  commandBatch(batch) {
    if (!Array.isArray(batch) || batch.length < 1 || batch.length > this.nodes.length) throw new Error(`batch 应包含 1–${this.nodes.length} 个输入`);
    if (new Set(batch.map(b => b?.node)).size !== batch.length) throw new Error('同一批输入中每个节点只能出现一次');
    const cmds = batch.map(b => this.prepareCommand(b.node, undefined, undefined, b.action, b.values ?? {}));
    const group = `batch-${this.events.length + 1}`, nodes = cmds.map(c => c.node);
    const results = [];
    for (const cmd of cmds) {
      try { results.push({ node: cmd.node, ...this.dispatch(cmd, { batch: group, concurrentWith: nodes.filter(n => n !== cmd.node) }) }); }
      catch (error) { results.push({ node: cmd.node, error: error.message }); }
    }
    return { batch: group, commands: results };
  }
}
