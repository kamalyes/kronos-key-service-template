/*
 * @Author: kamalyes example@qq.com
 * @Date: 2026-10-30 00:00:00
 * @LastEditors: kamalyes example@qq.com
 * @LastEditTime: 2026-10-30 00:00:00
 * @FilePath: \kronos-key-service\bootstrap\services.go
 * @Description: gRPC 服务注册（本服务只提供 gRPC，不注册 HTTP handler）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package bootstrap

import (
	pb "github.com/kamalyes/kronos-share-proto-template/pb/key"
	gwglobal "github.com/kamalyes/go-rpc-gateway/global"
	"google.golang.org/grpc"
)

// registerGRPCServices 注册 gRPC 服务
//
// ============================================================================
// 本服务刻意只提供 gRPC，不注册 HTTP handler
// ============================================================================
//
// 对比一下三种服务在这个架构里的形态：
//
//   本服务（基础设施）      只有 gRPC。HTTP 面由网关代理
//   业务服务               gRPC + 自己的 HTTP handler（可直接暴露，也可走网关）
//   网关                   只有 HTTP，没有业务逻辑，靠 reflection 代理上面两者
//
// 为什么密钥服务要选最严的那一档：
//
//   1. 这是安全属性，不是部署偏好。TempKeyDecrypt 是一个「送密文进来、
//      拿明文出去」的接口。它一旦有自己的 HTTP 入口，就多了一条绕过网关
//      鉴权的可能路径——而「忘了配网络策略」这种事是会发生的。
//      不注册 handler 意味着即使有人把这个 Pod 的 HTTP 端口暴露出去，
//      那上面也没有密钥接口可打。
//
//   2. 协议里的 google.api.http 注解仍然有用。网关的
//      AutoRegisterWithHealthCheck 通过 gRPC server reflection 发现本服务，
//      读取注解，在**网关进程里**建出对应的 HTTP 路由。
//      也就是说 HTTP 接口照样存在、照样进聚合文档，只是入口在网关而不在这里，
//      于是必然经过网关的鉴权中间件。
//
//   3. 所以 reflection 必须开着（resources 里 enable-reflection: true）。
//      关掉它网关就发现不了本服务，表现为「接口 404 但服务明明活着」。
//
// 代价要知道：本服务没有 HTTP 入口，也就不能用 curl 直接调试业务接口。
// 调试走 grpcurl，或者经网关。README 里写了命令。
//
// [EN] Deliberately gRPC only. Compare the three shapes in this architecture: an
// infrastructure service like this one speaks gRPC and lets the gateway proxy its HTTP face; a
// business service exposes both; a gateway exposes only HTTP, holds no business logic and
// proxies the other two by reflection. A key service takes the strictest tier because it is a
// security property rather than a deployment preference: TempKeyDecrypt takes ciphertext and
// returns plaintext, so its own HTTP entry point would add a path that can bypass the gateway's
// authorization, and forgetting a network policy does happen. Without a registered handler,
// exposing this pod's HTTP port exposes no key endpoint at all. The google.api.http annotations
// in the contract still matter: the gateway discovers this service through gRPC server
// reflection, reads them and builds the routes inside its own process, so the HTTP API still
// exists and still appears in the aggregated docs, with its entry point in the gateway and
// therefore necessarily behind its authorization middleware. Reflection must consequently stay
// enabled, or the gateway cannot discover this service and the symptom is a 404 for an endpoint
// whose service is plainly alive. The cost is that business endpoints cannot be curled here;
// debug with grpcurl or through the gateway, as the README spells out.
func (s *KeyServiceGateway) registerGRPCServices() {
	s.gateway.RegisterService(func(server *grpc.Server) {
		pb.RegisterTempKeyServiceServer(server, s.tempKeySvc)
		pb.RegisterIdBlockServiceServer(server, s.idBlockSvc)
	})
	gwglobal.LOGGER.Info("gRPC services registered (gRPC only; the HTTP face is proxied by the gateway)")
}
