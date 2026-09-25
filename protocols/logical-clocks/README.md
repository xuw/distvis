# 逻辑时钟：Lamport 时钟与向量时钟

课件：L05《Distributed Synchronization (1)》第 28–46 页。

## 算法

每个事件（本地事件、发送、接收）同时打上三种时间戳：

- **Lamport 时钟 L**：规则 1，每个事件发生前 `L := L + 1`；规则 2，发送时捎带 `t = L`，接收时 `L := max(L, t)` 再应用规则 1。
- **全序时间戳 `L.id`**：L 相同时用节点编号打破平局（课件第 40 页 `M·L + i`）。
- **向量时钟 V**：本进程事件使自己的分量加一；发送时捎带整个向量；接收时逐项取最大，再把自己的分量加一。

`Application.Compare(a, b)` 取出两个事件的时间戳（自己的事件直接读，别的节点的事件用 `Clock.Event` RPC 向所有者查询），给出：

- Lamport 比较 `L(a) vs L(b)`；
- 向量比较：`a→b`（V(a) < V(b)）、`b→a`、`concurrent`（互不小于）；
- Lamport 顺序是否与因果一致（`agrees`）。

要点：`e→e'` 蕴含 `L(e)<L(e')`，反之不成立；`V(e)<V(e')` 当且仅当 `e→e'`。

事件编号是 `节点:序号`，例如 `node-1:2` 是 node-1 的第 2 个事件；它恰好等于该事件向量时钟中自己的分量。时空图中消息标签为 `调用 Clock.Receive · {"L":2,"V":[2,0,0],...}`，可以直接看到捎带的两种时钟。

## 节点状态

| 字段 | 含义 |
| --- | --- |
| `role` | 固定为 `process` |
| `lamport` | 当前 Lamport 时钟 |
| `vector` | 当前向量时钟，如 `[2,2,0]`（第 i 项对应 node-i） |
| `eventCount` | 本节点已发生的事件数 |
| `events` | 最近 10 个事件：`id`、`kind`（local/send/recv）、`label`、`L`、`V`、`total`（`L.id`） |
| `lastCompare` | 最近一次 Compare 的结果：`La`/`Lb`、`Va`/`Vb`、`lamport`（`<`/`=`/`>`）、`lamportLess`、`vector`（`a→b`/`b→a`/`concurrent`/`same`）、`total`、`agrees`、`note` |
| `auto` | 是否开启随机事件 |

## 应用输入

| 方法 | 作用 |
| --- | --- |
| `Application.Local(label)` | 发生一个本地事件 |
| `Application.Send(to, label, recvLabel)` | 发送事件，消息捎带 L 和 V；目标节点收到时产生接收事件（标签为 recvLabel，可留空） |
| `Application.Compare(a, b)` | 比较两个事件，如 `a=node-1:2, b=node-3:1`，结果写入 `lastCompare` |
| `Application.Auto(enabled, intervalMs)` | 随机产生本地事件或向随机节点发消息（平均间隔 intervalMs，默认 1500） |

## 观察场景

用 `node scripts/scenarios.mjs run logical-clocks <场景ID>` 自动回放后，在「运行历史」中逐步查看。

### slides-figure · 课件第 37 页的图（3 节点）

1. p3 本地事件 e；p1 本地事件 a，然后发送 m1 给 p2（发送事件 b，p2 上的接收事件 c）。
2. p2 发送 m2 给 p3（d），p3 接收为 f。
3. 时钟：a=1、b=2、c=max(0,2)+1=3、d=4、e=1、f=max(1,4)+1=5；向量 b=[2,0,0]、d=[2,2,0]、f=[2,2,2]。
4. Compare(b, e)：`L(b)=2 > L(e)=1`，但 `vector=concurrent`。Compare(e, c)：`L(e)<L(c)` 却不是 e→c。Compare(a, f)：`a→b`（经 m1、m2 的传递性）。

思考：只看 Lamport 值，能否断定 e 发生在 b 之前？能断定的是哪个方向？（L(e)>L(e') ⇒ 不是 e→e'）

### causal-chain · 跨三个节点的因果链

p1 → p2 → p3 的消息链，向量逐步变为 [2,0,0] → [2,3,0] → [2,3,2]，Compare 首尾事件为 `a→b`，Lamport 顺序与之一致；反向比较得到 `b→a`。

### concurrent-equal · L 相等的并发事件

三个节点各一个本地事件，L 都是 1：L 相等必然并发，全序 `1.1 < 1.2 < 1.3` 只是用编号任意定序。随后一条消息让 x → recv(m)，而 z 与 recv(m) 仍然并发，尽管 L(z)=1 < 3。

思考：全序 Lamport 时钟把并发事件也排了先后，这在全序多播 / 互斥中为什么可以接受？

### auto-random · 4 节点随机事件

所有节点随机产生事件填满时空图，停下后比较几对事件。检查的是不变式：凡是向量判定有因果的，Lamport 顺序一定一致（`agrees=true`）；否则为 `concurrent`。可以自己在时空图上挑两个事件 Compare，再沿消息箭头验证。

## 实现边界

- 发送是异步的：`Send` 立即返回发送事件编号，接收事件在消息到达后出现在目标节点。
- 事件只保存在内存中，节点崩溃恢复后时钟与事件从零开始；Compare 查询崩溃节点的事件会失败（5 秒超时）。
- 查询事件用的 `Clock.Event` RPC 只是为了演示方便，不属于时钟算法本身，也不会推进时钟。
