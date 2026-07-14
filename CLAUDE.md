# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Pterodactyl Wings (v1.12.1-custom) — the server control plane for the Pterodactyl game server management panel. Written in Go 1.24. It manages game server instances via Docker containers, exposes an HTTP API (Gin framework), runs a built-in SFTP server, and handles backups/transfers.

This is a fork maintained by TheHiveHost with custom additions, based on upstream v1.12.1.

## Build & Development Commands

```bash
# Build for Linux (amd64 + arm64) — outputs to build/
make build

# Debug build with pprof (requires sudo, reads config.yml from cwd)
make debug

# Remote debug with Delve on :2345
make rmdebug

# Run all tests
go test ./...

# Run a specific package's tests
go test ./server/filesystem/...

# Run a single test
go test ./server/filesystem/... -run TestFilesystem_Writefile

# Clean build artifacts
make clean
```

## Architecture

```
cmd/            CLI entry points (cobra): root, configure, diagnostics
config/         Global config singleton (YAML, mutex-protected, config.Get()/config.Update())
environment/    ProcessEnvironment interface + Docker implementation
events/         Pub/sub event bus (state changes, resource updates)
internal/
  cron/         Scheduled tasks (activity upload, sftp events, stats)
  database/     SQLite via GORM (activity logs)
  ufs/          Custom Unix filesystem using openat2() — prevents path traversal
loggers/        Structured logging (apex/log)
parser/         Game server config file parser
remote/         Panel API client (HTTP)
router/         Gin HTTP routes + middleware + websocket + JWT auth
server/         Core server management
  backup/       S3 and local backup strategies
  filesystem/   File operations with disk quota enforcement
  installer/    Server installation orchestration
  transfer/     Node-to-node server transfer
sftp/           Built-in SFTP server (golang.org/x/crypto/ssh)
system/         Utilities (atomic types, rate limiters, locker, sink pools)
```

**Entry flow:** `wings.go` → `cmd.Execute()` → `cmd/root.go` loads config from `/etc/pterodactyl/config.yml`, initializes Docker, fetches servers from Panel API, starts SFTP (:2022) and HTTP (:8080) servers.

**Key patterns:**
- `server.Manager` holds all server instances with thread-safe access
- `environment.ProcessEnvironment` interface abstracts container runtimes (only Docker implemented)
- Global config accessed via `config.Get()` (returns immutable copy) and `config.Update()` (thread-safe writes)
- Heavy use of `sync.RWMutex`, worker pools (`gammazero/workerpool`), and `errgroup`
- JWT auth for both API endpoints and WebSocket connections
- `internal/ufs` provides chroot-like filesystem safety using `openat2()` syscall

## Custom TheHiveHost Additions

### File Fingerprints
- `GET /api/servers/:server/files/fingerprints` — file fingerprint endpoint for modpack validation
- `router/router_server_files.go:getServerFileFingerprints()` — supports `sha512` and `curseforge` algorithms
- `server/filesystem/curseforge.go` — CurseForge fingerprint (MurmurHash2, strips whitespace)

### Folder Size
- `POST /api/servers/:server/files/size` — calculate folder/file size
- `router/router_folder_size.go:getFolderSize()`

### Server Proxy Management
- `POST /api/servers/:server/proxy/create` — create nginx reverse proxy with optional SSL/Let's Encrypt
- `POST /api/servers/:server/proxy/delete` — delete nginx proxy config
- `router/router_server_proxy.go` — proxy CRUD + Let's Encrypt via `go-acme/lego`

### Installed Version Detection
- `GET /api/servers/:server/version` — detect installed Forge/NeoForge version or JAR hash
- `router/router_server_versions.go:getInstalledVersion()`

### Node Live Stats
- `GET /api/system/stats` — live host resource snapshot: CPU usage %/threads/model name, memory,
  disk (measured against `config.System.RootDirectory`, the volume holding server data), swap,
  disk I/O rate, network I/O rate.
- `router/router_system.go:getSystemStats()` — thin handler, just reads the cache.
- `system/stats.go` — background sampler (`StartStatsSampler`, started once from `cmd/root.go` next
  to the archive/backup directory setup) refreshes an in-memory snapshot every second via
  `github.com/shirou/gopsutil/v3`, so the HTTP handler never blocks on its own measurement window.
  `disk.IOCounters()` reports both a whole block device (e.g. `vda`) and each of its partitions
  (`vda1`, `vda15`, ...) as separate entries whose byte counts overlap — `resolveDiskDevice()`
  matches the longest mountpoint prefix (like `df`) to track exactly one device, not a sum across
  all of them.

## Testing

Tests use `github.com/stretchr/testify` with table-driven patterns. Most test coverage is in `server/filesystem/`, `system/`, `events/`, and `remote/`.

## Key Dependencies

- **Docker:** `github.com/docker/docker` — container lifecycle
- **HTTP:** `github.com/gin-gonic/gin` — routing and middleware
- **WebSocket:** `github.com/gorilla/websocket`
- **CLI:** `github.com/spf13/cobra`
- **ORM:** `gorm.io/gorm` with pure-Go SQLite (`github.com/glebarez/sqlite`)
- **Errors:** `emperror.dev/errors` (wraps stdlib errors with stack traces)
- **Archives:** `github.com/mholt/archives`, `github.com/klauspost/pgzip`
- **JWT:** `github.com/gbrlsnchs/jwt/v3`
- **ACME/Let's Encrypt:** `github.com/go-acme/lego/v4` (custom addition for proxy feature)

## Important Notes

- This runs on Linux only (Docker dependency, `openat2()` syscall). macOS builds will fail for `internal/ufs`.
- Config file default path: `/etc/pterodactyl/config.yml`
- Server state persists in `wings.db` (SQLite) for restart recovery.
- The module path is `github.com/pterodactyl/wings` (upstream), not a TheHiveHost path.
