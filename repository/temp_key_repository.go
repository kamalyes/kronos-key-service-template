/*
 * @Author: <% .AuthorName %> <% .AuthorEmail %>
 * @Date: <% nowFmt "2006-01-02" %> 00:00:00
 * @LastEditors: <% .AuthorName %> <% .AuthorEmail %>
 * @LastEditTime: <% nowFmt "2006-01-02" %> 00:00:00
 * @FilePath: \<% .ProjectName %>\repository\temp_key_repository.go
 * @Description: 临时密钥仓储，封装 Redis 存取与场景校验
 *
 * Copyright (c) <% .Year %> by <% .AuthorName %>, All Rights Reserved.
 */
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/<% .OrgName %>/<% .ProjectName %>/constants"
	"github.com/<% .OrgName %>/<% .ProjectName %>/models"
	enumspb "<% .ShareProtoModule %>/pb/enums"
	gwglobal "github.com/kamalyes/go-rpc-gateway/global"
	"github.com/redis/go-redis/v9"
)

// 仓储层错误
//
// 用哨兵错误而不是直接返回业务错误码：仓储层不该知道 gRPC status，
// 否则它就没法被非 gRPC 的调用方（迁移脚本、运维命令）复用。
// 服务层负责把这些错误翻译成 go-bizerror 的码。
//
// [EN] Sentinel errors rather than business codes, because the repository should not know about
// gRPC status or it cannot be reused by non-gRPC callers such as migration scripts or ops
// commands. The service layer translates these into go-bizerror codes.
var (
	// ErrScenarioRequired 未传有效场景
	ErrScenarioRequired = errors.New("temp key scenario is required")
	// ErrNotFound 密钥不存在或已过期
	ErrNotFound = errors.New("temp key not found")
	// ErrScenarioMismatch 条目里的场景与请求不一致
	ErrScenarioMismatch = errors.New("temp key scenario mismatch")
)

// TempKeyRepository 临时密钥仓储
//
// 没有字段：Redis 客户端从 gwglobal 取，由网关的 PoolManager 按配置构建。
// 自己持有一个客户端会绕开连接池的配置与健康检查。
//
// [EN] No fields: the Redis client comes from gwglobal, built by the gateway's PoolManager from
// config. Holding one here would bypass the pool's configuration and health checking.
type TempKeyRepository struct{}

// NewTempKeyRepository 构造仓储
func NewTempKeyRepository() *TempKeyRepository { return &TempKeyRepository{} }

// Save 写入条目并设置 TTL
func (r *TempKeyRepository) Save(ctx context.Context, keyID string, entry *models.TempKeyEntry, ttl time.Duration) error {
	if entry == nil || entry.Scenario == enumspb.KeyScenario_KEY_SCENARIO_UNSPECIFIED {
		return ErrScenarioRequired
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	cacheKey := constants.GetTempKeyCacheKey(entry.Scenario.String(), keyID)

	// 日志里只记键名、场景和 TTL，不记 entry——它带着私钥。
	// 这类「顺手把整个结构体打出来」的日志是私钥泄漏最常见的途径，
	// 而且日志通常比密钥本身活得久得多。
	//
	// [EN] The log records the key name, scenario and TTL, never the entry, which carries the
	// private key. Dumping a whole struct is the most common way a private key leaks, and logs
	// usually outlive the key by a wide margin.
	gwglobal.LOGGER.DebugContextKV(ctx, "TempKey.Save",
		"cache_key", cacheKey, "scenario", entry.Scenario.String(), "ttl", ttl.String())

	return gwglobal.GetRedis().Set(ctx, cacheKey, payload, ttl).Err()
}

// Get 按场景与 keyID 读取条目
//
// 读出来之后再比一次条目里的场景：键名里已经带了场景，理论上读到的就一定对得上。
// 这道校验防的是「键名构造逻辑被改坏」——比如有人把分隔符改了，导致不同场景的
// 键意外重合。多一次字段比较，换掉一类静默的跨场景串用。
//
// [EN] The scenario inside the entry is compared again after reading. The key already contains
// it, so a match is expected; the check guards against the key-construction logic being broken,
// for instance a changed separator making two scenarios collide. One field comparison buys
// protection from a silent cross-scenario mix-up.
func (r *TempKeyRepository) Get(ctx context.Context, scenario enumspb.KeyScenario, keyID string) (*models.TempKeyEntry, error) {
	if scenario == enumspb.KeyScenario_KEY_SCENARIO_UNSPECIFIED {
		return nil, ErrScenarioRequired
	}
	cacheKey := constants.GetTempKeyCacheKey(scenario.String(), keyID)

	raw, err := gwglobal.GetRedis().Get(ctx, cacheKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrNotFound
		}
		gwglobal.LOGGER.ErrorContextKV(ctx, "TempKey.Get: redis 读取失败",
			"cache_key", cacheKey, "error", err)
		return nil, err
	}

	var entry models.TempKeyEntry
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		return nil, err
	}
	if entry.Scenario != scenario {
		return nil, ErrScenarioMismatch
	}
	return &entry, nil
}

// Delete 删除条目，返回是否真的删掉了
//
// 返回 bool 而不是只返回 error：调用方要能区分「删掉了」和「本来就没有」。
// 幂等销毁接口需要这个信息来回显，而不是把两种情况都报成成功。
//
// [EN] Returns a bool as well as an error so the caller can tell a deletion from a key that was
// already gone, which an idempotent destroy endpoint needs in order to report it rather than
// calling both outcomes a success.
func (r *TempKeyRepository) Delete(ctx context.Context, scenario enumspb.KeyScenario, keyID string) (bool, error) {
	if scenario == enumspb.KeyScenario_KEY_SCENARIO_UNSPECIFIED {
		return false, ErrScenarioRequired
	}
	cacheKey := constants.GetTempKeyCacheKey(scenario.String(), keyID)
	n, err := gwglobal.GetRedis().Del(ctx, cacheKey).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
