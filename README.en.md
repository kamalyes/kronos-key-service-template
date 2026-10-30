# kronos-key-service

[简体中文](./README.md) | **English**

The central key and id issuer: it implements the two services of the contract hub's `key` module.

| Service | What it does | Who calls it |
|---|---|---|
| `TempKeyService` | Issues a per-tenant, per-scenario RSA pair, hands the public key to the caller and keeps the private key server-side under a TTL | Frontends and services that encrypt sensitive fields |
| `IdBlockService` | Issues id blocks from a central sequence per namespace, for callers to buffer and serve locally | Every business service that needs distributed unique primary keys |

## Why this service is gRPC only

It registers **no HTTP handler**. The three shapes in this architecture:

| Shape | gRPC | Own HTTP | Business logic |
|---|---|---|---|
| This service (infrastructure) | yes | no | yes |
| A business service (`kronos-service-template`) | yes | yes | yes |
| A gateway (`kronos-gateway-template`) | no | yes | no |

Three reasons:

1. **It is a security property rather than a deployment preference.** `TempKeyDecrypt` takes ciphertext and returns plaintext. Its own HTTP entry point would add a path that can bypass the gateway's authorization, and forgetting a network policy does happen. With no handler registered, exposing this pod's HTTP port exposes no key endpoint at all.

2. **The HTTP API still exists, with its entry point in the gateway.** The `google.api.http` annotations in the contract still matter: the gateway's `AutoRegisterWithHealthCheck` discovers this service through gRPC server reflection, reads them and builds the routes inside its own process. The API still appears in the aggregated docs and now necessarily passes the gateway's authorization middleware.

3. **Reflection must therefore stay enabled** (`enable-reflection: true` in the resources file). Disabled, the gateway cannot discover this service and the symptom is a 404 for an endpoint whose service is plainly alive.

The cost: business endpoints cannot be curled here. Debug with `grpcurl` or through the gateway.

## Quick start

```bash
kopy new https://github.com/kamalyes/kronos-key-service-template -o ./kronos-key-service
```

`scripts/init.sh` then resolves the contract-hub dependency, tidies, builds and runs the tests.

### Prerequisite

**Redis is a hard dependency with no degraded path.** Temporary keys live only in Redis and the id sequence increments only there, so an unavailable Redis means issued keys cannot be stored, decryption cannot read them and allocation has no sequence. The service therefore **refuses to start** rather than coming up and failing every request, which would let callers believe it is alive, possibly pass a health check, and defer the failure to business traffic.

```bash
docker run -d --name redis -p 6379:6379 redis:7-alpine
go run .
```

### Debugging with grpcurl

```bash
# List the services, which also verifies reflection
grpcurl -plaintext localhost:9290 list

# Issue a temporary key. The tenant comes from metadata, never the request body
grpcurl -plaintext \
  -H 'x-tenant-id: demo-tenant' \
  -d '{"scenario":"KEY_SCENARIO_LOGIN","key_size":"RSA_KEY_SIZE_2048"}' \
  localhost:9290 kronos.api.key.TempKeyService/TempKeyIssue

# Claim a block of ids
grpcurl -plaintext \
  -H 'x-tenant-id: demo-tenant' \
  -d '{"namespace":"order","count":10}' \
  localhost:9290 kronos.api.key.IdBlockService/IdBlockAllocate
```

## Design notes

### The private key never appears in any message

The whole protocol has a single key field, `public_key_pem`. The private key is written straight into Redis under a TTL and is only ever used indirectly through `TempKeyDecrypt`, where the caller submits ciphertext and receives plaintext without touching it.

That constraint is the reason this service exists: handing the private key out makes the caller responsible for storing it, and a private key stored on a client is not stored at all. The protocol therefore leaves no field for it rather than relying on a convention.

The matching implementation rule: logs record the cache key, the scenario and the TTL, **never the entry**. Dumping a whole struct is the most common way a private key leaks, and logs usually outlive the key by a wide margin.

### The scenario goes into the key, not only into the value

The cache key is `<prefix>:<scenario>:<keyID>`. With the scenario only in the value, a request using the wrong scenario still reads the record and must be stopped by an application-level comparison — the kind of check that gets forgotten. In the key, a wrong scenario is simply a missing key.

The scenario inside the entry is still compared after reading. That redundant check guards against the key-construction logic being broken, for instance a changed separator making two scenarios collide.

### External errors collapse into one

The error detail of a key service is itself an attack surface. Telling apart a missing key, a wrong scenario and another tenant's key lets a caller enumerate key ids to probe which exist and what they are for. Externally everything is an internal error and the detail goes only to the log.

`TempKeyDestroy` is the one exception: it deliberately checks neither ownership nor existence, because checking would require reading the entry first and that turns the endpoint into an existence probe. Destruction is an idempotent delete, and another tenant removing a key id it guessed only expires that key early, which it would do anyway.

### The RSA pool is tiered by key size

RSA generation is pure CPU work: tens of milliseconds at 2048 bits, hundreds to over a thousand at 4096. Generating on demand lets concurrent requests saturate the CPU and slow each other, so **the whole endpoint's p99 degrades** rather than a few requests — easily misread as slow Redis, because no single request appears to do heavy work.

| Tier | Capacity | Cold start | Threshold | Why |
|---|---|---|---|---|
| 1024 | 32 | 4 | 12 | Fast to generate and deprecated |
| 2048 | 128 | 32 | 96 | The workhorse: large pool, high pre-fill |
| 4096 | 16 | **0** | 6 | Slow and rare; pre-filling would add seconds to startup |

An empty pool polls briefly for the background top-up, bounded by `PoolWaitTimeout`, before falling back to live generation. Falling straight through would make the pool useless exactly at peak, which is when it empties.

The `from_pool` log field is the only way to tell whether the pool is working: when it fails through an undersized capacity or a top-up that cannot keep up, the service still works and merely slows down quietly.

### The encrypt probe is refused in production

`TempKeyEncryptProbe` lets a caller verify the round trip without writing any crypto. But it encrypts by deriving the public key from the privately held one, which amounts to exposing the server's willingness to compute with that private key.

The refusal lives in the code rather than in not exposing the route at deploy time, because route configuration drifts and this constraint should not. The predicate is `IsProductionLevel()`, covering production level and above, rather than `IsProduction()`, which matches only that one name: a security switch should err wide.

### There is no database section

Temporary keys live in Redis under a TTL and the id sequence only needs an atomic increment, so neither is persisted. A table would need migrations, backups and a cleanup job, and **a backup full of RSA private keys is itself a risk** — backups are retained far longer than these keys should exist.

This template therefore ships no `gorm`, `go-sqlbuilder`, `go-pbmo` or `migrations`.

## Who calls it

A business service generated from `kronos-service-template` calls `IdBlockAllocate` for primary keys. Its `models/idsource.go` keeps the local block buffer and `bootstrap/idgen.go` wires the `BlockAllocator` to this service.

Why that service does not use Snowflake locally: the gateway derives the worker id as `GetWorkerId() % 32`, leaving 32 slots, and a Deployment pod name carries a random suffix with no ordinal, so the id falls back to a hostname hash and birthday collisions follow:

| Replicas | Chance some pair shares a worker id |
|---|---|
| 2 | 3.1% |
| 4 | 17.7% |
| 8 | **61.4%** |
| 16 | **99.0%** |

Two pods sharing one keep independent sequence counters and emit identical ids within the same millisecond, surfacing as intermittent primary-key conflicts that correlate with traffic and change shape on every restart. A central sequence removes the problem entirely: uniqueness no longer depends on instance count, pod name or worker id.

## Layout

```
kronos-key-service/
├── main.go
├── bootstrap/
│   ├── server.go       Startup; refuses to start without Redis
│   ├── services.go     gRPC only, no HTTP handler
│   └── logger.go       Log context fields, the only audit trail
├── constants/
│   ├── service_name.go Service name = config prefix, one source
│   └── cache.go        TTL, cache keys, pool and sequence parameters
├── models/
│   └── temp_key.go     The Redis entry, key-size mapping, key id
├── repository/
│   ├── temp_key_repository.go     Redis access with a double scenario check
│   └── id_sequence_repository.go  The central sequence per namespace
├── service/
│   ├── common/helper.go           Tenant resolution and error collapsing
│   └── key/
│       ├── keypool.go             The tiered RSA pre-generation pool
│       ├── temp_key_service.go    The four TempKeyService RPCs
│       └── id_block_service.go    IdBlockService
└── resources/
    └── kronos-key-service-dev.yaml   No database section
```

## The contract hub

Defaults to `github.com/kamalyes/kronos-share-proto-template@build`.

The `build` branch is what the hub template's CI renders, generates with protoc and verifies: its `master` is all kopy placeholders and not valid Go, so `go get` cannot fetch it. Both service templates consume one branch, so the `key` module's definition is identical by construction with no two copies to keep in sync.

A real project should generate its own hub from the template's `master` and pin a tag, since tracking a branch means an upstream commit silently changes the dependency and past builds cannot be reproduced.
