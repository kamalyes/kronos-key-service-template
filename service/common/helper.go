/*
 * @Author: <% .AuthorName %> <% .AuthorEmail %>
 * @Date: <% nowFmt "2006-01-02" %> 00:00:00
 * @LastEditors: <% .AuthorName %> <% .AuthorEmail %>
 * @LastEditTime: <% nowFmt "2006-01-02" %> 00:00:00
 * @FilePath: \<% .ProjectName %>\service\common\helper.go
 * @Description: 服务层跨模块公共辅助
 *
 * Copyright (c) <% .Year %> by <% .AuthorName %>, All Rights Reserved.
 */
package common

import (
	"context"

	bizerror "github.com/kamalyes/go-bizerror"
	gwmiddleware "github.com/kamalyes/go-rpc-gateway/middleware"
)

// TenantOf 从请求上下文取租户 ID
//
// 租户只认上下文，不认请求体：协议里刻意没有 tenant_id 字段
// （见协议中心 proto/key/temp_key_service.proto 的约束），
// 请求体里的租户是调用方可控的，拿它做隔离等于没有隔离。
//
// 对密钥服务来说这一条比普通业务服务更要紧：租户隔离在这里直接等于
// 「能不能让服务端用别人的私钥解密」。
//
// [EN] The tenant comes from the context only. The contract deliberately has no tenant_id
// field, since a tenant in the body is controlled by the caller and isolating on it is no
// isolation. For a key service this matters more than for an ordinary one, because tenant
// isolation here decides whether the server will decrypt with somebody else's private key.
func TenantOf(ctx context.Context) (string, error) {
	tenantID := gwmiddleware.GetTenantID(ctx)
	if tenantID == "" {
		return "", bizerror.ToGRPCError(ctx, bizerror.BizErrCodeUnauthenticated)
	}
	return tenantID, nil
}

// InvalidParam 参数非法
func InvalidParam(ctx context.Context) error {
	return bizerror.ToGRPCError(ctx, bizerror.BizErrCodeInvalidParameter)
}

// NotFound 资源不存在
func NotFound(ctx context.Context) error {
	return bizerror.ToGRPCError(ctx, bizerror.BizErrCodeNotFound)
}

// Internal 服务端内部错误
//
// 密钥相关的失败一律归并到这里，不向调用方区分具体原因。
//
// 为什么不细分：密钥服务的错误信息本身就是攻击面。区分「密钥不存在」
// 「场景不对」「不是你的租户」会让调用方能用枚举 keyID 的方式探测
// 别人的密钥是否存在、属于哪个场景。对外一律「失败」，细节只进日志。
//
// 唯一的例外是「确实不存在」这类对调用方有用且无泄漏风险的情形，
// 由调用处显式选 NotFound。
//
// [EN] Key-related failures collapse here rather than being distinguished for the caller,
// because the error detail is itself an attack surface: telling apart a missing key, a wrong
// scenario and another tenant's key lets a caller enumerate key ids to probe which exist and
// what they are for. Externally everything is a failure and the detail goes only to the log.
// The exception is a genuine absence that is useful and safe to report, chosen explicitly at
// the call site.
func Internal(ctx context.Context) error {
	return bizerror.ToGRPCError(ctx, bizerror.BizErrCodeInternalError)
}
