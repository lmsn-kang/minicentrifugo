# AGENTS.md — cen-demo

## What this is

A Go WebSocket pub/sub server (Centrifugo-style) using NBIO for transport, sharded Hub for connection management, NATS JetStream for cross-node broadcast, and Redis for presence/history. Protocol is Protobuf over binary WebSocket frames.

## Build & run

- **No `go.mod` exists yet.** Before building: `go mod init cen-demo && go mod tidy`
- Run requires NATS (`nats://127.0.0.1:4222`) and Redis (`127.0.0.1:6379`). Defaults are in `config/config.go`.
- Main server: `go run main.go` — listens on `:8080` (config says 8000 but `main.go` calls `Start(":8080)`)
- Load tester: `go run ts.go -c <concurrency>` — connects to `ws://127.0.0.1:8000/ws` (NBIO address). Defaults to 2000 connections.
- `docker-compose.yaml` exists but is **empty** — no containers defined yet.

## Known compilation issues

These files will not compile as-is. Do not assume the codebase builds cleanly:

- **`client/client.go`** — multiple bugs: `[]bytes` should be `[]byte` (L16); `writeCh`/`closeCh` referenced without `c.` receiver prefix; `writeLoop()` called without receiver; `Write` method has broken select syntax; unused import `google.golang.org/grpc/channelz`.
- **`engine/engine.go`** — `Engine` interface references `types.ClientInfo` but the `types` package is not imported.
- **`engine/redis_engine.go`** — imports `cen-demo/config` but also references `types.ClientInfo` without importing `cen-demo/type`.
- **`pool/pool.go`** — incomplete stub (file ends mid-declaration at `var`).
- **`protocol/client.pb.go`** — generated protobuf code has `package __` (invalid). The `.proto` has `option go_package = "."` which produces a blank package name. Regenerate with a proper `go_package`.
- **`type/`** — directory name is `type` but the package declares `package types`. Go convention prefers matching; tools may complain.

## Regenerating protobuf

```bash
protoc --go_out=. --go_opt=paths=source_relative client.proto
```

Run from `protocol/`. The `.proto` `go_package` should be changed from `"."` to a real package path like `"cen-demo/protocol"` before regenerating.

## Architecture map

```
main.go              → entrypoint: inits NATS, creates Server, starts on :8080
ts.go                → standalone load-test client (not imported by server)
server/server.go     → gin router + NBIO engine + websocket upgrader
server/websocket.go  → onOpen/onMessage/onClose handlers, protobuf dispatch
hub/hub.go           → sharded client registry (256 shards, FNV-1a hash)
hub/shard.go         → per-shard client map + channel subscription map
broker/nats_broker.go→ NATS JetStream publish/subscribe across instances
engine/engine.go     → Engine interface (presence + history)
engine/redis_engine.go → Redis-backed Engine implementation
client/client.go     → per-connection state + async write loop
internal/nats.go     → global NATS/JetStream connection init
config/config.go     → static config (port, redis addr, nats url)
protocol/            → .proto definition + generated .pb.go
pool/pool.go         → unfinished/stub (sync pool for protobuf messages)
type/type.go         → ClientInfo struct (package name: types)
```

## Key dependencies

- `github.com/gin-gonic/gin` — HTTP router
- `github.com/lesismal/nbio` — high-performance network I/O; websocket sub-package for WS
- `github.com/nats-io/nats.go` — NATS client + JetStream
- `github.com/redis/go-redis/v9` — Redis client
- `github.com/google/uuid` — client ID generation
- `github.com/gorilla/websocket` — used by `ts.go` load tester only
- `google.golang.org/protobuf` — protobuf runtime

## No tests

No `_test.go` files exist anywhere in the project.