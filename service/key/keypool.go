/*
 * @Author: <% .AuthorName %> <% .AuthorEmail %>
 * @Date: <% nowFmt "2006-01-02" %> 00:00:00
 * @LastEditors: <% .AuthorName %> <% .AuthorEmail %>
 * @LastEditTime: <% nowFmt "2006-01-02" %> 00:00:00
 * @FilePath: \<% .ProjectName %>\service\key\keypool.go
 * @Description: RSA 密钥对预生成池，按密钥长度分档
 *
 * Copyright (c) <% .Year %> by <% .AuthorName %>, All Rights Reserved.
 */
package key

import (
	"fmt"
	"time"

	"github.com/<% .OrgName %>/<% .ProjectName %>/constants"
	cachex "github.com/kamalyes/go-cachex"
	gwglobal "github.com/kamalyes/go-rpc-gateway/global"
	"github.com/kamalyes/go-toolbox/pkg/sign"
)

// 为什么需要预生成池
//
// RSA 生成是纯 CPU 工作，2048 位在普通机器上几十毫秒，4096 位几百毫秒到秒级。
// 签发接口如果每次现场生成，高并发下 N 个请求同时占满 CPU、互相拖慢，
// 现象是整个签发接口的 p99 一起恶化，而不是某几个请求慢——很容易被误判成
// 「Redis 慢」或「网络抖动」，因为每个请求自己看起来都没做什么重活。
//
// 池把生成挪到后台：请求只是从一个切片里取走一个现成的密钥对。
// 代价是常驻内存里躺着若干私钥，所以池容量不能无限大，而且进程退出时
// 池里没用掉的密钥直接丢弃——它们从未被签发出去，丢掉没有任何影响。
//
// [EN] RSA generation is pure CPU work: tens of milliseconds at 2048 bits and hundreds to over
// a thousand at 4096. Generating on demand lets N concurrent requests saturate the CPU and slow
// each other, so the whole endpoint's p99 degrades rather than a few requests, which is easily
// misread as slow Redis or a network blip because no single request appears to do heavy work.
// The pool moves generation to the background and a request merely takes a ready pair from a
// slice. The cost is several private keys resident in memory, so the capacity is bounded, and
// whatever is left is discarded at exit — those keys were never issued, so dropping them costs
// nothing.

// TempKeyPem 池里存的裸 PEM 对
//
// 池里只放 PEM，不绑场景和租户：绑了就只能给那一个场景用，池的命中率会被切碎。
// 场景与租户在取出之后由 service 层绑定并写入 Redis。
//
// [EN] The pool holds bare PEM pairs without a scenario or tenant: binding them would restrict
// each entry to one scenario and fragment the hit rate. The service layer binds both after
// taking a pair and writes the result to Redis.
type TempKeyPem struct {
	PublicKeyPem  string
	PrivateKeyPem string
}

// keyPools 按密钥长度索引的池集合
type keyPools struct {
	pools map[sign.RsaKeySize]*cachex.ObjectPoolManager[TempKeyPem]
}

// Get 按长度取一个预生成的密钥对
//
// 池空时短轮询等待后台补充，最多等 constants.PoolWaitTimeout。
//
// 为什么要等而不是立刻回退实时生成：池被取空恰恰发生在高峰，而高峰正是
// 最不该让 N 个请求同时做 RSA 运算的时候。等几十到两百毫秒，大概率等到
// 后台补上来；等不到再实时生成，那时并发量已经被这段等待摊平了一些。
//
// 返回 false 表示没取到，调用方负责实时生成——不要在这里直接生成，
// 池的职责是「提供预生成的」，回退策略属于调用方的决定。
//
// [EN] On an empty pool this polls briefly for the background top-up, bounded by
// PoolWaitTimeout. Waiting beats falling straight through, because the pool empties exactly at
// peak, which is when N simultaneous RSA computations are least affordable; a short wait usually
// catches the refill, and by the time it gives up the burst has been partly flattened. A false
// return leaves live generation to the caller: the pool's job is to supply pre-generated pairs,
// and the fallback is the caller's decision.
func (p *keyPools) Get(keySize sign.RsaKeySize) (TempKeyPem, bool) {
	pool, ok := p.pools[keySize]
	if !ok {
		// 没注册这一档：直接让调用方实时生成，不要静默换一个长度。
		// 换长度会让调用方拿到与请求不符的密钥，而响应里没有长度字段可供发现。
		// [EN] An unregistered size falls through to live generation rather than silently
		// substituting another: the caller would get a key that does not match the request and
		// the response has no size field to reveal it
		return TempKeyPem{}, false
	}
	if v, ok := pool.TryGet(); ok {
		return v, true
	}
	deadline := time.Now().Add(constants.PoolWaitTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		if v, ok := pool.TryGet(); ok {
			return v, true
		}
	}
	return TempKeyPem{}, false
}

// Stop 停止所有池的后台补充
//
// 必须在进程退出时调用，否则后台 goroutine 会继续生成密钥，
// 让进程关不干净——容器里表现为 SIGTERM 之后要等到 grace period 结束才被 KILL。
//
// [EN] Must be called at exit, or the background goroutines keep generating keys and the
// process does not shut down cleanly: the container then waits out its grace period.
func (p *keyPools) Stop() {
	for _, pool := range p.pools {
		_ = pool.Stop()
	}
}

// registerKeyPools 注册三档密钥池
func registerKeyPools() *keyPools {
	log := gwglobal.GetLogger()

	// capacity 池容量 | coldStart 启动时同步预填数 | minThreshold 触发补充的水位 | batchSize 每次补多少
	//
	// coldStart 是唯一会拖慢启动的参数：它在启动时同步生成。2048 预填 32 个
	// 大约是一秒量级，可以接受；4096 刻意设 0，预填它会让启动多等好几秒，
	// 而它是低频档位，用纯异步补足够。
	//
	// [EN] coldStart is the only parameter that delays startup, since it generates
	// synchronously. Pre-filling 32 keys at 2048 bits costs about a second, which is
	// acceptable; 4096 is deliberately zero, because pre-filling it would add several seconds
	// to startup for a rarely used tier that an asynchronous top-up serves well enough.
	configs := []struct {
		size         sign.RsaKeySize
		capacity     int
		coldStart    int
		minThreshold int
		batchSize    int
	}{
		{sign.RsaKeySize1024, 32, 4, 12, 16},
		{sign.RsaKeySize2048, 128, 32, 96, 64},
		{sign.RsaKeySize4096, 16, 0, 6, 8},
	}

	pools := make(map[sign.RsaKeySize]*cachex.ObjectPoolManager[TempKeyPem], len(configs))
	for _, cfg := range configs {
		// 闭包捕获：cfg 是循环变量，Go 1.22 起每轮迭代是新变量，
		// 直接捕获是安全的。更早的版本必须在循环内 cfg := cfg。
		// [EN] Loop-variable capture is safe from Go 1.22, which gives each iteration a fresh
		// variable; earlier versions need an explicit copy inside the loop
		size := cfg.size
		pools[size] = cachex.RegisterObjectPool(
			fmt.Sprintf("temp_key_pool_%d", size),
			func() (TempKeyPem, error) { return generateTempKeyPem(size) },
			cachex.WithObjectPoolCapacity(cfg.capacity),
			cachex.WithObjectPoolColdStartCount(cfg.coldStart),
			cachex.WithObjectPoolMinThreshold(cfg.minThreshold),
			cachex.WithObjectPoolBatchSize(cfg.batchSize),
			cachex.WithObjectPoolRefreshInterval(constants.PoolRefreshInterval),
			cachex.WithObjectPoolLogger(log),
		)
	}
	return &keyPools{pools: pools}
}

// generateTempKeyPem 生成一对密钥并导出成 PEM
func generateTempKeyPem(keySize sign.RsaKeySize) (TempKeyPem, error) {
	pair, err := sign.GenerateRsaKeyPair(keySize)
	if err != nil {
		return TempKeyPem{}, err
	}
	pub, err := sign.ExportRsaPublicKeyToPEM(pair.PublicKey)
	if err != nil {
		return TempKeyPem{}, err
	}
	priv, err := sign.ExportRsaPrivateKeyToPEM(pair.PrivateKey)
	if err != nil {
		return TempKeyPem{}, err
	}
	return TempKeyPem{PublicKeyPem: pub, PrivateKeyPem: priv}, nil
}
