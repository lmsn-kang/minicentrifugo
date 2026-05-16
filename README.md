# minicentrifugo

`minicentrifugo` 是一个用 Go 实现的轻量级 WebSocket 发布订阅服务原型。项目整体参考 Centrifugo 的核心设计思路：本机连接由分片 Hub 管理，跨节点消息通过 NATS JetStream 分发，在线状态和消息历史写入 Redis，客户端与服务端之间使用 Protobuf 二进制 WebSocket 帧通信。

这个项目的重点不是做一个完整的生产级消息网关，而是把实时系统里几个关键环节拆开实现出来：连接管理、频道订阅索引、异步写队列、跨节点广播、Presence 在线状态和 History 消息历史。

## 核心特性

- 基于 WebSocket 的实时双向通信
- 使用 Protobuf 作为二进制协议格式
- 使用 NBIO 作为网络传输层
- 使用 256 个 shard 管理本机连接和订阅关系
- 每个客户端独立异步写队列，避免并发写 WebSocket
- 使用 NATS JetStream 做跨节点发布订阅
- 使用 Redis 存储在线状态和消息历史
- 提供简单压测脚本，便于观察广播吞吐

## 架构流程

```text
客户端
  -> /ws WebSocket 连接
  -> Protobuf ClientMessage
  -> server 消息分发
  -> hub 本机订阅索引
  -> Redis presence/history
  -> NATS 跨节点广播
  -> hub 本机广播
  -> Protobuf ServerMessage
```

## 目录结构

```text
.
├── broker/              # NATS JetStream 发布订阅封装
├── client/              # 单个 WebSocket 客户端连接状态
├── config/              # 静态配置
├── engine/              # Redis presence/history 实现
├── hub/                 # 分片连接管理和频道订阅索引
├── internal/            # NATS 初始化
├── pool/                # Protobuf 消息对象池
├── protocol/            # Protobuf 协议定义与生成代码
├── server/              # HTTP/WebSocket 服务和消息处理
├── type/                # 共享数据结构
├── worker/              # 简单 worker pool
├── main.go              # 服务入口
└── ts.go                # WebSocket 压测客户端
```

## 模块说明

`server` 负责启动 Gin + NBIO 服务，注册 `/status` 和 `/ws` 路由，并处理 WebSocket 的连接建立、消息接收和连接关闭。

`client` 保存单个连接的 ID、订阅频道、WebSocket 连接对象和写队列。所有下行消息先进入 `writeCh`，再由独立写循环串行写出。

`hub` 是本机连接和订阅关系的核心索引。它把 clientID 通过 FNV-1a hash 分到 256 个 shard，降低大量连接同时订阅、广播、断开时的锁竞争。

`broker` 负责把本机发布的消息写入 NATS，并订阅 NATS 中其他节点发来的消息，再回灌到本机 Hub 广播。

`engine` 抽象了状态存储能力。目前实现是 Redis：Presence 使用 Hash 存储在线客户端信息，History 使用 Redis Stream 保存频道消息。

## 协议

协议定义在 `protocol/client.proto`。

客户端消息：

- `subscribe`：订阅指定频道
- `publish`：向指定频道发布二进制消息

服务端消息：

- `reply`：命令执行结果
- `push`：频道广播消息

## 运行要求

- Go 1.22+
- NATS：`nats://127.0.0.1:4222`
- Redis：`127.0.0.1:6379`

默认配置在 `config/config.go` 中。

## 启动服务

```bash
go mod tidy
go run main.go
```

服务默认监听：

```text
:8000
```

状态接口：

```bash
curl http://127.0.0.1:8000/status
```

WebSocket 地址：

```text
ws://127.0.0.1:8000/ws
```

## 压测

```bash
go run ts.go -c 2000
```

压测脚本会创建多个 WebSocket 连接，统一订阅同一个频道，并由其中一个连接定时发布消息，用于观察广播吞吐和连接稳定性。

## 当前状态

这是一个实时发布订阅服务的工程原型，还没有补齐生产环境需要的完整能力。后续可以继续完善：

- 鉴权和用户身份系统
- 订阅权限控制
- Presence 断线清理
- NATS 广播语义优化
- 错误码和请求 ID
- Prometheus 指标和结构化日志
- 单元测试和集成测试
