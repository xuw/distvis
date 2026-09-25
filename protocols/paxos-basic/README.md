# Basic Paxos（单值共识）

课件：L07《Replication and Consensus》第 21–49 页。

## 算法

- 所有节点都是 acceptor 和 learner；任意节点都可以通过 `Application.Propose` 成为 proposer。
- 提案号写成课件的 `round.node`（`1.1`、`1.4`）：先比 round，再比节点编号，因此全局唯一且全序。
  新提案号 = 本节点见过的最大 round + 1（`maxRound`），与 Lamport 时钟类似。
- **第一阶段**：proposer 先让自己的 acceptor 投票，再向其余节点广播 `Paxos.Prepare{n}`。
  acceptor 若 n 大于已承诺的编号则承诺（返回 `ok=true`，并带回已接受的 `acceptedN/acceptedV`，即课件中的 `<A 1.1 紫荆>`），
  否则拒绝并返回 `higher`（它已承诺的更大编号）。
- proposer 得到多数派（5 节点中 3 个）承诺后，取其中 `acceptedN` 最大者的值；都没有接受过值时才用自己的值。
- **第二阶段**：广播 `Paxos.Accept{n, v}`；acceptor 只要 n 不小于已承诺的编号就接受。多数派接受 ⇒ 值被选定（chosen），
  proposer 再广播 `Paxos.Decide{v}` 通知 learner。
- 任一拒绝都说明有更高的提案号，proposer 立即放弃本轮（`result=rejected`）；联系不到多数派时 6 秒后 `result=timeout`。
- acceptor 在回复 Prepare/Accept 之前先 `sdk.Save` 持久化 `promised / accepted / maxRound / chosen`，崩溃恢复后仍记得承诺和投票。

时空图中 `调用 Paxos.Prepare · {"n":"1.4"}` 就是课件的 `P 1.4`，`调用 Paxos.Accept · {"n":"1.1","v":"紫荆"}` 就是 `A 1.1 紫荆`；
返回里的 `"ok":true/false` 对应图中的 OK / Reject。

## 节点状态

| 字段 | 含义 |
| --- | --- |
| `role` | `proposer` 正在运行提案 / `acceptor` 其余时间 |
| `promised` | 已承诺的最高提案号（不再接受更小编号） |
| `accepted` | 已接受的提案，如 `1.1 紫荆`；接受 ≠ 选定 |
| `chosen` | 本节点作为 learner 得知的选定值（未知时无此字段） |
| `conflictingDecide` | 已知选定值后又收到不同的 Decide（只可能在关闭持久化后出现） |
| `maxRound` | 见过的最大 round，用于生成新提案号 |
| `lastProposal` | 本节点最近一次提案：`n`、`value`（第一阶段后可能换成已接受的值）、`phase`（prepare/paused/accept/done）、`promises`/`accepts`（k/5）、`result`（chosen/rejected/timeout）、`rejectedBy`、`attempt`（第几次尝试） |
| `amnesia` | 是否关闭了 acceptor 状态持久化 |
| `backoff` | 重试前是否随机退避 |
| `recovered` | 本进程是否从持久化文件恢复（崩溃重启后为 true） |

## 应用输入

| 方法 | 作用 |
| --- | --- |
| `Application.Propose(value, pauseMs, retry)` | 发起提案；value 为空时按节点取 紫荆/澜园/桃李。`pauseMs` 是两阶段之间的停顿，用于摆出课件中的交错；`retry=true` 时被拒绝后用更高编号重试（直到选定或 40 次） |
| `Application.Amnesia(enabled)` | 打开后本节点不再持久化承诺和投票：崩溃重启就“忘记”（课件 p.47） |
| `Application.Backoff(enabled)` | 重试前的随机退避，默认打开；关闭后两个提案者可能活锁（课件 p.48） |

## 观察场景

所有场景都是 5 节点、链路延迟 300ms（活锁为 200ms），用链路阻断控制每个提案者能联系到哪个多数派，对应课件的图。
`node scripts/scenarios.mjs run paxos-basic <场景ID>` 自动回放，然后在「运行历史」中逐步查看。

### case1-no-prior · 情形 1：尚无提案（p.33–36）

1. 切断 N1 与 N4、N5 的链路；N1 `Propose(紫荆)`。
2. `P 1.1` 得到 N1–N3 三个承诺（3/5 已过半），随后 `A 1.1 紫荆` 被 N1–N3 接受，紫荆被选定。

思考：为什么 3/5 个承诺就可以进入第二阶段，而不必等 N4、N5？

### case2-chosen · 情形 2：已有选定值（p.37–40）

1. 先完成情形 1（紫荆已由 N1–N3 选定）。N4 与 N1、N2 断开，只能联系 N3、N5。
2. N4 `Propose(澜园)`：它从没见过任何提案（maxRound=0），提案号是 `1.4`（> `1.1`）。
3. N3 的 Promise 带回 `<A 1.1 紫荆>`，N4 放弃自己的 澜园，发送 `A 1.4 紫荆`；所有节点的 `chosen` 都是 紫荆。

观察：选定点就是 `A 1.1` 被第 3 个 acceptor 接受的那一刻；任意两个多数派必相交（这里交在 N3），后来者不可能错过已选定的值。

### case3-seen · 情形 3：看到尚未选定的值（p.41–42）

1. N1 `Propose(紫荆, pauseMs=3000)`，拿到承诺后停顿；此时把 N1→N2 的延迟调到 4000ms。
2. `A 1.1 紫荆` 先被 N1、N3 接受（2/5，尚未选定），N2 的那一份还在路上。
3. N4（与 N1、N2 断开）发起 `P 1.4`，在 N3 处看到 `<A 1.1 紫荆>`，沿用 紫荆 并被 N3–N5 选定。
4. 迟到的 `A 1.1` 到达 N2，N2 只承诺过 1.1，于是接受；N1 也凑够 3/5——两个提案都成功，值相同。

### case4-unseen · 情形 4：没看到之前的值（p.43–45）

1. 同情形 3，但延迟的是 N1→N3：`A 1.1 紫荆` 只被 N1、N2 接受。
2. N4 的 `P 1.4` 先到达 N3，N3–N5 都没接受过值，N4 提自己的 澜园 并选定。
3. 迟到的 `A 1.1` 到达 N3，被拒绝（`higher=1.4`），N1 的提案失败（`result=rejected`）。
4. N2 仍保存着 `accepted=1.1 紫荆`——接受过的值不一定被选定。

思考：为什么 P 1.4 使 A 1.1 失效是安全的？（前一个提案没有得到多数派。）

### livelock · 活锁与随机退避（p.48）

1. N1、N5 关闭退避，先后 `Propose(retry=true, pauseMs=1500)`。
2. 每个 Prepare 都使对方停顿中的提案失效；被拒绝的一方立即用更高的 round 重试，又使对方失效……
   15 秒后仍没有值被选定，`lastProposal.n` 不断增长（2.5、3.1、4.5……），`Paxos.Prepare` 消息持续增加。
3. 打开两边的随机退避，很快有一个提案在对方重试前完成，所有节点学到同一个值。

思考：FLP 说明异步系统中不能同时保证安全与活性；Paxos 选择了哪一个？课件 p.49 固定 leader 的方案为什么能消除大部分冲突？

### amnesia · 重启丢失投票，破坏安全性（p.47）

1. N3 打开 `Amnesia`；N1 以 N1–N3 选定 紫荆。
2. N3 崩溃并重启：`promised`、`accepted` 为空——它忘记了自己的承诺和投票。
3. N4（与 N1、N2 断开）以 N3–N5 为多数派发起 `P 1.4`，没人报告已接受的值，于是选定 澜园。
4. N1、N2 的 `chosen=紫荆`，N3–N5 的 `chosen=澜园`：两个不同的值都被“选定”。

### reboot-safe · 持久化保证重启安全（对照）

同 amnesia，但 N3 正常持久化：重启后 `accepted=1.1 紫荆` 仍在，N4 只能沿用 紫荆，所有节点一致。

思考：acceptor 必须在**回复之前**写盘，为什么先回复后写盘不行？

## 实现边界

- 单值（single-decree）Paxos；多值日志见 `paxos-kv`。
- 所有节点都是 acceptor/learner；proposer 的自投票是本地函数调用，不产生消息。
- 收到一个拒绝就放弃本轮（Paxos Made Simple 允许的优化），便于观察活锁；联系不到多数派时 6 秒超时。
- Decide 只是通知：learner 错过它（链路阻断、崩溃）就不会知道选定值，除非之后有提案再次经过它。
- `Backoff` 开关只保存在内存中，重启后恢复为默认（打开）；`Amnesia` 标志本身会持久化。
