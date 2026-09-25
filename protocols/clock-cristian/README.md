# Cristian 时钟同步

课件：L05《Distributed Synchronization (1)》第 12–14 页（Cristian's Time Sync）。

## 算法

- node-1 是时间服务器 S，直接接收 UTC（误差恒为 0、无漂移）。
- 客户端 p 记下发送时刻，调用 `Time.Now`，服务器返回自己的时钟读数 `t`；p 收到后用**自己的时钟**算出 RTT，把时钟设为 `t + RTT/2`。
- 精度：收到回复时服务器时间落在 `[t + min, t + RTT − min]`（min 为估计的最小单向延迟），取中点，误差不超过 `RTT/2 − min`。
- 同步是一次跳变（step）：客户端时钟快的话会被拨回去（`backwardJumps` 加一）。

## 模拟时钟

所有容器共享宿主机的真实时钟，所以每个节点模拟自己的硬件时钟：`local = anchorLocal + (now − anchorReal) × rate`，`rate = 1 + drift/1000`。“真实时间”（UTC）就是宿主机时间。漂移被放大到每秒几十毫秒，一两分钟内就能看到。默认值由节点编号决定：

| 节点 | node-1 (S) | node-2 | node-3 | node-4 | node-5 |
| --- | --- | --- | --- | --- | --- |
| 初始偏差 | 0 | +1800ms | −1200ms | +2600ms | −700ms |
| 漂移率 | 0 | +20ms/s | −15ms/s | +10ms/s | −25ms/s |

可用 `SetClock` 覆盖。时钟参数保存在 `/state`：节点崩溃期间硬件时钟照常走，恢复后接着读。

## 节点状态

| 字段 | 含义 |
| --- | --- |
| `role` | `server`（node-1）/ `client` |
| `localTime` | 本地时钟读数（北京时间 HH:MM:SS.mmm） |
| `errorMs` | 本地时钟 − 真实时间 |
| `driftMsPerSec` | 漂移率 |
| `backwardJumps` | 时钟被往回拨的次数 |
| `served` / `delayMs` | 服务器：已回答的请求数 / 读时钟前的排队延迟 |
| `syncs` / `autoPeriodMs` | 客户端：同步次数 / 周期同步间隔（0 为关闭） |
| `lastSync.serverTime` | 服务器返回的 `t` |
| `lastSync.rttMs` | 用客户端自己的时钟测得的 RTT |
| `lastSync.adjustMs` | 这次拨动时钟的量 |
| `lastSync.errorBeforeMs` / `errorAfterMs` | 同步前后的真实误差 |
| `lastSync.boundMs` / `withinBound` | 精度界 `RTT/2 − min`，以及误差是否在界内 |
| `autoPeakMs` | 周期同步开始后观察到的最大 \|误差\|（锯齿的峰值） |

## 应用输入

| 方法 | 作用 |
| --- | --- |
| `Application.Sync(minMs)` | 客户端与 node-1 同步一次；minMs 为估计的最小单向延迟，只影响精度界 |
| `Application.AutoSync(periodMs, minMs)` | 每 periodMs 同步一次，0 关闭 |
| `Application.SetDelay(delayMs)` | 在 node-1 上设置：每个请求先排队 delayMs 再读时钟 |
| `Application.SetClock(offsetMs, driftMsPerSec)` | 重设本节点的时钟偏差和漂移率 |

## 观察场景

`node scripts/scenarios.mjs run clock-cristian <场景ID>` 自动回放；也可以打开同名实验手动操作。

### basic · 对称延迟下一次同步（3 节点，延迟 100ms）

node-2 快 1.8s、node-3 慢 1.2s，各 `Sync` 一次：RTT ≈ 200ms，同步后误差只剩几十毫秒，在 ±RTT/2 之内。10s 后漂移又把误差拉大到 ±150–200ms。

思考：精度界 `RTT/2 − min` 和实际误差差了多少？什么情况下实际误差会接近这个界？

### asymmetric · 非对称链路（3 节点）

把 node-2→node-1 的延迟改为 700ms（返回方向仍 100ms）。RTT ≈ 800ms，node-2 把时钟设为 `t + 400`，而真实经过的只有 100ms，于是误差 ≈ `(700 − 100)/2 = +300ms`，仍在 ±400ms 的精度界之内。node-3 不受影响。

思考：如果客户端知道 min = 100ms，精度界变成 300ms，实际误差恰好落在界上，为什么？客户端能否从一次交换中发现链路不对称？

### periodic · 周期同步抑制漂移（3 节点）

node-3 只同步一次；node-2 每 3s 同步。node-2 的误差呈锯齿形，峰值约为 漂移率 × 周期 ≈ 60ms（`autoPeakMs`）；24s 后 node-3 的误差已超过 250ms。

思考：要把误差控制在 10ms 以内，周期应该多短？对服务器的负载意味着什么？

### server-delay · 服务器排队延迟（3 节点）

node-1 `SetDelay(1000)`：每个请求先排队 1 秒再读时钟。客户端看到 RTT ≈ 1200ms，无法区分排队与网络传输，误差 ≈ +500ms（精度界也扩大到 ±600ms）。去掉延迟后再同步，恢复到几十毫秒以内。对照 `clock-ntp` 的 proc-delay 场景：NTP 用 t1、t2 把服务器处理时间扣掉了。

思考：课件说“RTT increases if many people go to S”。为什么这让时间服务器必须离客户端近，却又因此离 UTC 源更远？

## 实现边界

- 平台每 50ms 推进一次消息投递，每条消息的实际延迟比设定值少 0–50ms，另外还有进程间管道的几毫秒开销。这相当于 ±25ms 左右的网络抖动，所以“误差很小”的检查用 45ms 作为阈值。
- 时间戳用毫秒精度的 `HH:MM:SS.mmm` 字符串传输（跨午夜按 ±12 小时回绕处理）。
- 只有一个时间服务器，不处理服务器故障；调用超时（8s）即同步失败。
