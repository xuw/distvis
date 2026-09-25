# Ricart & Agrawala 分布式互斥

课件：L06《Distributed Synchronization (2)》第 32–43 页。

## 算法

- 每个节点维护 Lamport 时钟；请求时间戳写成 `L.id`（先比 L，再比节点编号），因此是全序。
- 申请临界区：时钟加一，向其余 n-1 个节点广播 `RA.Request{L, from}`，收到 n-1 个 OK 后进入。
- 收到请求的节点三种情况：
  1. 不感兴趣（released）：RPC 返回 `ok=true`，即立即回复 OK；
  2. 正在临界区（critical）：返回 `ok=false`，把请求者记入 `deferred`；
  3. 自己也在等待（wanted）：比较时间戳，自己的更早则推迟，否则立即回复。
- 离开临界区时，对 `deferred` 中的每个节点发送 `RA.OK`（推迟的回复）。
- 每轮 2(n-1) 条协议消息：n-1 个请求 + n-1 个 OK（立即 OK 搭载在请求的 RPC 返回里）。

时空图中，`调用 RA.Request` 的返回 `{"ok":true}` 就是立即 OK；返回 `{"ok":false}` 表示被推迟，稍后会出现一条单独的 `调用 RA.OK`。

## 节点状态

| 字段 | 含义 |
| --- | --- |
| `role` | `released` 未申请 / `wanted` 等待 OK / `critical` 在临界区（拓扑图中高亮） |
| `clock` | Lamport 时钟 |
| `request` | 当前请求时间戳 `L.id` |
| `okFrom` / `okNeeded` | 已收到 OK 的节点 / 需要的数量 n-1 |
| `deferred` | 本节点推迟回复的请求者 |
| `entries` | 进入临界区次数 |
| `lastWaitMs` | 最近一次从请求到进入的等待时间 |
| `messagesSent` | 本节点发出的 Request 与推迟 OK 数量 |

## 应用输入

| 方法 | 作用 |
| --- | --- |
| `Application.Acquire(holdMs)` | 申请临界区，进入后停留 holdMs 毫秒（默认 2000）再自动释放 |
| `Application.Tick(events)` | 本地发生 events 个事件，Lamport 时钟前进，用于构造课件中的初始时间戳 |
| `Application.Auto(enabled, intervalMs, holdMs)` | 随机负载：本节点按平均间隔反复申请 |

## 观察场景

每个场景都已作为一个实验创建（`node scripts/scenarios.mjs setup mutex-ricart-agrawala`），可手动操作，也可用 `node scripts/scenarios.mjs run mutex-ricart-agrawala <场景ID>` 自动回放后在「运行历史」中逐步查看。

### concurrent · 两个节点同时请求（3 节点，延迟 400ms）

1. 在 node-1 和 node-3 上几乎同时 `Acquire(holdMs=3000)`。
2. 两个请求的 L 相同（都是 1），`1.1 < 1.3`，node-1 先进入。
3. 观察 node-1 对 node-3 的请求返回 `ok=false` 并出现在 `deferred`；node-1 离开时发出唯一一条 `RA.OK`。

思考：共有几条 Request、几个立即 OK、几个推迟 OK？与 2(n-1) 对照。

### slides-example · 课件例子：顺序由时间戳决定（3 节点）

1. `Tick` 设置初始时钟 P1=42、P2=11、P3=14。
2. node-3 申请并进入临界区。
3. node-1 在真实时间上**先**申请（时间戳 `44.1`），150ms 后 node-2 申请（`17.2`）。
4. node-3 离开后，node-2 先进入：node-2 推迟了给 node-1 的回复，而 node-1 立即回复了 node-2。

思考：两个请求是并发事件，Lamport 全序给出了一个任意但全体一致的顺序；这为什么足以保证安全？

### crash-blocks · 无关节点崩溃也会阻塞（3 节点）

1. 崩溃 node-2（它从未申请锁）。
2. node-1 申请，缺少 node-2 的 OK，一直停在 `wanted`；时空图中发往 node-2 的请求被丢弃并周期重试。
3. 恢复 node-2 后，重试的请求得到立即 OK，node-1 进入。

思考：与集中式算法相比，单点故障变成了 n 点故障。超时后能否直接假定对方同意？

### auto-load · 5 节点随机负载

所有节点开启 `Auto`，观察请求、推迟回复交织；每个节点都会多次进入（无饥饿），任意时刻只有一个 `critical`。

## 实现边界

- 消息不丢失、节点不故障是算法前提；这里对失败的请求做重试，只是为了演示「等待到对方恢复」。
- 节点状态只在内存中，崩溃恢复后从空状态开始。
