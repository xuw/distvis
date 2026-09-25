# NTP 时钟同步

课件：L05《Distributed Synchronization (1)》第 15–21 页（Network Time Protocol）。

## 算法

- 一轮交换得到四个时间戳：客户端发送 `t0`、服务器接收 `t1`、服务器发送 `t2`、客户端接收 `t3`（`t0`、`t3` 用客户端时钟，`t1`、`t2` 用服务器时钟）。
- `delay = (t3 − t0) − (t2 − t1)`：客户端等待的时间减去服务器处理时间，只剩网络往返。
- `offset = ((t1 − t0) + (t2 − t3)) / 2`，即 `t2 + delay/2 − t3`（课件第 20 页）；客户端把时钟拨动 offset。真实偏差落在 `offset ± delay/2` 之内。
- 与 Cristian 的区别：服务器处理/排队时间被量出并扣除，不影响精度。
- 多次测量（第 21 页）：每次同步（一个 burst）与每个上游做 8 次交换，选 **delay 最小** 的样本——它受排队影响最少，误差界也最窄。
- 层次（第 15 页）：node-1 为 stratum 1（直接接收 UTC）；node-2、node-3 为 stratum 2，从 node-1 同步；node-4 及以后为 stratum 3，同时向 node-2、node-3 取样，采用 delay 最小的那个样本。

时空图中一次交换显示为 `调用 NTP.Query · {"t0":"10:00:01.234"}`，返回 `{"t1":…, "t2":…}`；一个 burst 的 8 次交换并行发出。

## 模拟时钟与网络抖动

- 每个节点模拟自己的硬件时钟：`local = anchorLocal + (now − anchorReal) × rate`，`rate = 1 + drift/1000`，真实时间即宿主机时间。默认偏差/漂移与 `clock-cristian` 相同（node-1 为 0/0；node-2 +1800ms、+20ms/s；node-3 −1200ms、−15ms/s；node-4 +2600ms、+10ms/s；node-5 −700ms、−25ms/s），可用 `SetClock` 覆盖。时钟参数保存在 `/state`，崩溃期间照常走。
- 平台链路延迟是固定值，因此用服务器端的随机等待模拟抖动：服务器在盖 `t1` 之前、盖 `t2` 之后各睡 `jitterMs × u³`（u 在 0..1 均匀分布：多数很短，偶尔很长，像排队）。在协议看来，这与请求/应答包在网络中多走了这么久无法区分。
- `delayMs` 则是 `t1` 与 `t2` 之间的处理时间，会被 NTP 扣除。

## 节点状态

| 字段 | 含义 |
| --- | --- |
| `role` / `stratum` | `stratum-1` / `stratum-2` / `stratum-3` |
| `upstreams` | 本节点向哪些上游取样 |
| `localTime` / `errorMs` | 本地时钟读数 / 本地 − 真实时间 |
| `driftMsPerSec` / `backwardJumps` | 漂移率 / 时钟被往回拨的次数 |
| `served` | 作为服务器回答的交换次数 |
| `serverDelayMs` / `serverJitterMs` | 作为服务器时的处理时间 / 抖动上限（非 0 时显示） |
| `lastSync.upstream` | 选中样本来自哪个上游 |
| `lastSync.delayMs` / `offsetMs` | 选中样本的 delay 与 offset |
| `lastSync.serverProcMs` | 选中样本的 `t2 − t1` |
| `lastSync.boundMs` / `withinBound` | 误差界 delay/2，以及同步后相对 UTC 的误差是否在界内（stratum 3 还要叠加上游自身误差） |
| `lastSync.errorBeforeMs` / `errorAfterMs` | 同步前后的真实误差 |
| `lastSync.samples` | 所有样本 `上游 d=delay o=offset`，`←` 为选中者 |
| `lastSync.sampleSpreadMs` | 样本 offset 的极差 |
| `bursts` / `autoPeriodMs` | 已完成的同步次数 / 周期同步间隔 |
| `avgErrMs` | 历次同步后 \|误差\| 的平均（采用最小 delay 样本） |
| `avgSampleErrMs` | 假如每次只用单个样本，\|误差\| 的平均（按全部样本计算） |

## 应用输入

| 方法 | 作用 |
| --- | --- |
| `Application.Sync()` | 做一个 burst（每个上游 8 次交换）并按最小 delay 样本调整时钟 |
| `Application.AutoSync(periodMs)` | 每个 burst 结束后等待 periodMs 再做下一次，0 关闭 |
| `Application.SetServer(delayMs, jitterMs)` | 本节点作为服务器时的处理时间与抖动上限 |
| `Application.SetClock(offsetMs, driftMsPerSec)` | 重设本节点的时钟偏差和漂移率 |

## 观察场景

`node scripts/scenarios.mjs run clock-ntp <场景ID>` 自动回放；也可以打开同名实验手动操作。

### proc-delay · 服务器处理时间被扣除（3 节点，延迟 100ms）

node-1 每个请求处理 1000ms。样本表中 `t2 − t1 ≈ 1000`，`delay ≈ 200`，同步后误差仍在几十毫秒以内。与 `clock-cristian` 的 server-delay 场景（误差 +500ms）对比。

思考：NTP 能扣除的是“服务器知道自己花了多久”的部分；请求在服务器网卡队列里等待（盖 t1 之前）能扣除吗？

### jitter-filter · 网络抖动与最小 delay 过滤（3 节点，延迟 50ms）

node-1 设 `jitterMs=800`，node-2 周期同步 10 次。每个 burst 的样本表里 offset 相差几百毫秒；`avgSampleErrMs`（单样本平均误差，约 100ms 以上）远大于 `avgErrMs`（最小 delay 样本，约 20–30ms）。

思考：为什么 delay 最小的样本 offset 也最可信？offset 的误差是 (请求方向延迟 − 应答方向延迟)/2，它和 delay 有什么关系？

### asymmetric · 非对称链路仍有误差（3 节点）

node-2→node-1 方向延迟 700ms。8 个样本几乎完全一致（`sampleSpreadMs` 很小），但都偏 +300ms。多次测量只能过滤随机抖动，消除不了固定的不对称。

思考：只凭 t0..t3 能否区分“链路不对称”和“两边时钟差 300ms”？

### strata · 误差随 stratum 累积（5 节点）

每一跳的请求方向（下层→上层）都慢 200ms。node-2/3 同步后误差约 +100ms；node-4/5 从 node-2/3 同步，继承上游的 +100ms 再叠加自己这一跳的 +100ms，约 +200ms。`lastSync.upstream` 显示 stratum 3 选中了哪个上游。

思考：为什么 stratum 越大，离 UTC 越远？课件说 class 3 servers “get time from any server”，能否出现环？

## 实现边界

- 平台每 50ms 投递一次消息，每条消息实际延迟比设定值少 0–50ms，另有几毫秒进程间开销，相当于约 ±25ms 的额外抖动；“误差很小”的检查用 45ms 作阈值。
- 同步直接按 offset 跳变（step），不实现 NTP 的频率校正、时钟过滤算法的其他部分、多服务器投票和认证；burst 内 8 次交换并行发出以缩短演示时间，选中样本到拨表之间的漂移（最多约 1 秒 × 漂移率）未补偿。
- 时间戳用毫秒精度的 `HH:MM:SS.mmm` 字符串传输（跨午夜按 ±12 小时回绕处理）。
- stratum 由节点编号固定，不根据上游是否已同步动态调整。
