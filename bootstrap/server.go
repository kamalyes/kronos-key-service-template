/*
 * @Author: kamalyes example@qq.com
 * @Date: 2026-10-30 00:00:00
 * @LastEditors: kamalyes example@qq.com
 * @LastEditTime: 2026-10-30 00:00:00
 * @FilePath: \kronos-key-service\bootstrap\server.go
 * @Description: 服务启动引导
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package bootstrap

import (
	"fmt"

	"github.com/kamalyes/kronos-key-service/constants"
	"github.com/kamalyes/kronos-key-service/repository"
	keysvc "github.com/kamalyes/kronos-key-service/service/key"
	goconfig "github.com/kamalyes/go-config"
	gateway "github.com/kamalyes/go-rpc-gateway"
	gwglobal "github.com/kamalyes/go-rpc-gateway/global"
)

// KeyServiceGateway 服务启动器
type KeyServiceGateway struct {
	gateway *gateway.Gateway

	tempKeyRepo *repository.TempKeyRepository
	idSeqRepo   *repository.IDSequenceRepository

	tempKeySvc *keysvc.TempKeyService
	idBlockSvc *keysvc.IdBlockService
}

// NewKeyServiceGateway 创建服务启动器实例
func NewKeyServiceGateway() *KeyServiceGateway {
	return &KeyServiceGateway{}
}

// Run 启动服务
func (s *KeyServiceGateway) Run() error {
	// 配置前缀取 constants.ServiceName，不写字面量
	//
	// go-config 按 <前缀>-<环境别名>.yaml 匹配，所以前缀和 resources/ 下的文件名
	// 必须一致。两处各写一个字符串的话，改了一处忘了另一处就是启动即崩：
	//   ❌ 未找到前缀为 'xxx' 的配置文件
	// 而这个报错只在真正启动时出现，编译期毫无提示。都从同一个常量来，
	// 结构上就不可能分叉——resources/ 下的文件名也由同一个 kopy 变量渲染。
	//
	// [EN] go-config matches <prefix>-<env alias>.yaml, so the prefix and the file name under
	// resources/ must agree. With a literal in each place, changing one and forgetting the
	// other fails at startup with "config file with prefix not found" and nothing at compile
	// time. One constant makes divergence structurally impossible.
	gw, err := gateway.NewGateway().
		WithSearchPath("resources").
		WithEnvironment(goconfig.GetEnvironment()).
		WithPrefix(constants.ServiceName).
		WithHotReload(nil).
		Build()
	if err != nil {
		return fmt.Errorf("create gateway failed: %v", err)
	}
	s.gateway = gw

	// 先配日志上下文字段，之后所有启动日志和业务日志都带齐链路字段
	// [EN] Configure the log context fields first so every later log carries them
	configureLoggerContext()

	// Redis 是硬依赖，不像业务服务那样可以降级
	//
	// 临时密钥只存 Redis，ID 序列也只在 Redis 里自增。Redis 不可用时这个服务
	// 没有任何可降级的路径：签发出去的密钥存不下，解密读不到，发号拿不到序列。
	// 所以这里直接拒绝启动，而不是起来之后每个请求都失败——后者会让调用方
	// 以为服务是活的，健康检查也可能过，故障被推迟到业务请求上才暴露。
	//
	// [EN] Redis is a hard dependency with no degraded path: temporary keys live only there and
	// the id sequence increments only there, so an unavailable Redis means issued keys cannot be
	// stored, decryption cannot read them and allocation has no sequence. Startup is refused
	// outright rather than coming up and failing every request, which would let callers believe
	// the service is alive, possibly pass a health check, and defer the failure to business
	// traffic.
	if gwglobal.GetRedis() == nil {
		return fmt.Errorf("Redis 未配置：本服务的临时密钥与 ID 序列都依赖 Redis，无法降级启动" +
			" | Redis is not configured and this service cannot start without it")
	}

	gwglobal.LOGGER.Info("Initializing repositories...")
	s.tempKeyRepo = repository.NewTempKeyRepository()
	s.idSeqRepo = repository.NewIDSequenceRepository()

	// 密钥池的冷启动预填在这里发生，会阻塞约一秒
	//
	// 刻意放在启动阶段而不是首个请求：否则第一个用户承担全部预填成本，
	// 表现为「服务刚起来时第一个签发请求特别慢」，而那通常是健康检查之后
	// 第一个真实用户，体验最差的位置。
	//
	// [EN] The pool's cold start blocks here for about a second, deliberately at startup rather
	// than on the first request, which would make the first user pay the whole pre-fill and show
	// up as a very slow first issue right after the health check passes.
	gwglobal.LOGGER.Info("Initializing services (pre-generating RSA key pools)...")
	s.tempKeySvc = keysvc.NewTempKeyService(s.tempKeyRepo)
	s.idBlockSvc = keysvc.NewIdBlockService(s.idSeqRepo)

	gwglobal.LOGGER.Info("Registering gRPC services...")
	s.registerGRPCServices()

	// 池的后台 goroutine 要随进程退出停掉，否则容器要等到 grace period 结束才被 KILL
	// [EN] The pool's background goroutines must stop with the process, or the container waits
	// out its grace period
	defer s.tempKeySvc.Close()

	gwglobal.LOGGER.Info("KeyService Server is ready")
	return s.gateway.Run()
}
