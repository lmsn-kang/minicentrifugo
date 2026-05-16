# minicentrifugo

`minicentrifugo` 是一个用 Go 实现的轻量级 WebSocket pub/sub 服务原型，整体设计参考 Centrifugo 的核心思路：本机连接由 sharded hub 管理，跨节点广播走 NATS JetStream，在线状态和消息历史落到 Redis，客户端与服务端之间使用 Protobuf 二进制 WebSocket 帧通信。

这个项目更偏向学习和工程原型，重点展示高并发连接管理、频道订阅索引、异步写队列、跨节点消息分发和 Redis 状态存储这些实时系统里的核心模块。

## Features

- WebSocket binary protocol based on Protobuf
- NBIO-based HTTP/WebSocket transport
- Sharded hub for local connection and subscription management
- Per-client async write loop with bounded write queue
- NATS JetStream broker for cross-node publish fanout
- Redis-backed presence and history storage
- Simple load-test client for broadcast throughput checks

## Architecture

```text
client
  -> /ws WebSocket
  -> Protobuf ClientMessage
  -> server message dispatcher
  -> hub local subscription index
  -> Redis presence/history
  -> NATS broker for cross-node broadcast
  -> hub broadcast
  -> Protobuf ServerMessage
```

Main modules:

- `main.go`: server entrypoint
- `server/`: Gin + NBIO server and WebSocket event handling
- `client/`: connection state and async write loop
- `hub/`: 256-shard local client registry and channel subscription index
- `broker/`: NATS JetStream publish/subscribe adapter
- `engine/`: Redis presence and history implementation
- `protocol/`: Protobuf message definitions and generated Go code
- `worker/`: bounded worker pool for asynchronous Redis/broadcast tasks
- `ts.go`: local load-test client

## Protocol

Client messages:

- `subscribe`: subscribe current connection to a channel
- `publish`: publish binary payload to a channel

Server messages:

- `reply`: simple command acknowledgement
- `push`: channel message pushed to subscribers

The protocol definition is in `protocol/client.proto`.

## Requirements

- Go 1.22+
- NATS running at `nats://127.0.0.1:4222`
- Redis running at `127.0.0.1:6379`

Defaults are defined in `config/config.go`.

## Run

```bash
go mod tidy
go run main.go
```

The server listens on `:8000` by default.

Health/status endpoint:

```bash
curl http://127.0.0.1:8000/status
```

WebSocket endpoint:

```text
ws://127.0.0.1:8000/ws
```

## Load Test

```bash
go run ts.go -c 2000
```

The load tester opens multiple WebSocket connections, subscribes them to the same channel, and uses one publisher connection to broadcast messages periodically.

## Notes

This repository is still a demo implementation. Before using it as a production service, the broker fanout semantics, presence cleanup, error handling, authentication, observability, and automated tests should be strengthened.
