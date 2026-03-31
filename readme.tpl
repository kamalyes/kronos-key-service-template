# <% .ProjectName %>

**简体中文** | [English](./README.en.md)

中心密钥与发号服务：实现协议中心 `key` 模块的两个 service。

| Service | 做什么 | 谁在调 |
|---|---|---|
| `TempKeyService` | 按租户 + 场景签发一次性 RSA 密钥对，公钥给调用方，私钥只留服务端并随 TTL 过期 | 需要加密敏感字段的前端/服务 |
| `IdBlockService` | 按命名空间集中发放 ID 号段，调用方本地缓冲后逐个发放 | 所有需要分布式唯一主键的业务服务 |

## 这个服务为什么是纯 gRPC

它**不注册任何 HTTP handler**。对比一下三种服务在这个架构里的形态：

| 形态 | gRPC | 自己的 HTTP | 业务逻辑 |
|---|---|---|---|
| 本服务（基础设施） | ✅ | ❌ | ✅ |
| 业务服务（`kronos-service-template`） | ✅ | ✅ | ✅ |
| 网关（`kronos-gateway-template`） | ❌ | ✅ | ❌ |

三个理由：

1. **这是安全属性，不是部署偏好。** `TempKeyDecrypt` 是「送密文进来、拿明文出去」的接口。它一旦有自己的 HTTP 入口，就多了一条绕过网关鉴权的可能路径，而「忘了配网络策略」这种事是会发生的。不注册 handler 意味着即使有人把这个 Pod 的 HTTP 端口暴露出去，那上面也没有密钥接口可打。

2. **HTTP 接口照样存在，入口在网关。** 协议里的 `google.api.http` 注解仍然有用：网关的 `AutoRegisterWithHealthCheck` 通过 gRPC server reflection 发现本服务、读取注解、在**网关进程里**建出路由。于是 HTTP 接口照样进聚合文档，只是必然经过网关的鉴权中间件。

3. **所以 reflection 必须开着**（`resources/<% .ProjectName %>-dev.yaml` 里 `enable-reflection: true`）。关掉它网关就发现不了本服务，表现为「接口 404 但服务明明活着」。

代价：不能用 `curl` 直接调试业务接口。调试走 `grpcurl` 或经网关，命令见下。

## 快速开始

```bash
kopy new https://github.com/<% .OrgName %>/kronos-key-service-template -o ./<% .ProjectName %>
```

生成后 `scripts/init.sh` 会自动解析协议中心依赖、`go mod tidy`、编译、跑测试。

### 前置依赖

**Redis 是硬依赖，没有降级路径。** 临时密钥只存 Redis，ID 序列也只在 Redis 里自增。Redis 不可用时签发出去的密钥存不下、解密读不到、发号拿不到序列，所以服务会**直接拒绝启动**而不是起来之后每个请求都失败——后者会让调用方以为服务是活的、健康检查也可能过，故障被推迟到业务请求上才暴露。

```bash
docker run -d --name redis -p 6379:6379 redis:7-alpine
go run .
```

### 用 grpcurl 调试

```bash
# 列出服务（验证 reflection 是否正常）
grpcurl -plaintext localhost:<% .GRPCPort %> list

# 签发临时密钥。租户从 metadata 取，不在请求体里
grpcurl -plaintext \
  -H 'x-tenant-id: demo-tenant' \
  -d '{"scenario":"KEY_SCENARIO_LOGIN","key_size":"RSA_KEY_SIZE_2048"}' \
  localhost:<% .GRPCPort %> <% .ProtoPackage %>.key.TempKeyService/TempKeyIssue

# 申领一段 ID
grpcurl -plaintext \
  -H 'x-tenant-id: demo-tenant' \
  -d '{"namespace":"order","count":10}' \
  localhost:<% .GRPCPort %> <% .ProtoPackage %>.key.IdBlockService/IdBlockAllocate
```

## 设计要点

### 私钥永不出现在任何消息里

整个协议只有 `public_key_pem` 一个密钥字段。私钥生成后直接写入带 TTL 的 Redis，之后只能经 `TempKeyDecrypt` 间接使用——调用方送密文、拿明文，全程接触不到私钥。

这条约束是这个服务存在的理由：私钥一旦下发，就变成「客户端保管私钥」，等于没有保管。所以协议层面不给这个字段留位置，而不是靠约定不要用。

配套的实现约束：日志里只记键名、场景和 TTL，**不记条目本身**。"顺手把整个结构体打出来"是私钥泄漏最常见的途径，而日志通常比密钥本身活得久得多。

### 场景进键名，不只是存在条目里

缓存键是 `<prefix>:<scenario>:<keyID>`，场景是键的一部分。只把场景写进 value 的话，用错场景的请求仍然能读到这条记录，得靠应用层比对才拦得住——而那道校验是会被漏写的。键里带上场景，用错场景直接就是 key 不存在。

读出来之后还会再比一次条目里的场景。这道冗余校验防的是「键名构造逻辑被改坏」，比如有人改了分隔符导致不同场景的键意外重合。

### 对外错误一律归并

密钥服务的错误信息本身就是攻击面。区分「密钥不存在」「场景不对」「不是你的租户」会让调用方能用枚举 keyID 的方式探测别人的密钥是否存在、属于哪个场景。所以对外统一返回内部错误，细节只进日志。

`TempKeyDestroy` 是唯一的例外——它刻意不校验归属也不校验存在性，因为要校验就得先读出条目，而那会把这个接口变成一个存在性探测器。销毁是幂等删除，别的租户删掉一个它猜中的 keyID，后果只是那个密钥提前失效，而密钥本来就会过期。

### RSA 预生成池按长度分档

RSA 生成是纯 CPU 工作：2048 位几十毫秒，4096 位几百毫秒到秒级。现场生成的话，高并发下 N 个请求同时占满 CPU、互相拖慢，现象是**整个签发接口的 p99 一起恶化**，而不是某几个请求慢——很容易被误判成「Redis 慢」，因为每个请求自己看起来都没做重活。

| 档位 | 容量 | 冷启动预填 | 补充水位 | 理由 |
|---|---|---|---|---|
| 1024 | 32 | 4 | 12 | 生成快、已不推荐使用 |
| 2048 | 128 | 32 | 96 | 主力档，大池 + 高预填 |
| 4096 | 16 | **0** | 6 | 生成慢且低频，预填会拖慢启动好几秒 |

池空时会短轮询等待后台补充（上限 `PoolWaitTimeout`），等不到才回退实时生成。不等就直接实时生成，等于池在高峰期形同虚设——而高峰正是池会被取空的时候。

日志里的 `from_pool` 字段是判断池是否在起作用的唯一依据：池失效（容量配小、补充跟不上）时服务功能完全正常，只是悄悄变慢。

### 加密自检接口在生产环境被拒

`TempKeyEncryptProbe` 让调用方不写一行加密代码就能验证闭环。但它用服务端持有的私钥推导出公钥再加密，等于把「服务端愿意用这把私钥做运算」暴露成一个接口。

代码里按环境直接拒绝，而不是靠部署时不暴露路由：路由配置会被改，而「生产不可用」这件事写在代码里才不会随配置漂移。判断用的是 `IsProductionLevel()`（生产级别及以上）而不是 `IsProduction()`（精确匹配 production 这一个名字）——安全开关要宽不要窄。

### 没有 database 段

临时密钥只存 Redis 并随 TTL 过期，ID 序列只需要一个原子自增，两者都不落库。落库要配迁移、备份和清理任务，而**数据库备份里躺着一堆 RSA 私钥本身就是风险**——备份的保留期通常远长于这些密钥该存在的时间。

所以这个模板不带 `gorm` / `go-sqlbuilder` / `go-pbmo` / `migrations`。

## 谁在调它

`kronos-service-template` 生成的业务服务会调 `IdBlockAllocate` 来做主键分发。那边的 `models/idsource.go` 维护本地号段缓冲，`bootstrap/idgen.go` 把 `BlockAllocator` 接到本服务。

为什么业务服务不自己用 Snowflake：网关的 `osx.GetWorkerIdForSnowflake()` 等于 `GetWorkerId() % 32`，只有 32 个槽位。Deployment 的 Pod 名带随机后缀、提取不出序号，于是退化成按主机名哈希取模，副本一多就是生日碰撞：

| 副本数 | 至少一对撞上 worker id |
|---|---|
| 2 | 3.1% |
| 4 | 17.7% |
| 8 | **61.4%** |
| 16 | **99.0%** |

撞上之后两个 Pod 各有独立序列计数器，同一毫秒会发出相同 ID，表现为偶发主键冲突——与流量相关、重启后碰撞组合还会变，极难排查。集中发号后唯一性只取决于中心序列，与实例数、Pod 名、worker id 全都无关。

## 目录结构

```
<% .ProjectName %>/
├── main.go
├── bootstrap/
│   ├── server.go       启动序列；Redis 不可用直接拒绝启动
│   ├── services.go      只注册 gRPC，不注册 HTTP handler
│   └── logger.go        日志上下文字段（审计的唯一线索）
├── constants/
│   ├── service_name.go  服务名 = 配置文件前缀，同源不可分叉
│   └── cache.go         TTL、缓存键、对象池与号段参数
├── models/
│   └── temp_key.go      Redis 条目；RSA 长度映射；keyID 生成
├── repository/
│   ├── temp_key_repository.go     Redis 存取 + 场景双重校验
│   └── id_sequence_repository.go  按命名空间的中心序列
├── service/
│   ├── common/helper.go           租户解析与错误归并
│   └── key/
│       ├── keypool.go             RSA 预生成池，按长度分档
│       ├── temp_key_service.go    TempKeyService 四个 RPC
│       └── id_block_service.go    IdBlockService
└── resources/
    └── <% .ProjectName %>-dev.yaml   无 database 段
```

## 协议中心

默认消费 `<% .ShareProtoModule %>@<% .ShareProtoVersion %>`。

`build` 分支是协议模板 CI 渲染 + protoc 生成 + 验证后的产物——它的 `master` 全是 kopy 占位符，不是合法 Go，`go get` 拉不动。两个服务模板指向同一个分支，所以 `key` 模块的定义天然一致，不需要人工保持两份 proto 同步。

真实项目应该用 kopy 从协议模板的 `master` 生成自己的协议中心仓库，并钉具体 tag——跟分支意味着上游一提交依赖就变了，复现不了历史构建。
