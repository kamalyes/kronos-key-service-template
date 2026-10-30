/*
 * @Author: kamalyes example@qq.com
 * @Date: 2026-10-30 00:00:00
 * @LastEditors: kamalyes example@qq.com
 * @LastEditTime: 2026-10-30 00:00:00
 * @FilePath: \kronos-key-service\constants\cache.go
 * @Description: 缓存键与时长常量
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package constants

import (
	"fmt"
	"time"
)

// TTLTempKey 临时私钥在 Redis 里的存活时长
//
// 这个值是安全与可用性的直接权衡：
//   定得长 → 私钥在服务端留存更久，一旦 Redis 被读取，暴露面更大；
//   定得短 → 调用方拿到公钥、加密、回传的整个往返必须在窗口内完成，
//            移动端弱网下容易超时，表现为「偶发解密失败」。
//
// 五分钟量级覆盖「取公钥 → 用户填表 → 提交」这条人机交互链路，
// 又不至于让私钥长期驻留。改这个值前先确认最慢的调用方场景能走完。
//
// [EN] A direct trade-off: a longer TTL leaves the private key on the server longer and widens
// the exposure if Redis is read, while a shorter one must still cover the caller's whole round
// trip of fetching the public key, encrypting and posting back, which times out on a weak
// mobile connection and surfaces as intermittent decryption failures. A few minutes covers the
// fetch-fill-submit interaction without leaving keys resident. Check the slowest caller flow
// before changing it.
const TTLTempKey = 5 * time.Minute

// 缓存键前缀 | Cache key prefixes
const (
	// tempKeyPrefix 临时密钥键前缀，带服务名以便多服务共用一套 Redis 时隔离
	// [EN] Prefixed with the service name so several services can share one Redis
	tempKeyPrefix = ServiceName + ":temp_key:"

	// IDSequencePrefix ID 号段序列的键前缀
	// [EN] Key prefix for the id sequences
	IDSequencePrefix = ServiceName + ":idseq:"
)

// GetTempKeyCacheKey 构造临时密钥的缓存键
//
// 场景进键名，不只是存在条目里：从物理层面隔离不同场景的密钥。
// 只把场景写进 value 的话，用错场景的请求仍然能读到这条记录，
// 得靠应用层比对才拦得住——而应用层那道校验是会被漏写的。
// 键里带上场景，用错场景直接就是 key 不存在。
//
// [EN] The scenario goes into the key, not only into the stored value, which isolates
// scenarios physically. With it only in the value, a request using the wrong scenario still
// reads the record and must be stopped by an application-level comparison, and that check is
// the kind that gets forgotten. In the key, a wrong scenario is simply a missing key.
func GetTempKeyCacheKey(scenario, keyID string) string {
	return fmt.Sprintf("%s%s:%s", tempKeyPrefix, scenario, keyID)
}

// ID 号段参数 | Id block parameters
const (
	// IDSequenceLength 序列号的零填充位数
	//
	// 位数不够不会出错（Go 的 %0*d 超了自然变长），但 ID 长度会跳变，
	// 对按长度做校验或截断的下游是个隐患。按单个时间片内的峰值发号量取值。
	//
	// [EN] Too few digits is not an error, since the format simply widens, but the id length
	// then jumps, which trips any downstream that validates or truncates by length.
	IDSequenceLength = 8

	// IDSequenceExpire 序列键的存活时长
	//
	// 必须大于一个时间片（IDGenerator 默认按小时分片），否则当前时间片的键会在
	// 用着的时候过期、序列从头开始，于是同一小时内发出重复 ID。留两倍以上余量。
	//
	// [EN] Must exceed one time bucket, hourly by default, or the key in active use expires,
	// the sequence restarts and duplicate ids follow within the same hour. Leave at least double.
	IDSequenceExpire = 3 * time.Hour
)

// 对象池参数 | Object pool parameters
//
// RSA 生成是 CPU 密集的，2048 位在普通 runner 上几十毫秒量级，4096 位要几百毫秒到秒级。
// 高并发下 N 个请求同时实时生成，会把 CPU 打满并互相拖慢，表现为签发接口整体变慢
// 而不是某个请求慢。预生成池把这件事挪到后台：请求只从池里取一个现成的。
//
// 三档参数按「生成速度 × 使用频率」分别配：
//   1024 生成快、已不推荐使用 → 小池、少量预填
//   2048 主力档 → 大池 + 高预填 + 快补充
//   4096 生成慢且低频 → 不预填（冷启动不为它等），纯异步补
//
// [EN] RSA generation is CPU-bound: 2048-bit takes tens of milliseconds and 4096-bit hundreds
// to over a thousand on an ordinary runner. Under load, N requests generating at once saturate
// the CPU and slow each other down, so the issuing endpoint degrades as a whole rather than in
// individual requests. A pre-generation pool moves the work to the background and a request
// merely takes a ready pair. The three tiers are sized by generation cost against usage: 1024
// is fast and deprecated, 2048 is the workhorse, and 4096 is slow and rare, so it is not
// pre-filled at all and never delays startup.
const (
	// PoolWaitTimeout 池空时最多等后台补多久，超时回退实时生成
	//
	// 不等就直接实时生成，等于池在高峰期形同虚设——高峰正是池会被取空的时候。
	// 但也不能无限等：分发器真的跟不上时，等待会把请求全堆在这里。
	//
	// [EN] Falling straight through to live generation makes the pool useless exactly at peak,
	// which is when it empties. The wait must still be bounded, or requests pile up here when
	// the background cannot keep up.
	PoolWaitTimeout = 200 * time.Millisecond

	// PoolRefreshInterval 后台补充的检查间隔
	// [EN] How often the background top-up checks the pool
	PoolRefreshInterval = 2 * time.Second
)
