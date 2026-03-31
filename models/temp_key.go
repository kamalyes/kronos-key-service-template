/*
 * @Author: <% .AuthorName %> <% .AuthorEmail %>
 * @Date: <% nowFmt "2006-01-02" %> 00:00:00
 * @LastEditors: <% .AuthorName %> <% .AuthorEmail %>
 * @LastEditTime: <% nowFmt "2006-01-02" %> 00:00:00
 * @FilePath: \<% .ProjectName %>\models\temp_key.go
 * @Description: 临时密钥条目与 RSA 长度映射
 *
 * Copyright (c) <% .Year %> by <% .AuthorName %>, All Rights Reserved.
 */
package models

import (
	"time"

	enumspb "<% .ShareProtoModule %>/pb/enums"
	gwglobal "github.com/kamalyes/go-rpc-gateway/global"
	"github.com/kamalyes/go-toolbox/pkg/sign"
)

// TempKeyEntry 存在 Redis 里的临时密钥条目
//
// 为什么不是 GORM 实体：它的生命周期是分钟级，由 Redis TTL 负责回收。
// 落库要配迁移、备份和清理任务，而备份里躺着一堆 RSA 私钥本身就是个风险——
// 数据库备份的保留期通常远长于这些密钥该存在的时间。
//
// [EN] Not a GORM entity: it lives for minutes and Redis's TTL reclaims it. A table would need
// migrations, backups and a cleanup job, and a backup full of RSA private keys is itself a
// risk, since database backups are usually retained far longer than these keys should exist.
type TempKeyEntry struct {
	// PrivateKeyPem PEM 格式私钥
	//
	// 这是整个服务里唯一持有私钥的地方，而且只在服务端内存与 Redis 之间流动，
	// 任何 PB 消息里都没有对应字段（见协议中心 proto/key/temp_key_service.proto 的约束）。
	// 不要给这个结构体加 json 以外的序列化方式，也不要把它放进任何日志或响应。
	//
	// [EN] The only place the private key is held, moving only between server memory and Redis;
	// no PB message has a field for it. Do not add another serialization path, and never put it
	// in a log or a response.
	PrivateKeyPem string `json:"private_key_pem"`

	// TenantID 所属租户，用于租户级隔离
	//
	// 解密时要比对它：场景一致只保证「用途对」，不保证「是你的密钥」。
	// 少了这一道，A 租户猜到 B 租户的 keyID 就能让服务端替它解密。
	//
	// [EN] Compared on decryption: a matching scenario proves the purpose, not the ownership.
	// Without this check, one tenant that guesses another's key id has the server decrypt for it.
	TenantID string `json:"tenant_id"`

	// Scenario 使用场景，与缓存键里的场景互为冗余校验
	// [EN] The scenario, redundant with the one in the cache key as a cross-check
	Scenario enumspb.KeyScenario `json:"scenario"`

	CreatedAt time.Time `json:"created_at"`

	// ExpiresAt 过期时间
	//
	// 与 Redis TTL 并存不是重复：TTL 决定键什么时候消失，这个字段让服务端在读到
	// 条目后再判一次。两者的时钟源不同（Redis 自己 vs 本服务），TTL 还可能因为
	// 运维操作（持久化恢复、手工 PERSIST）失效，多这一道判断的成本是一次时间比较。
	//
	// [EN] Not redundant with the Redis TTL: the TTL decides when the key disappears, while
	// this lets the server re-check after reading it. The two have different clock sources and
	// a TTL can be lost to operations such as a restore or a manual PERSIST; the extra check
	// costs one time comparison.
	ExpiresAt time.Time `json:"expires_at"`
}

// Expired 判断条目是否已过期
func (e *TempKeyEntry) Expired() bool {
	return e == nil || time.Now().After(e.ExpiresAt)
}

// RsaKeySizeToSign 把协议里的密钥长度枚举换成 sign 包的类型
//
// 未指定时取 2048 而不是 4096：默认值会被绝大多数调用方直接用上，
// 而 4096 的生成成本是 2048 的量级倍。把最贵的那档设成默认，等于让
// 所有没想过这个参数的调用方都付最高代价。要 4096 的场景自己显式传。
//
// 未知枚举值也落到 2048：下游用了比本服务新的协议版本时，宁可按安全基线
// 发一把能用的密钥，也不要报错——但这意味着新增枚举值后必须回来补这里，
// 否则新档位会被静默降级成 2048。
//
// [EN] Unspecified maps to 2048 rather than 4096, because a default is what most callers end
// up using and 4096 costs an order of magnitude more to generate; making the most expensive
// tier the default charges every caller who never thought about the parameter. An unknown value
// also falls back to 2048, so a downstream on a newer contract still gets a usable key at the
// security baseline rather than an error — which does mean a new enum value must be added here,
// or it is silently downgraded to 2048.
func RsaKeySizeToSign(size enumspb.RsaKeySize) sign.RsaKeySize {
	switch size {
	case enumspb.RsaKeySize_RSA_KEY_SIZE_1024:
		return sign.RsaKeySize1024
	case enumspb.RsaKeySize_RSA_KEY_SIZE_4096:
		return sign.RsaKeySize4096
	default:
		return sign.RsaKeySize2048
	}
}

// NewKeyID 生成密钥 ID
//
// 用雪花 ID 而不是时间戳：UnixNano 在高并发或多实例下会撞，而 keyID 撞了的后果是
// 后一次签发把前一次的私钥覆盖掉——前一个调用方拿着公钥加密，回来解密时用的是
// 另一把私钥，表现为随机的「解密失败」，和流量正相关，极难复现。
//
// [EN] A snowflake id rather than a timestamp: UnixNano collides under concurrency or across
// instances, and a collided key id means the later issue overwrites the earlier private key, so
// the first caller encrypts with one public key and is decrypted against another. It surfaces
// as random decryption failures correlated with traffic and is very hard to reproduce.
func NewKeyID() string {
	return gwglobal.NewSnowflakeID()
}
