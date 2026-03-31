module github.com/<% .OrgName %>/<% .ProjectName %>

go <% .GoVersion %>

require (
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.29.0
	github.com/kamalyes/go-bizerror v0.0.0-20260814173213-9f68830eaadf
	github.com/kamalyes/go-cachex v0.4.1
	github.com/kamalyes/go-config v0.23.0
	github.com/kamalyes/go-logger v0.7.1
	github.com/kamalyes/go-rpc-gateway v1.9.6
	github.com/kamalyes/go-toolbox v0.17.0
	google.golang.org/grpc v1.80.0
	google.golang.org/protobuf v1.36.11
)

// 这个服务不连数据库，所以没有 gorm / go-sqlbuilder / go-pbmo
//
// 临时密钥只存 Redis 并随 TTL 过期：落库毫无意义——私钥的生命周期是分钟级，
// 建一张表要配迁移、备份、清理任务，而备份里躺着一堆私钥本身就是个风险。
// ID 号段同理，序列只需要一个原子自增，Redis 的 INCRBY 正好。
//
// [EN] No gorm, go-sqlbuilder or go-pbmo, because this service has no database. A temporary
// private key lives for minutes and belongs in Redis under a TTL; a table would need
// migrations, backups and a cleanup job, and a backup full of private keys is itself a risk.
// An id sequence only needs an atomic increment, which is what Redis INCRBY is.

// 这里故意不写协议中心的 require：由 scripts/init.sh 执行 go get 时自动加上
// 写一个占位版本（v0.0.0-000101...）反而更糟——go get 会先解析 go.mod 里已有的
// 版本约束，直接报 invalid version: unknown revision 000000000000 而拿不到目标版本
//
// [EN] The contract-hub require is deliberately absent and added by scripts/init.sh via
// go get. A placeholder version would be worse: go get resolves the existing requirement
// first and fails with "invalid version: unknown revision 000000000000".
