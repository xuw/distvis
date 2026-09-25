# Bully 选举算法

课件：L06《Distributed Synchronization (2)》第 18–22 页。

## 算法

目标：选出**存活节点中编号最大**的那个作为协调者（`role=leader`）。

- 协调者每秒向所有节点发送 `Bully.Heartbeat`。follower 超过 3–4 秒（3 秒 + 随机抖动）没有收到心跳，就认为协调者失效。
- 发现失效的节点 P 发起选举（`role=election`）：
  1. 向所有编号更大的节点发送 `Bully.Election`；
  2. 1.2 秒内没有任何 `Bully.OK`：P 获胜，成为协调者，向所有节点广播 `Bully.Coordinator`；
  3. 收到 OK：更高的节点接手了，P 的工作结束，等待 Coordinator；3 秒内等不到就重新选举。
- 收到 Election（必然来自更低编号）：回复一个 `Bully.OK`，然后自己发起选举（已在选举中则不重复发起）；如果自己已经是协调者，就直接给对方发 Coordinator。
- 收到更高编号的 Coordinator / Heartbeat：跟随它。收到**更低**编号的：说明自己应当是协调者——已是协调者就回一个 Coordinator，否则发起选举「抢」回来。
- 节点启动（包括崩溃后恢复）约 1 秒后发起一次选举，所以恢复的最高编号节点会立即夺回协调者位置。

OK 是一条单独的消息（而不是 RPC 的返回），这样时空图中 Election / OK / Coordinator 与课件插图一一对应；等待 OK 用定时器实现，不依赖 10 秒的 RPC 超时。

## 节点状态

| 字段 | 含义 |
| --- | --- |
| `role` | `leader`（拓扑图高亮）/ `follower` / `election` 正在选举 |
| `leader` | 本节点认定的协调者（选举中为空串） |
| `electionsStarted` | 本节点发起选举的次数（本次启动以来） |
| `lastElection` | 最近一次选举的经过，例如「更高编号无人回 OK，成为协调者」 |
| `okFrom` | 最近一次选举中回复 OK 的更高节点 |
| `timesLeader` | 成为协调者的次数 |
| `sent` | 本节点发出的 election / ok / coordinator 消息数（不含心跳） |

## 应用输入

| 方法 | 作用 |
| --- | --- |
| `Application.StartElection()` | 立即在本节点发起选举（模拟「本节点最先发现协调者失效」） |

## 观察场景

`node scripts/scenarios.mjs run bully-election [场景ID]` 自动回放并检查。

### startup · 启动时选出编号最大的节点（5 节点）

所有节点启动后发起选举。node-5 没有更高编号，立即获胜；其余节点都收到更高节点的 OK，最后收到 node-5 的 Coordinator。

思考：启动时共有多少条 Election？一般地，n 个节点同时发起时是 O(n²)。

### leader-crash · 协调者崩溃，次高节点接任（5 节点）

崩溃 node-5。最先超时的节点（随机抖动决定是谁）向更高节点发 Election，选举逐级向上传递；node-4 发往 node-5 的 Election 无人回应，1.2 秒后 node-4 胜出并广播 Coordinator。在时空图中数一数本次选举的 Election / OK / Coordinator 消息（节点状态 `sent` 也有计数）。

思考：最好情况（node-4 最先发现）和最坏情况（node-1 最先发现）分别需要多少条消息？

### slides · 课件示例（7 节点，延迟 200ms）

复现第 21–22 页插图：崩溃 node-7，并让 node-4 立即 `StartElection`（它「最先发现」）。

1. node-4 → 5、6、7 发 Election；5、6 回 OK，node-4 退出竞争。
2. node-5 → 6、7，node-6 → 7 继续选举；node-6 回 OK 给 node-5。
3. node-6 等不到 node-7 的 OK，成为协调者，Coordinator 发给所有人。

思考：课件注明「假设消息不丢失」。如果 node-6 发给 node-4 的 OK 丢了，会发生什么？

### recover-bully · 旧协调者恢复后抢回位置（5 节点）

node-5 崩溃，node-4 接任；node-5 恢复后立即发起选举，没有更高编号，直接自立并广播 Coordinator，node-4 让位。在 node-5 的 Coordinator 到达 node-4 之前，**短暂存在两个 leader**——检查项用 `moreThanOne` 断言了这一点。

思考：如果协调者手里有状态（例如集中式互斥的锁表），「抢回位置」会带来什么问题？

### two-crash · 最高的两个节点同时崩溃（5 节点）

node-4、node-5 同时崩溃：node-3 发往 4、5 的 Election 都没有回应，成为协调者；node-1、node-2 的 Election 由 node-3 回 OK。

## 实现边界

- 超时时间是固定的（心跳 1 秒、失效判断 3–4 秒、等 OK 1.2 秒、等 Coordinator 3 秒），假设消息延迟远小于这些值；阻断链路或把延迟调大会导致误判和多个 leader 并存，这正是同步假设被破坏的后果。
- 没有任期号（对比 Raft），无法区分新旧协调者的消息。
