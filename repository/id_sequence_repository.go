/*
 * @Author: <% .AuthorName %> <% .AuthorEmail %>
 * @Date: <% nowFmt "2006-01-02" %> 00:00:00
 * @LastEditors: <% .AuthorName %> <% .AuthorEmail %>
 * @LastEditTime: <% nowFmt "2006-01-02" %> 00:00:00
 * @FilePath: \<% .ProjectName %>\repository\id_sequence_repository.go
 * @Description: ID 号段仓储，按命名空间维护中心序列
 *
 * Copyright (c) <% .Year %> by <% .AuthorName %>, All Rights Reserved.
 */
package repository

import (
	"context"
	"sync"

	"github.com/<% .OrgName %>/<% .ProjectName %>/constants"
	cachex "github.com/kamalyes/go-cachex"
	gwglobal "github.com/kamalyes/go-rpc-gateway/global"
)

// IDSequenceRepository 按命名空间发放 ID 号段
//
// 为什么用 go-cachex 的 IDGenerator 而不是自己写 INCRBY：
// 它的 GenerateIDs 把「取号 + 格式化」做进一条 Lua 脚本里 INCRBY n，
// 一次往返拿一整段，原子性由 Redis 的单线程执行保证，不需要额外的锁或 WATCH。
// 自己写的话，要么 N 次 INCR（N 次往返），要么自己写 Lua——而那就是在重写它。
//
// [EN] go-cachex's IDGenerator rather than a hand-written INCRBY: its GenerateIDs does the
// increment and the formatting inside one Lua script performing INCRBY n, returning a whole
// block in a single round trip with atomicity from Redis's single-threaded execution, so no
// extra lock or WATCH is needed. Hand-rolling it means either N round trips or writing that
// same Lua script.
type IDSequenceRepository struct {
	// generators 按命名空间缓存生成器
	//
	// 用 sync.Map 而不是「每次 new 一个」：IDGenerator 本身是个轻结构体，
	// 但它持有链式配置（前缀、序列位数、过期），每次重建既浪费也容易漏配一项，
	// 导致同一个命名空间在不同请求里用了不同的键前缀——那会直接发出重复 ID。
	//
	// [EN] A sync.Map rather than constructing one per call: the generator is a light struct but
	// carries chained configuration, and rebuilding it each time both wastes work and invites
	// missing one setting, which would leave the same namespace using different key prefixes
	// across requests and emit duplicate ids.
	generators sync.Map // map[string]*cachex.IDGenerator
}

// NewIDSequenceRepository 构造仓储
func NewIDSequenceRepository() *IDSequenceRepository { return &IDSequenceRepository{} }

// generatorFor 取或建某个命名空间的生成器
func (r *IDSequenceRepository) generatorFor(namespace string) *cachex.IDGenerator {
	if v, ok := r.generators.Load(namespace); ok {
		return v.(*cachex.IDGenerator)
	}
	gen := cachex.NewIDGenerator(gwglobal.GetRedis()).
		WithKeyPrefix(constants.IDSequencePrefix + namespace + ":").
		WithSequenceLength(constants.IDSequenceLength).
		WithExpire(constants.IDSequenceExpire)

	// LoadOrStore 而不是 Store：并发首次访问同一命名空间时会建出多个生成器，
	// 必须保证最终只有一个被所有人用到。多建出来的那几个没有副作用
	// （没碰 Redis），被丢弃即可。
	// [EN] LoadOrStore rather than Store: a concurrent first access builds several generators
	// and only one must end up in use. The extras touched no Redis state and are simply dropped
	actual, _ := r.generators.LoadOrStore(namespace, gen)
	return actual.(*cachex.IDGenerator)
}

// Allocate 申领 n 个 ID
//
// 不在这里校验 n 的上下界：那是协议约束（见 IdBlockAllocateRequest 的 validate 标签），
// 由网关的校验拦截器在进入业务代码之前拦掉。仓储层重复校验一遍，
// 只会让两处的上限迟早不一致，而不一致时以哪个为准没人说得清。
//
// [EN] The bounds on n are not re-checked here: they are a protocol constraint enforced by the
// gateway's validation interceptor before business code runs. Re-validating in the repository
// only lets the two limits drift apart, and nobody can then say which one governs.
func (r *IDSequenceRepository) Allocate(ctx context.Context, namespace string, n int) ([]string, error) {
	return r.generatorFor(namespace).GenerateIDs(ctx, n)
}
