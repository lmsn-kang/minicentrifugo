# 面试项目介绍 & 深度追问

---

## 第一部分：项目介绍（面试时怎么说）

### 30 秒版本（电梯演讲）

> 我做了一个类 Centrifugo 的 WebSocket 实时消息推送服务。核心解决了三个问题：百万连接下的锁竞争、WebSocket 的并发写安全、多节点消息同步。具体来说，用 256 分片的 Hub 管理连接，每客户端独立写协程+channel 做异步写入和背压控制，用 NATS JetStream 做跨节点广播保证消息不丢，Redis 做 Presence 和 History 持久化。协议层用 Protobuf 替代 JSON 减少编解码开销和带宽。压测工具测到可以支撑万级连接。

### 2 分钟完整版

> 我想做一个高并发的实时消息推送服务，参考了 Centrifugo 的设计但自己从零实现核心链路。
>
> **整体架构**是四层：接入层用 NBIO 事件驱动处理 WebSocket 连接；路由层用分片 Hub 做本地消息分发；持久层用 Redis 做在线状态和消息历史；跨节点层用 NATS JetStream 做集群广播。
>
> **为什么这样选型**：NBIO 用 epoll 代替 Go 标准库的一连接一协程模型，万级连接时内存和调度开销更低。Hub 分 256 个 shard，用 FNV-1a 哈希把客户端打散，每个 shard 一个 RWMutex，Broadcast 时只加读锁，把锁竞争降到最低。写协程的设计是因为 NBIO 的 WebSocket 连接不是线程安全的，不能多个 goroutine 同时写，所以每个客户端起一个 writeLoop 协程从 channel 读数据串行写入，Write 方法用 select + default 做非阻塞发送实现背压——channel 满了就丢弃并断开，防止慢客户端拖垮服务。
>
> **跨节点广播**：单节点本地 Hub.Broadcast 就够了，但多节点时发消息的节点需要通知其他节点。我用 NATS JetStream 的 WorkQueue 模式做消息总线——发布时往 `centrifugo.publish.<channel>` 发消息，所有节点都订阅这个通配符 subject，收到后提取 channel 名做本地广播。JetStream 持久化保证消息不丢，ManualAck 保证消费确认。
>
> **协议**：纯 Protobuf 二进制帧，用 oneof 做消息多态：客户端发 Subscribe/Publish，服务端回 Push/Reply。比 JSON 省 3-10 倍带宽，编解码快 10 倍以上。
>
> **发现并修复了一个 bug**：最初 Publish 处理流程是本地广播一遍、NATS 再广播一遍，NATS 订阅回调又会触发本地广播，导致同一条消息被广播两次。最终改为只通过 NATS 广播，所有节点在回调中统一做本地广播，消除重复。

---

## 第二部分：面试官深度追问

面试官会从你的介绍中提取关键词逐层追问，以下是完整拷打路线：

---

### 追问路线一：分片 Hub

> **你说 256 分片减少锁竞争，256 这个数怎么定的？**

2 的幂次方，这样哈希取模可以用位掩码 `hash & 255` 代替 `hash % 256`，位运算是 CPU 单指令，取模需要除法慢很多。256 是经验值——太少（比如 4）锁竞争仍然集中在少数 shard，太多（比如 65536）内存浪费且 L1 缓存命中率低。Centrifugo 也是 256，压测验证过这个数字在万级连接下表现好。

> **Broadcast 遍历 256 个 shard 加读锁，每次消息都要锁 256 次，这个开销你考虑过吗？**

考虑过。RLock 在无竞争时开销极低（一次原子 CAS 操作），256 次 RLock 大约几百纳秒。只有当有写操作（连接/断开/订阅）跟 RLock 并发时才会短暂阻塞。在订阅远多于取消订阅的场景下（典型直播/聊天），读锁几乎不阻塞。如果真成为瓶颈，可以维护一个全局的"活跃频道集合"，Broadcast 前先检查频道是否存在订阅者，不存在直接跳过所有 shard，但这引入了跨 shard 一致性问题。

> **如果活跃频道只集中在几个 shard，其他 shard 白锁了，怎么优化？**

可以做二级索引：维护一个全局的 `activeChannels map[string]struct{}`，用原子操作标记哪些频道有订阅者。Broadcast 之前先检查频道是否活跃，不活跃跳过；只在活跃频道对应的 shard 上加 RLock。代价是增加了一个需要跨 shard 同步的数据结构。另一个思路是用 `sync.Map` 替代 `map + RWMutex`，在读多写少场景下 `sync.Map` 的 `Load` 是无锁的，但 `Store` 和 `Delete` 开销更大。

> **FNV-1a 哈希和 CRC32、MurmurHash 相比有什么优劣？**

项目选 FNV-1a 是因为实现极简（一个初始值+一趟异或乘法循环），不需要额外库。对于分片路由场景，FNV-1a 的分布性足够均匀。CRC32 也可以但稍慢（查表法需要 256 字节查表）。MurmurHash 分布性更好、对相近输入的雪崩效应更强，但实现复杂、不是标准库自带。分片路由不需要密码学安全性，所以不用 SHA256 这些。

---

### 追问路线二：写协程 & 背压

> **为什么每个连接要一个写协程？直接在 Hub.Broadcast 里写不行吗？**

不行。两个原因：第一，NBIO 的 WebSocket 连接不是线程安全的，如果多个 goroutine 同时对同一个连接写，会有并发写冲突，导致帧交错甚至 panic。第二，直接写在 Broadcast 持有 RLock 的路径上，如果某个客户端写阻塞（比如 TCP 缓冲满），会阻塞整个 Broadcast 流程，影响同 shard 所有订阅者的写入。独立写协程把写操作从 Broadcast 路径上解耦出来，Broadcast 只往 channel 里投递数据就返回，不阻塞。

> **writeCh 缓冲设了 256，怎么定的？**

256 是一个经验值，意味着在写入速度跟不上时，可以缓冲 256 条消息。如果超过 256，Write 方法走 default 分支丢弃消息并关闭连接。这是背压策略——宁可断开慢客户端也不能让内存无限增长。实际生产中可以根据消息大小和频率调整。如果单条消息 100 字节，256 条就是 25KB，很小的内存开销。如果单条 1MB，256 条就是 256MB，就需要变小或限制消息大小。

> **丢弃消息后直接 Close 连接，有没有更温和的策略？**

有的，可以根据场景分级：
1. 低优先级消息可丢弃，高优先级消息等一段时间（带 timeout 的 select）
2. 慢客户端检测：如果 writeCh 持续接近满，标记为慢客户端，只发送关键消息
3. 生产级系统（如 Centrifugo）有消息优先级：Publication 低优先级可丢，Join/Leave 等控制消息高优先级不可丢
4. 断开前可以先发一个 WebSocket Close 帧，而不是直接断开

> **Client.Close 里用 sync.Once，如果手动调 Close 之后 writeLoop 还在写，会不会 data race？**

不会。Close 先 `close(closeCh)`，这会让 writeLoop 在下一次 select 时收到信号退出。之后才 `conn.Close()`。writeLoop 退出后不会再访问 Conn。即使极端情况下 writeLoop 正在执行 WriteMessage，conn.Close() 会让 WriteMessage 返回 error，writeLoop 检查到 error 后也会执行 c.Close，但 sync.Once 保证只执行一次。整个流程是安全的。

> **Write 方法里 `case <-c.closeCh: return false` 这个分支什么时候命中？**

当连接正在关闭（Close 已被调用，closeCh 已关闭），但还有 Broadcast goroutine 尝试写入时。closeCh 关闭后所有 `<-c.closeCh` 立即返回，所以 Write 会返回 false，调用方知道消息丢弃了。比写到一个关闭的 channel 导致 panic 安全得多。

---

### 追问路线三：NATS & 跨节点

> **你说 NATS JetStream 保证消息不丢，具体机制是什么？**

JetStream 是 NATS 的持久化层。发布消息时，JetStream 把消息写入 Stream（类似日志），然后才返回 Ack 给发布者。消费者（Subscriber）取消息后必须手动 Ack（我用的是 ManualAck），如果消费者宕机或处理超时（AckWait 默认 30 秒），JetStream 会把消息重新投递给其他消费者。这样即使在发布到消费的中间环节出现故障，消息也不会丢失。

> **你用的是 WorkQueue 策略，和 InterestPolicy、LimitsPolicy 有什么区别？**

三种保留策略：
- **LimitsPolicy**：消息保留到超时或达到限制，不管有没有消费者。适合需要回放历史的场景（如 Kafka）
- **InterestPolicy**：有活跃消费者时才保留。消费者断开期间的消息丢失
- **WorkQueuePolicy**：消息被消费者 Ack 后就删除。每个消费者组内只投递一份，不会重复消费

我选 WorkQueue 是因为跨节点广播场景下，每条消息只需要被一个节点处理一次。但这里有个我最初没意识到的问题：WorkQueue 在多消费者时是负载均衡而非广播——每条消息只投递给一个消费者。而我需要的是所有节点都收到。解决方案是每个节点用独立的 Push Consumer，不共享同一个队列。

> **你刚才说发现了一个 bug，Publish 导致消息广播两次，具体流程是怎样的？**

原始代码中 Publish 处理流程：
1. 客户端发 Publish → 服务端收到
2. 服务端先做本地广播 `Hub.Broadcast(channel, data)` — 所有本地订阅者收到一次
3. 服务端再做 NATS 广播 `Broker.Publish(channel, data)`
4. NATS 订阅回调收到消息，再次做本地广播 `Hub.Broadcast(channel, data)` — 所有本地订阅者又收到一次

同一个客户端收到了两条一样的推送。修复方案是在 Publish 处理中只做 NATS 广播不做本地广播，由 NATS 回调统一处理：

```go
case *protocol.ClientMessage_Publish:
    // 只发到 NATS，不做本地广播
    go func() {
        s.Engine.AddHistory(ch, rawData, 100)
        s.Broker.Publish(ch, bytesOut)
    }()
```

NATS 回调中的 Broadcast 负责所有节点（包括发布节点自己）的本地广播。

> **但这样本节点发布的消息要绕一圈 NATS 才回到本地广播，延迟更高了。怎么优化？**

对，传统的 Pub/Sub 确实会多一跳延迟。优化方式：
1. 本节点写一个本地回调，绕过网络（NATS 支持 same-process 优化，发布的消息直接投递给同进程的订阅者）
2. 双写：本地广播后，NATS 广播加节点 ID 前缀，回调中检查如果是自己发的就跳过

方案 2 更简单实用：
```go
// 发布时带节点 ID
subject := "centrifugo.publish." + nodeID + "." + channel

// 订阅回调中过滤
channel := m.Subject // centrifugo.publish.<nodeID>.<channel>
// 提取 nodeID，如果等于本节点就跳过
```
本地广播立即执行，其他节点通过 NATS 广播。但没有做这个优化，简单起见选择统一走 NATS 回调。

> **NATS 连接断了怎么办？你的代码里 InitNATS 失败直接 log.Fatal 退出进程？**

对，这是最粗暴的处理方式——启动时如果连不上 NATS 直接挂。生产环境绝不能这样做。改进方向：
1. `nats.Connect` 用 `nats.ReconnectWait()` 和 `nats.MaxReconnects(-1)` 配置自动重连
2. 启动时不 Fatal，而是重试几次后降级运行（本地广播可用，跨节点不可用）
3. 运行中断连时，Publish 返回 error，记录日志但不崩溃

---

### 追问路线四：Redis 持久化

> **为什么在线状态用 Hash 而不是 Set？**

Hash 的 field 可以存结构化数据（JSON 序列化的 ClientInfo），Set 只能存字符串。如果只需要 clientID，Set 也够用，但 Hash 可以存更多元数据（userID、连接时间等），方便扩展。另外 Hash 的 HGetAll 比 SMEMBERS 多返回的是值而不仅仅是成员。

> **AddPresence 里 HSet 和 ExpireAt 不是原子操作，中间崩溃了怎么办？**

HSet 成功但 ExpireAt 没执行 → key 没有过期时间 → 永远不会自动清理 → 内存泄漏。或者 key 刚好在 HSet 之后 ExpireAt 之前过期被删除，ExpireAt 作用在一个空 key 上。解决方案是用 Lua 脚本合并：

```lua
redis.call('HSET', KEYS[1], ARGV[1], ARGV[2])
redis.call('EXPIREAT', KEYS[1], ARGV[3])
return 1
```

一次 RTT，原子执行，没有中间状态。

> **你用 Redis Stream 做消息历史，为什么不用 List 或 Sorted Set？**

对比一下：
- **List**：LPUSH + LTRIM 可以实现，但没有自动 MAXLEN，要手动裁剪；LTRIM 在元素多时性能差；没有消息 ID，要自己维护游标
- **Sorted Set**：用时间戳做 score，可以范围查询，但需要自己维护大小；ZADD + ZREMRANGEBYSCORE 组合，但要两步操作；每条消息要序列化做 member，score 给排序
- **Stream**：XADD + MAXLEN 一条命令自动裁剪（Approx 模式性能好）；天然消息 ID（时间戳+序号）；XRevRangeN 直接取最近 N 条；支持消费者组（留了扩展空间）

Stream 是 Redis 5.0+ 专门为消息流设计的，语义最贴合"保留最近 N 条历史消息"这个需求。

> **AddHistory 里每条消息都 XADD + EXPIRE 48 小时，如果频道持续活跃，Stream 永远不过期——这是问题吗？**

是的，Redis Stream 的 EXPIRE 是设在整个 key 上的，每次 Publish 都会刷新 TTL，所以活跃频道的 Stream 永远不会过期，内存不释放。但这不一定是 bug——活跃频道确实需要保留历史。问题是不活跃频道的 Stream 也会多存活 48 小时。如果频道数非常多（百万级），这可能导致 Redis 内存暴涨。改进方案：只在创建 Stream 时设一次 TTL，不每次都刷新；或者用定时任务扫描不活跃的 Stream 并清理。

> **Redis 客户端用 context.Background() 做所有操作的 context，有什么风险？**

context.Background() 永远不会被取消也不会超时。如果 Redis 慢或挂了，所有 Redis 操作会阻塞直到 Redis 客户端自身的超时（默认可能是无限等待或几分钟）。在异步 goroutine 中可能没事，但如果在主请求路径中（比如查询在线状态），会导致请求堆积。应该用 `context.WithTimeout` 给每次操作设合理超时：

```go
ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
defer cancel()
val, err := e.rdb.HGetAll(ctx, key).Result()
```

---

### 追问路线五：协议 & 序列化

> **Protobuf 的 oneof 你是怎么用的？为什么不用两个独立的消息类型？**

Protobuf 的 `oneof` 相当于联合类型（union），一个 `ClientMessage` 可以是 Subscribe 或 Publish，由 `Payload` 字段的类型决定。好处：
1. WebSocket 帧只需要一种消息类型，反序列化一次再 switch 判断类型
2. 扩展性强：加新消息类型（如 Unsubscribe、Ping）只需要在 oneof 里加字段
3. 二进制编码紧凑，oneof 只编码实际存在的字段

如果用两个独立类型，WebSocket 帧需要额外的方式区分消息类型（比如消息头增加 type 字段），相当于自己实现了一个 oneof，不如直接用 Protobuf 的 oneof。

> **Protobuf 编码后比 JSON 小多少？你家消息实际测试过吗？**

一条 SubscribeRequest（channel 名 10 字节）：JSON 约 `{"subscribe":{"channel":"bench_proto"}}` = 38 字节，Protobuf 约 12 字节（field tag + length + value），节省约 68%。PublishRequest 带 100 字节数据：JSON 约 130 字节（加上 key 名和 base64 编码开销），Protobuf 约 104 字节。Protobuf 还省去了 JSON 的字符串解析开销。

> **为什么选择 Binary WebSocket 帧而不是 Text 帧？**

Binary 帧直接传 Protobuf 的字节序列化结果，无需 base64 编码（JSON 传二进制数据需要 base64，膨胀 33%）。onMessage 里判断 `messageType != websocket.BinaryMessage` 直接丢弃 Text 帧，也防止了误发 JSON 的客户端干扰协议层。

> **`.proto` 文件的 go_package 你改成了什么？为什么？**

改成了 `cen-demo/protocol`。原来 `go_package = "."` 导致生成的 `.pb.go` 包名是空字符串（`package __`），Go 不允许包名为空。改为完整模块路径后，生成的代码包名是 `protocol`，import 路径是 `cen-demo/protocol`，和其他文件中的 import 一致。这是 `protoc --go_out` 加 `paths=source_relative` 后的标准做法。

---

### 追问路线六：架构设计 & 生产化

> **你的代码里 NewServer 硬编码创建 Hub、Broker、Engine，这样有什么问题？**

违反了依赖倒置原则。Server 直接依赖 Hub、NatsBroker、RedisEngine 的具体实现，导致：
1. 无法替换实现——比如测试时要 mock Redis，必须改 Server 代码
2. 无法单独测试——每次测 Server 都要启动 Redis 和 NATS
3. 违反开闭原则——加新的 Engine 实现（比如 etcd）要改 Server

应该用依赖注入：
```go
func NewServer(h *hub.Hub, b Broker, e Engine) *Server {
    return &Server{Hub: h, Broker: b, Engine: e}
}
```

> **Engine 接口设计有 inconsistence——AddPresence 没有 error 返回，AddHistory 有，为什么？**

设计不统一，是疏忽。AddPresence 调用 Redis HSet + ExpireAt，这两步都可能失败（网络错误、Redis 宕机），但返回值是 void，调用者无法感知失败。在线状态丢失意味着客户端在线但其他节点查不到。所有 Engine 方法都应该返回 error：

```go
type Engine interface {
    AddPresence(channel string, info types.ClientInfo, expireAt int64) error
    RemovePresence(channel, clientID string) error
    Presence(channel string) ([]types.ClientInfo, error)
    AddHistory(channel string, msg []byte, limit int64) error
    History(channel string, limit int) ([][]byte, error)
}
```

> **你项目里全局变量很多——config.C、internal.NC、internal.JS，这在测试和部署时有什么问题？**

全局可变状态的问题：
1. 测试困难——无法注入 mock，每次测试都要连真实 Redis/NATS
2. 无法并发测试——多个测试用例共享全局状态，互相干扰
3. 无法多实例——一个进程只能有一套配置
4. goroutine 安全问题——虽然是只写一次读多次，但编译器不保证 happen-before

改进：把全局变量封装为结构体，通过参数传递：

```go
type App struct {
    Config  *config.Config
    NATS   *nats.Conn
    JS     nats.JetStreamContext
}
```

> **如果流量突然增长 10 倍，你的服务最先撑不住的是哪个环节？**

按瓶颈排序：
1. **Hub.Broadcast 遍历 256 shard**——单次广播锁 256 次，高频频道（比如系统公告频道）会被频繁 Broadcast，RLock 争用增加。解决：活跃频道索引，跳过空 shard
2. **Redis 连接瓶颈**——单个 Redis 客户端连接，Publish 请求的 AddHistory 走异步 goroutine，但 goroutine 数量无限制，Redis 连接池可能耗尽。解决：worker pool 限制并发，或 Redis 集群
3. **NATS 带宽**——每条消息都通过 NATS 广播，万级 QPS × 消息大小可能打满 NATS 节点。NATS 本身很快（百万 msg/s），但 JetStream 持久化有磁盘 IO 瓶颈
4. **writeCh 溢出**——如果大部分客户端都是慢客户端，Write 走 default 丢弃，大量连接被 Close 再重建，形成恶性循环。解决：消息大小限制、客户端限速

> **你提到学习了 Centrifugo 的设计，除了鉴权你还有什么没实现？**

主要缺：
1. **频道权限控制**：谁可以订阅、谁可以发布
2. **限流/配额**：单个客户端 pub 频率限制、单频道消息量限制
3. **消息确认机制**：客户端是否收到推送的回执
4. **Admin HTTP API**：外部服务推送消息的 API
5. **指标导出**：Prometheus metrics（连接数、消息 QPS、延迟分布）
6. **优雅重连**：客户端断线重连后恢复订阅（需要 session 恢复机制）
7. **消息去重**：NATS 回调中可能有重复消息（at-least-once delivery）

---

### 追问路线七：细节拷打

> **onOpen 里 UUID 生成 clientID，UUID v4 碰撞概率极低但非零，如果碰撞了会怎样？**

Hub.Add 会用 `clients[c.ID()] = c` 覆盖同 ID 的旧连接。旧连接的 Client 对象从 map 中消失但不会被 Close，旧连接的 writeLoop goroutine 泄漏。修复：Add 之前检查是否已存在同 ID 的 Client，存在则先 Close 再替换。或者使用更强的 ID 方案如 Snowflake ID。

> **if session := conn.Session(); session != nil 这段类型断言如果失败会怎样？**

不会 panic，因为用了 comma-ok 断言 `if c, ok := session.(*client.Client); ok`。如果 Session 为 nil（连接还没完成升级）或类型不对（不应该发生），if 不执行，连接被忽略。这是安全的写法。

> **onClose 中的 c.Close() 和 Hub.Remove(c.ID()) 顺序有关系吗？**

有关系。应该先 Remove 再 Close。因为 Remove 中会调用 c.GetSubscriptions() 获取频道列表来清理 subs，如果先 Close 导致连接断开触发了其他清理逻辑，GetSubscriptions 可能返回空列表。当前代码先 Remove 再 Close 是正确的。

但实际上还有一个遗漏——onClose 中没有调用 `Engine.RemovePresence`，在线状态只能靠 Redis Hash 的 TTL 60 秒自动过期。在这 60 秒内查询在线状态，会返回已断开的用户。这是一个 bug。

> **你的 Broker.Subscribe 参数类型是 interface{}，然后强转 *hub.Hub，为什么不直接用具体类型？**

为了降低耦合——Broker 包不导入 Hub 包，避免循环依赖。但 interface{} 是最弱的抽象，编译时无法检查类型安全。更好的做法是定义接口：

```go
type Broadcaster interface {
    Broadcast(channel string, data []byte)
}
```

Hub 天然实现这个接口，Broker 只依赖接口不依赖具体类型。

> **InitNATS 里每次启动都 DeleteStream 再 AddStream，如果两个节点同时启动会怎样？**

两个节点同时执行 DeleteStream，可能把对方刚创建的 Stream 删掉，然后各自 AddStream，导致短暂的服务不可用。如果第二个 AddStream 先完成，第一个节点再 DeleteStream 又删掉。这是启动时的竞态条件。解决：不要_DeleteStream，只用 `AddStream` + 检查 `already exists` 错误来确保 Stream 存在。修改或更新的字段用 `UpdateStream`。

> **ts.go 压测工具里用 gorilla/websocket，服务端用 NBIO，两端用了不同的 WebSocket 库，有影响吗？**

没有影响。WebSocket 是标准协议（RFC 6454），不管服务端和客户端用什么库实现，只要发送和接收符合协议的二进制/文本帧就行。gorilla/websocket 在客户端侧更成熟，而 NBIO 在服务端侧性能更好，各取所长。

> **protobuf 序列化中 `proto.Marshal` 的错误被你忽略了（`bytesOut, _ := proto.Marshal(pushMsg)`），这安全吗？**

不安全。proto.Marshal 失败时 bytesOut 为 nil，后面 Write(nil) 会向 WebSocket 写一个空帧。但这在实际中几乎不会发生——Protobuf 的 Marshal 只有在 required 字段缺失时才会失败，而 proto3 没有 required 字段。不过严谨的做法应该检查 error 并记录日志。

> **你的 config 是包级全局变量 `var C = Config{...}`，这个在 package init 时就执行了。如果有人 import 了 config 包但不需要默认值怎么办？**

这是个常见的 Go 配置反模式。import 就会初始化，无法跳过。而且 Config 是公开的 struct，任何包都能修改它（不是只读的）。改进方案：用函数 `Default()` 返回默认值，或者用 `sync.Once` 延迟初始化，或者从环境变量/配置文件加载。

> **如果我要给这个项目加一个"房间"概念（限制房间人数、只给房间内的人发消息），你会怎么设计？**

房间本质上就是频道加权限。当前订阅已经支持频道级别的消息分发，缺的是：
1. 订阅前鉴权：在 `case *protocol.ClientMessage_Subscribe` 中加权限检查，查询用户是否在房间成员列表中
2. 房间状态：在 Engine 中增加 `JoinRoom/LeaveRoom/RoomMembers` 方法，用 Redis Set 存储 `room:<id>:members`
3. 房间人数限制：在 JoinRoom 时 `SCARD` 检查是否超过上限
4. 新消息类型：proto 中增加 `JoinNotification/LeaveNotification`

核心改动在协议层（加 oneof 分支）和权限层（RoomService），Hub 的 Broadcast 逻辑不需要改——频道本质上就是房间。

---

### 追问路线八：性能 & 压测

> **你的压测工具 ts.go 能测出什么？有什么局限？**

能测出：消息吞吐量（msg/s）、连接建立速度、客户端收发延迟。

局限：
1. 只有 1 个 publisher，无法测多客户端并发写的性能
2. 没有延迟统计（消息从发出到收到的时间差）
3. 没有百分位延迟（P50/P99）
4. 没有重连逻辑，断了就退出
5. 统计是用 atomic 计数器每秒重置，看不到累计值和趋势
6. 所有连接跑在一个进程里，超过几万连接时客户端本身成为瓶颈

> **如果让你做性能优化，优先改什么？**

1. **Pool 复用 Protobuf 对象**：pool/pool.go 已实现但未使用，接入后减少 GC 压力
2. **Broadcast 跳过空 shard**：维护活跃频道索引，减少 256 次 RLock
3. **AddPresence 用 Redis Pipeline**：HSet + ExpireAt 合并一次 RTT
4. **NATS 本地回调优化**：避免本节点消息绕经 NATS 一圈
5. **NBIO Handler 调优**：调整 worker 数量、读缓冲大小
6. **消息大小限制**：onMessage 中检查 `len(data)` 超阈值直接断开

> **GC 对这个服务的影响大吗？你在代码里做了哪些减少 GC 压力的措施？**

影响主要在：
1. Protobuf 消息对象频繁分配和释放——每条消息创建 ClientMessage + Unmarshal + ServerMessage + Marshal，产生大量短生命周期对象
2. Hub.Broadcast 中遍历订阅者列表，如果频道订阅者多，会产生大量迭代

已做的减 GC 措施：
- sync.Pool 复用 Protobuf 对象（pool/pool.go，但未接入）
- writeCh 缓冲避免了每次写都创建 goroutine
- 原子计数器代替 map 遍历统计连接数

待做的：
- 接入 pool/pool.go
- Broadcast 中 data []byte 复用（当前每发给一个订阅者都传同一个 slice，因为只读所以安全）
- context 对象复用

> **sync.Pool 你用了但没接入，能解释一下 sync.Pool 的原理和注意事项吗？**

sync.Pool 是 Go 标准库的对象池，有两大特性：
1. **Get/Put 复用对象**：减少堆分配，降低 GC 压力
2. **GC 时清空**：每次 GC 都会把 Pool 中未使用的对象回收，所以 Pool 不适合做持久缓存，只适合临时对象复用

注意事项：
- Put 之前要 Reset 对象状态，否则拿到的是脏数据（项目中用 `proto.Reset(m)`）
- Pool 中的对象可能随时被 GC 回收，Get 可能返回 nil（需要 `New` 函数兜底）
- 不要 Pool 大对象（如 1MB 的 slice），GC 扫描成本高于分配收益
-并发安全，多 goroutine 可以同时 Get/Put

---

### 追问路线九：错误处理 & 可观测性

> **整个项目里几乎没有错误日志，onMessage 中 proto.Unmarshal 失败直接 return，你怎么调试线上问题？**

这是最大的短板之一。生产环境至少需要：
1. 结构化日志（`log/slog` 或 `zap`）：每条日志带 requestID、clientID、channel
2. 关键指标（Prometheus）：
   - `connections_total`（当前连接数）
   - `messages_received_total`（按类型分：subscribe/publish）
   - `messages_broadcast_total`
   - `redis_errors_total`
   - `nats_publish_errors_total`
   - `broadcast_duration_seconds`（直方图）
3. 分布式追踪（OpenTelemetry）：从消息接收到广播完成的链路追踪

> **优雅退出只做了 NBEngine.Shutdown，NATS 和 Redis 连接呢？**

没关闭。进程退出时 OS 会回收 TCP 连接，但不是优雅断开：
- NATS：服务端可能记录 "client disconnected unexpectedly"
- Redis：连接池中的空闲连接不会被正常归还

应该在 Server.Stop 中加入：
```go
func (s *Server) Stop() {
    // 1. 停止接受新连接
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    s.NBEngine.Shutdown(ctx)
    // 2. 关闭所有客户端连接
    s.Hub.CloseAll()
    // 3. 关闭 NATS
    internal.NC.Close()
    // 4. 关闭 Redis
    s.Engine.Close()
    // 5. Drain NATS subscription
}
```

> **如果线上 Redis 响应变慢（比如 500ms），你的服务会怎样？**

目前 AddPresence 和 AddHistory 是在 goroutine 中调用的，不会阻塞主流程。但：
1. goroutine 数量不受控，Redis 慢时 goroutine 会堆积，内存增长
2. Presence 查询（`Presence()` 方法）目前在 onMessage 路径上虽然没被调用，但如果未来加了查询接口就是同步阻塞
3. 如果 Redis 完全挂了，AddHistory 返回的 error 被忽略，消息历史丢失但服务主流程不受影响

改进：所有 Redis 操作用带超时的 context（3 秒），加 worker pool 限制并发 Redis 调用数。

> **你的程序启动后什么都不打印，怎么知道它正在运行？**

只有两句日志：`NBIO Server starting on :8000...` 和优雅退出时的 `Shutting down server...`。没有 periodically 的状态日志。生产环境至少应该：
1. 启动时打印配置（端口、Redis 地址、NATS 地址）
2. 定期打印状态（每 30 秒输出连接数、频道数、消息速率）
3. WebSocket 连接/断开事件日志（info 级别）
4. Protobuf 解析失败、Redis 操作失败等异常日志（warn/error 级别）