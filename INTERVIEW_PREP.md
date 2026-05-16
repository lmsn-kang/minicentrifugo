# 面试准备：cen-demo 项目速查手册

> 这个项目是一个类 Centrifugo 的 WebSocket Pub/Sub 服务器，核心技术栈：Go + NBIO + NATS JetStream + Redis + Protobuf。

---

## 一、你必须能手写的核心代码

### 1. 分片 Hub（Sharded Hub）— 最核心的数据结构

**为什么分片？** 单把 `sync.RWMutex` 在百万连接下锁竞争严重，分 256 个 shard 把写压力分摊。

**手写目标：** `hub.go` + `shard.go`

```go
// hub.go — FNV-1a 哈希选分片
const shardCount = 256 // 必须是 2 的幂，这样 mask = shardCount-1 可以用位与代替取模

type Hub struct {
    shards []*Shard
    mask   uint64
    count  uint64 // atomic，统计在线连接数
}

func (h *Hub) shardIndex(clientID string) uint64 {
    hash := uint64(14695981039346656037) // FNV-1a offset basis
    for _, b := range []byte(clientID) {
        hash ^= uint64(b)
        hash *= 1099511628211 // FNV-1a prime
    }
    return hash & h.mask // 等价于 hash % shardCount，但更快
}

func (h *Hub) Add(c *client.Client) {
    idx := h.shardIndex(c.ID())
    h.shards[idx].Add(c)
    atomic.AddUint64(&h.count, 1)
}

func (h *Hub) Remove(clientID string) {
    idx := h.shardIndex(clientID)
    h.shards[idx].Remove(clientID)
    atomic.AddUint64(&h.count, ^uint64(0)) // 等价于 -1
}
```

**面试考点：**
- 为什么 shard 数必须是 2 的幂？（位与代替取模，O(1)）
- FNV-1a 哈希怎么算？和 CRC32、MurmurHash 比？（分布均匀、实现简单）
- `atomic.AddUint64(&h.count, ^uint64(0))` 为什么不是 `-1`？（uint64 不能存负数，取反 = 减 1）
- Broadcast 时遍历所有 shard，会不会性能差？（每个 shard 锁范围小，并发度 = shard 数）

---

```go
// shard.go — 每个分片独立加锁
type Shard struct {
    mu      sync.RWMutex
    clients map[string]*client.Client       // clientID -> Client
    subs    map[string]map[string]bool       // channel -> clientID -> true
}

func (s *Shard) Subscribe(c *client.Client, channel string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    c.Subscribe(channel)         // Client 自身也记录订阅
    if _, ok := s.subs[channel]; !ok {
        s.subs[channel] = make(map[string]bool)
    }
    s.subs[channel][c.ID()] = true
}

func (s *Shard) Broadcast(channel string, data []byte) {
    s.mu.RLock()     // 读锁，允许多个 Broadcast 并发读
    defer s.mu.RUnlock()
    subscriberIDs, ok := s.subs[channel]
    if !ok { return }
    for id := range subscriberIDs {
        if c, exists := s.clients[id]; exists {
            c.Write(data)
        }
    }
}
```

**面试考点：**
- `subs` 为什么是 `map[string]map[string]bool` 而不是 `map[string][]string`？（O(1) 查找/删除 vs O(n) 遍历）
- Subscribe 用写锁，Broadcast 用读锁——为什么？（广播远多于订阅，读写锁提高并发读）
- Remove 时要清理该 client 在 subs 中的所有引用（否则内存泄漏）

---

### 2. 异步写循环（Write Loop）— 连接管理的精髓

**手写目标：** `client.go` 的 `writeLoop` 和 `Write` 方法

```go
type Client struct {
    id        string
    mu        sync.RWMutex
    channels  map[string]bool
    Conn      *websocket.Conn
    writeCh   chan []byte        // 异步写缓冲
    closeCh   chan struct{}      // 关闭信号
    closeOnce sync.Once          // 保证只关闭一次
}

func New(id string, conn *websocket.Conn) *Client {
    c := &Client{
        id:       id,
        Conn:     conn,
        channels: make(map[string]bool),
        writeCh:  make(chan []byte, 256), // 带缓冲，减少阻塞
        closeCh:  make(chan struct{}),
    }
    conn.SetSession(c)
    go c.writeLoop()  // 启动后台写协程
    return c
}

func (c *Client) writeLoop() {
    for {
        select {
        case data := <-c.writeCh:
            if err := c.Conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
                c.Close()
                return
            }
        case <-c.closeCh:
            return
        }
    }
}

func (c *Client) Write(data []byte) bool {
    select {
    case c.writeCh <- data:
        return true
    case <-c.closeCh:
        return false
    default:              // 缓冲满时丢弃 + 关闭，防止背压导致内存暴涨
        go c.Close()
        return false
    }
}

func (c *Client) Close() {
    c.closeOnce.Do(func() {
        close(c.closeCh)
        if c.Conn != nil {
            c.Conn.Close()
        }
    })
}
```

**面试考点：**
- 为什么不全在写协程里直接写，要用 channel？（WebSocket 不是线程安全的，多个 goroutine 同时写会 panic）
- `writeCh` 为什么要带 256 容量？（削峰：突发广播时缓冲 256 条消息，超则丢弃）
- `default` 分支做了什么？（背压保护：缓冲满了说明消费不过来，宁可关连接也不让内存无限增长）
- `sync.Once` 为什么必须？（防止 `close(closeCh)` 重复 close panic）
- 如果广播消息太频繁，writeCh 满了会怎样？（走 default → 关闭连接，这是有意的熔断策略）

---

### 3. Protobuf Proto 定义 + 序列化/反序列化

**手写目标：** `client.proto`

```protobuf
syntax = "proto3";
package protocol;
option go_go_package = "cen-demo/protocol";

message ClientMessage {
    oneof payload {
        SubscribeRequest subscribe = 1;
        PublishRequest publish = 2;
    }
}
message SubscribeRequest { string channel = 1; }
message PublishRequest { string channel = 1; bytes data = 2; }

message ServerMessage {
    oneof payload {
        PushMessage push = 1;
        ReplyMessage reply = 2;
    }
}
message PushMessage { string channel = 1; bytes data = 2; }
message ReplyMessage { string text = 1; }
```

**Go 中的使用：**
```go
// 解码
var req protocol.ClientMessage
proto.Unmarshal(data, &req)

switch payload := req.Payload.(type) {
case *protocol.ClientMessage_Subscribe:
    channel := payload.Subscribe.Channel
case *protocol.ClientMessage_Publish:
    channel := payload.Publish.Channel
    rawData := payload.Publish.Data
}

// 编码
msg := &protocol.ServerMessage{
    Payload: &protocol.ServerMessage_Push{
        Push: &protocol.PushMessage{Channel: ch, Data: rawData},
    },
}
bytesOut, _ := proto.Marshal(msg)
```

**面试考点：**
- `oneof` 的作用？（一条消息只携带一种 payload，节省带宽 + 类型安全）
- proto3 默认值问题？（未设字段 = 零值，无法区分 "未设置" 和 "设为零值"）
- bytes vs string？（bytes 是原始二进制，string 是 UTF-8；二进制数据必须用 bytes）
- `field_number = 1` 有什么讲究？（1-15 只占 1 字节 varint 编码，高频字段应使用小编号）

---

### 4. NATS JetStream 发布/订阅

**手写目标：** `nats_broker.go`

```go
func (b *NatsBroker) Publish(channel string, data []byte) error {
    subject := "centrifugo.publish." + channel
    _, err := internal.JS.Publish(subject, data)
    return err
}

func (b *NatsBroker) Subscribe(h interface{}) {
    subject := "centrifugo.publish.>"
    internal.JS.Subscribe(subject, func(m *nats.Msg) {
        channel := m.Subject[len("centrifugo.publish."):]
        h.(*hub.Hub).Broadcast(channel, m.Data)
        m.Ack()
    }, nats.ManualAck())
}
```

**面试考点：**
- NATS subject 的 `>` 通配符？（`>` 匹配单个层级末尾的所有 token，如 `centrifugo.publish.>` 匹配 `centrifugo.publish.chat.room1` 但只匹配最后一段之后的内容）
- `ManualAck()` 的意义？（确认消息被处理后才从队列移除，防止丢失）
- Publish 为什么用 JetStream 而不是 Core NATS？（JetStream 有持久化，保证 at-least-once 投递）
- JetStream 的 WorkQueue 模式？（同组消费者只投递给一个，适合多实例部署——每个节点一个消费者）

---

### 5. Redis Presence + History

**手写目标：** `redis_engine.go`

```go
// Presence 用 Hash 存储：presence:<channel> -> {clientID: ClientInfoJSON}
func (e *RedisEngine) AddPresence(channel string, info types.ClientInfo, expireAt int64) {
    key := "presence:" + channel
    data, _ := json.Marshal(info)
    e.rdb.HSet(e.ctx, key, info.ClientID, data)
    e.rdb.ExpireAt(e.ctx, key, time.Unix(expireAt, 0))
}

func (e *RedisEngine) RemovePresence(channel, clientID string) {
    e.rdb.HDel(e.ctx, "presence:"+channel, clientID)
}

func (e *RedisEngine) Presence(channel string) ([]types.ClientInfo, error) {
    val, err := e.rdb.HGetAll(e.ctx, "presence:"+channel).Result()
    // 遍历 val，json.Unmarshal 每条记录
}

// History 用 Stream 存储：history:<channel>，限制最大条数
func (e *RedisEngine) AddHistory(channel string, msg []byte, limit int64) error {
    return e.rdb.XAdd(e.ctx, &redis.XAddArgs{
        Stream: "history:" + channel,
        MaxLen: limit,   // 裁剪到 limit 条
        Approx: true,    // 近似裁剪，性能更好
        Values: map[string]interface{}{"data": msg},
    }).Err()
}

func (e *RedisEngine) History(channel string, limit int) ([][]byte, error) {
    res, _ := e.rdb.XRevRangeN(e.ctx, "history:"+channel, "+", "-", int64(limit)).Result()
    // 提取每条 streamMsg.Values["data"]
}
```

**面试考点：**
- Presence 为什么用 Hash 而不是 Set？（Hash 可以存 clientID→JSON，Set 只能存成员）
- `ExpireAt` 的作用？（自动清理过期在线状态，替代心跳 TTL）
- History 为什么用 Stream 而不是 List？（Stream 支持 XReadGroup 多消费者、XRange 时间范围查询、MaxLen 近似裁剪）
- `Approx: true` 是什么？（MAXLEN ~ limit，Redis 不精确裁剪到约 limit 条，避免每次写入都遍历整个 stream）
- `HGetAll` 在大 channel 下会不会阻塞？（会，生产环境应考虑 HScan）

---

### 6. WebSocket 连接生命周期

**手写目标：** `server/websocket.go`

```go
func (s *Server) onOpen(conn *websocket.Conn) {
    clientID := uuid.New().String()
    c := client.New(clientID, conn)
    s.Hub.Add(c)
    conn.SetReadDeadline(time.Now().Add(10 * time.Minute))
}

func (s *Server) onClose(conn *websocket.Conn, err error) {
    if session := conn.Session(); session != nil {
        if c, ok := session.(*client.Client); ok {
            s.Hub.Remove(c.ID())
            c.Close()
        }
    }
}

func (s *Server) onMessage(conn *websocket.Conn, mt websocket.MessageType, data []byte) {
    conn.SetReadDeadline(time.Now().Add(10 * time.Minute)) // 续命

    if mt != websocket.BinaryMessage { return } // 只接受二进制帧

    cli := conn.Session().(*client.Client)       // 从连接取出 Client

    var req protocol.ClientMessage
    if proto.Unmarshal(data, &req) != nil { return }

    switch payload := req.Payload.(type) {
    case *protocol.ClientMessage_Subscribe:
        // ... 订阅频道 + 写 presence
    case *protocol.ClientMessage_Publish:
        // ... 存历史 + 本地广播 + NATS 跨节点广播
    }
}
```

**面试考点：**
- `SetReadDeadline` 为什么每次收到消息都续？（实现空闲超时：10 分钟无消息就断开）
- `conn.Session()` 是什么模式？（将自定义状态绑定到连接，类似 `context.Value`，用类型断言取出）
- Binary vs Text 帧的区别？（Binary = opcode 0x02，Protobuf 总是用 Binary）
- Publish 为什么用 `go func()` 异步？（写 Redis + 序列化 + 广播都比较耗时，不应阻塞读循环）

---

### 7. Protobuf 对象池（sync.Pool）

**手写目标：** `pool/pool.go`

```go
var clientMsgPool = sync.Pool{
    New: func() any { return &protocol.ClientMessage{} },
}

func GetClientMessage() *protocol.ClientMessage {
    return clientMsgPool.Get().(*protocol.ClientMessage)
}

func PutClientMessage(m *protocol.ClientMessage) {
    proto.Reset(m) // 清空消息内容，防止下次 Get 拿到脏数据
    clientMsgPool.Put(m)
}
```

**面试考点：**
- `sync.Pool` 什么时候回收？（GC 时随时可能清除，不能用来做持久存储）
- 为什么 Put 前要 `proto.Reset`？（否则下次 Get 拿到旧数据 → 反序列化结果错误）
- `sync.Pool` 的性能收益？（减少 GC 压力，避免频繁分配 Protobuf 大对象）

---

## 二、面试高频知识点速查

### Go 并发原语

| 原语 | 本项目用途 | 手写要点 |
|------|-----------|---------|
| `sync.RWMutex` | Shard 的读写锁 | 写操作加写锁，广播加读锁；不要在持有锁时做 I/O |
| `sync.Once` | Client.Close() | 防止重复 close channel panic |
| `atomic` | Hub 连接计数 | `atomic.AddUint64` 做 +1/-1，比 Mutex 轻量 |
| `chan []byte` | 异步写缓冲 | 带缓冲 chan + select default = 背压保护 |
| `sync.Pool` | Protobuf 消息对象池 | GC 会清除，必须 Reset 后 Put |

### 网络/WebSocket

| 概念 | 说明 |
|------|------|
| NBIO | Go 高性能网络库，基于 epoll/kqueue，比标准 net/http 处理更多连接 |
| WebSocket 帧类型 | Text(opcode 0x01) vs Binary(opcode 0x02)；Protobuf 用 Binary |
| 读超时续命 | 每次收到消息 `SetReadDeadline`，空闲超过阈值自动断开 |
| 异步写 | 单 goroutine 通过 channel 序列化写操作，避免并发写 panic |

### 分布式组件

| 组件 | 角色 | 关键 API |
|------|------|---------|
| NATS JetStream | 跨节点消息广播 | `JS.Publish`, `JS.Subscribe`, `ManualAck`, WorkQueue |
| Redis Hash | 在线用户列表(Presence) | `HSet`, `HDel`, `HGetAll`, `ExpireAt` |
| Redis Stream | 历史消息 | `XAdd`(MaxLen近似裁剪), `XRevRangeN` |

---

## 三、面试官可能的追问及回答

### Q: 为什么不直接用 `sync.Mutex` 而用 `sync.RWMutex`？
> Broadcast 的频率远高于 Subscribe/Unsubscribe，读多写少场景用 RWMutex 让多个广播并发读，提高吞吐。

### Q: 如果一个 channel 有 100 万订阅者，Broadcast 怎么优化？
> 1. shard 已经在分摊了（256 shard 各自遍历自己那部分）
> 2. 可以在每个 shard 内用扇出：启动多个 goroutine 并发写给本 shard 的订阅者
> 3. 更进一步：用写合并（batch write）或换成 epoll 批量写

### Q: Hub 删除连接时，如果 Broadcast 正在遍历，会不会漏删？
> 不会。Remove 持写锁，Broadcast 持读锁，两者互斥。遍历期间不会删除，删除期间不会遍历。

### Q: writeCh 满了丢弃连接，会不会太激进？
> 这是背压（backpressure）策略。如果客户端消费速度跟不上生产速度，缓存只会越来越大最终 OOM。提前断开是合理的熔断策略。

### Q: NATS 和 Redis 各自的职责？能不能只用一个？
> - NATS：跨节点实时广播（pub/sub），低延迟，不持久化（JetStream 持久化）
> - Redis：Presence（谁在线）+ History（消息记录），需要随机读写
> - 不能互相替代：NATS 不擅长 KV 查询，Redis Pub/Sub 没有持久化投递保证

### Q: 如果 Redis 挂了怎么办？
> 1. Presence 和 History 功能降级（不写 Redis，只做本地广播）
> 2. NATS 广播不受影响（仍可跨节点推送）
> 3. 生产环境需要 Redis Sentinel/Cluster 做高可用

### Q: Protobuf vs JSON，为什么选 Protobuf？
> - 二进制编码，体积小 3-5x
> - 解析速度比 JSON 快（不需要反射 + 字符串解析）
> - 强类型，oneof 区分消息类型
> - WebSocket Binary 帧天然适合二进制协议

### Q: `go func()` 异步执行的 goroutine 怎么控制？
> 当前是 fire-and-forget。生产环境应：
> 1. 用 Worker Pool（如 `ants`）限制并发 goroutine 数
> 2. 或用 semaphore (`chan struct{}`) 限制并发度
> 3. 加 recover 防止 panic 级联

### Q: 连接数 10 万时，系统中同时有多少 goroutine？
> - 每个 Client 1 个 writeLoop goroutine = 10 万
> - 加上 Go runtime 的 GOMAXPROCS 个 NBIO poller goroutine
> - 加上广播触发的临时 goroutine（go func）
> - 总计约 10 万 + 少量，Go 的 goroutine 很轻量（2KB 栈初始）

---

## 四、 мож口试编码练习

### 练习 1：从零手写 Sharded Hub（30 分钟）

要求：
1. 定义 Shard 结构（clients map + subs map + RWMutex）
2. 定义 Hub 结构（shards 数组 + FNV-1a 哈希选分片）
3. 实现 Add / Remove / Subscribe / Broadcast
4. Broadcast 用读锁，其余用写锁

### 练习 2：手写异步写循环（20 分钟）

要求：
1. Client 结构包含 Conn、writeCh、closeCh、closeOnce
2. writeLoop 从 writeCh 读数据并写入 WebSocket
3. Write 在 writeCh 满时走 default 分支触发 Close
4. Close 用 sync.Once 保证幂等

### 练习 3：手写 Protobuf 解码 + 消息分派（15 分钟）

要求：
1. 定义 ClientMessage 和 ServerMessage 的 proto
2. Go 侧 proto.Unmarshal 解码
3. switch oneof type 做消息分派
4. proto.Marshal 编码回复

### 练习 4：手写 Redis Presence（15 分钟）

要求：
1. AddPresence：HSet + ExpireAt
2. RemovePresence：HDel
3. Presence：HGetAll + json.Unmarshal
4. 解释为什么用 Hash 而不是 Set

---

## 五、项目架构一图流

```
Client (WebSocket/Protobuf)
    │
    ▼
┌── NBIO Engine (epoll/kqueue) ──┐
│  onOpen → client.New → Hub.Add│
│  onMessage → proto.Unmarshal  │
│  onClose → Hub.Remove         │
└──────────┬────────────────────┘
           │
     ┌─────┼─────────┐
     ▼     ▼         ▼
   Hub    Engine    Broker
  (本地)  (Redis)  (NATS)
     │     │         │
     │  Presence    跨节点
     │  History    广播
     │     │         │
     ▼     ▼         ▼
  Broadcast  └───►  Subscribe()
  → 写入      → 遍历全部分片
  writeCh           → 找到订阅者
                    → 调用 Write()
```

---

## 六、关键代码位置索引

| 功能 | 文件 | 关键行 |
|------|------|-------|
| 分片哈希 | `hub/hub.go` | `shardIndex` — FNV-1a |
| 读写锁广播 | `hub/shard.go` | `Broadcast` — RLock |
| 异步写循环 | `client/client.go` | `writeLoop` + `Write` |
| 背压/熔断 | `client/client.go` | `Write` 的 default 分支 |
| 连接建立 | `server/websocket.go` | `onOpen` |
| 消息分派 | `server/websocket.go` | `onMessage` — switch oneof |
| NATS 广播 | `broker/nats_broker.go` | `Publish` + `Subscribe` |
| Redis Presence | `engine/redis_engine.go` | `AddPresence` — HSet+ExpireAt |
| Redis History | `engine/redis_engine.go` | `AddHistory` — XAdd+MaxLen |
| 对象池 | `pool/pool.go` | `sync.Pool` + `proto.Reset` |
| Protobuf 定义 | `protocol/client.proto` | oneof 消息结构 |
| 优雅退出 | `main.go` | signal.Notify + Shutdown |