package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// 并发控制缓存常量定义
//
// 性能优化说明：
// 原实现使用 SCAN 命令遍历独立的槽位键（concurrency:account:{id}:{requestID}），
// 在高并发场景下 SCAN 需要多次往返，且遍历大量键时性能下降明显。
//
// 新实现改用 Redis 有序集合（Sorted Set）：
// 1. 每个账号/用户只有一个键，成员为 requestID，分数为时间戳
// 2. 使用 ZCARD 原子获取并发数，时间复杂度 O(1)
// 3. 使用 ZREMRANGEBYSCORE 清理过期槽位，避免手动管理 TTL
// 4. 单次 Redis 调用完成计数，减少网络往返
const (
	// 并发槽位键前缀（有序集合）
	// 格式: concurrency:account:{accountID}
	accountSlotKeyPrefix = "concurrency:account:"
	// 格式: concurrency:user:{userID}
	userSlotKeyPrefix = "concurrency:user:"
	// 格式: concurrency:api_key:{apiKeyID}
	apiKeySlotKeyPrefix      = "concurrency:api_key:"
	liveAccountSlotKeyPrefix = "concurrency:live:account:"
	liveUserSlotKeyPrefix    = "concurrency:live:user:"
	liveAPIKeySlotKeyPrefix  = "concurrency:live:api_key:"
	// API-key-scoped client WebSocket ingress leases use a shorter TTL than
	// ordinary request slots, because idle ingress sessions do not hold a turn slot.
	openAIWSIngressLeaseKeyPrefix  = "concurrency:openai_ws_ingress:api_key:"
	openAIWSIngressLeaseTTLSeconds = 60
	liveLeaseTTLSeconds            = 60
	// 等待队列计数器格式: concurrency:wait:{userID}
	waitQueueKeyPrefix = "concurrency:wait:"
	// 账号级等待队列计数器格式: wait:account:{accountID}
	accountWaitKeyPrefix = "wait:account:"

	// 默认槽位过期时间（分钟），可通过配置覆盖
	defaultSlotTTLMinutes = 15

	// 活跃索引用来替代后台任务全量 SCAN 槽位键。
	// member 是账号/用户 ID，score 是“预计仍需关注到”的 Redis Unix 秒时间戳。
	accountActiveIndexKey = "concurrency:account:active_index" // ZSET member=accountID, score=expireAtUnixSeconds
	userActiveIndexKey    = "concurrency:user:active_index"    // ZSET member=userID, score=expireAtUnixSeconds

	// 后台清理只按批处理索引候选，避免单次任务占用 Redis 太久。
	activeIndexCleanupBatchSize  = 1000
	activeIndexPipelineChunkSize = 500

	// 一次性迁移 marker：活跃索引机制上线前遗留的等待计数键无法被索引发现，
	// 且有流量时 TTL 会被不断刷新，必须清扫一次。marker 存在即代表已完成。
	legacyWaitSweepMarkerKey = "concurrency:startup:legacy_wait_sweep:v1"
)

var (
	// acquireScript 使用有序集合计数并在未达上限时添加槽位
	// 使用 Redis TIME 命令获取服务器时间，避免多实例时钟不同步问题
	// KEYS[1] = 普通槽位键，KEYS[2] = 对应 Live 槽位键
	// ARGV[1] = maxConcurrency
	// ARGV[2] = TTL（秒）
	// ARGV[3] = requestID
	// 返回 {是否成功, Redis 当前秒}，Go 侧复用同一时间源写活跃索引，省去额外 TIME 往返。
	acquireScript = redis.NewScript(`
		-- Redis 3.2-4.x compat: opt into effects replication so redis.call('TIME')
		-- replicates correctly. No-op on Redis 5.0+ (effects replication is default).
		redis.replicate_commands()
		local key = KEYS[1]
		local liveKey = KEYS[2]
		local maxConcurrency = tonumber(ARGV[1])
		local ttl = tonumber(ARGV[2])
		local requestID = ARGV[3]

		-- 使用 Redis 服务器时间，确保多实例时钟一致
		local timeResult = redis.call('TIME')
		local now = tonumber(timeResult[1])
		local expireBefore = now - ttl

		-- 清理过期槽位
		redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
		redis.call('ZREMRANGEBYSCORE', liveKey, '-inf', now - 60)

		-- 检查是否已存在（支持重试场景刷新时间戳）
		local exists = redis.call('ZSCORE', key, requestID)
		if exists ~= false then
			redis.call('ZADD', key, now, requestID)
			redis.call('EXPIRE', key, ttl)
			return {1, now}
		end

		-- 检查是否达到并发上限
		local count = redis.call('ZCARD', key) + redis.call('ZCARD', liveKey)
		if count < maxConcurrency then
			redis.call('ZADD', key, now, requestID)
			redis.call('EXPIRE', key, ttl)
			return {1, now}
		end

		return {0, now}
	`)

	// getCountScript 统计有序集合中的槽位数量并清理过期条目
	// 使用 Redis TIME 命令获取服务器时间
	// KEYS[1] = 普通槽位键，KEYS[2] = 对应 Live 槽位键
	// ARGV[1] = TTL（秒）
	getCountScript = redis.NewScript(`
		-- Redis 3.2-4.x compat: opt into effects replication so redis.call('TIME')
		-- replicates correctly. No-op on Redis 5.0+ (effects replication is default).
		redis.replicate_commands()
		local key = KEYS[1]
		local liveKey = KEYS[2]
		local ttl = tonumber(ARGV[1])

		-- 使用 Redis 服务器时间
		local timeResult = redis.call('TIME')
		local now = tonumber(timeResult[1])
		local expireBefore = now - ttl

		redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
		redis.call('ZREMRANGEBYSCORE', liveKey, '-inf', now - 60)
		return redis.call('ZCARD', key) + redis.call('ZCARD', liveKey)
	`)

	acquireLiveLeaseScript = redis.NewScript(`
		redis.replicate_commands()
		local accountRegular = KEYS[1]
		local accountLive = KEYS[2]
		local userRegular = KEYS[3]
		local userLive = KEYS[4]
		local apiLive = KEYS[5]
		local accountMax = tonumber(ARGV[1])
		local userMax = tonumber(ARGV[2])
		local ttl = tonumber(ARGV[3])
		local leaseID = ARGV[4]
		local replacing = tonumber(ARGV[5])
		local now = tonumber(redis.call('TIME')[1])
		local liveExpireBefore = now - ttl
		redis.call('ZREMRANGEBYSCORE', accountLive, '-inf', liveExpireBefore)
		redis.call('ZREMRANGEBYSCORE', userLive, '-inf', liveExpireBefore)
		redis.call('ZREMRANGEBYSCORE', apiLive, '-inf', liveExpireBefore)
		if redis.call('ZSCORE', accountLive, leaseID) ~= false then
			return 1
		end
		local accountCount = redis.call('ZCARD', accountRegular) + redis.call('ZCARD', accountLive)
		local userCount = redis.call('ZCARD', userRegular) + redis.call('ZCARD', userLive)
		local allowance = 0
		if replacing == 1 then allowance = 1 end
		if accountMax > 0 and accountCount >= accountMax + allowance then return 0 end
		if userMax > 0 and userCount >= userMax + allowance then return 0 end
		redis.call('ZADD', accountLive, now, leaseID)
		redis.call('ZADD', userLive, now, leaseID)
		redis.call('ZADD', apiLive, now, leaseID)
		redis.call('EXPIRE', accountLive, ttl)
		redis.call('EXPIRE', userLive, ttl)
		redis.call('EXPIRE', apiLive, ttl)
		return 1
	`)

	refreshLiveLeaseScript = redis.NewScript(`
		redis.replicate_commands()
		local ttl = tonumber(ARGV[1])
		local leaseID = ARGV[2]
		local now = tonumber(redis.call('TIME')[1])
		local expireBefore = now - ttl
		for _, key in ipairs(KEYS) do
			redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
			if redis.call('ZSCORE', key, leaseID) == false then return 0 end
		end
		for _, key in ipairs(KEYS) do
			redis.call('ZADD', key, now, leaseID)
			redis.call('EXPIRE', key, ttl)
		end
		return 1
	`)

	// trackSlotScript 记录 stats-only 槽位，不做并发上限判断。
	// KEYS[1] = 有序集合键
	// ARGV[1] = TTL（秒）
	// ARGV[2] = requestID
	trackSlotScript = redis.NewScript(`
		-- Redis 3.2-4.x compat: opt into effects replication so redis.call('TIME')
		-- replicates correctly. No-op on Redis 5.0+ (effects replication is default).
		redis.replicate_commands()
		local key = KEYS[1]
		local ttl = tonumber(ARGV[1])
		local requestID = ARGV[2]

		local timeResult = redis.call('TIME')
		local now = tonumber(timeResult[1])
		local expireBefore = now - ttl

		redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
		redis.call('ZADD', key, now, requestID)
		redis.call('EXPIRE', key, ttl)
		return 1
	`)

	// acquireOpenAIWSIngressLeaseScript atomically reaps crashed members and
	// acquires or refreshes one API-key-scoped ingress lease using Redis TIME.
	acquireOpenAIWSIngressLeaseScript = redis.NewScript(`
		redis.replicate_commands()
		local key = KEYS[1]
		local maxConnections = tonumber(ARGV[1])
		local ttl = tonumber(ARGV[2])
		local leaseID = ARGV[3]
		local now = tonumber(redis.call('TIME')[1])
		local expireBefore = now - ttl
		redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
		if redis.call('ZSCORE', key, leaseID) ~= false then
			redis.call('ZADD', key, now, leaseID)
			redis.call('EXPIRE', key, ttl)
			return 1
		end
		if redis.call('ZCARD', key) < maxConnections then
			redis.call('ZADD', key, now, leaseID)
			redis.call('EXPIRE', key, ttl)
			return 1
		end
		return 0
	`)

	// refreshOpenAIWSIngressLeaseScript does not recreate a missing member: a
	// process that lost its lease must terminate its local WebSocket instead of
	// silently continuing beyond the distributed cap.
	refreshOpenAIWSIngressLeaseScript = redis.NewScript(`
		redis.replicate_commands()
		local key = KEYS[1]
		local ttl = tonumber(ARGV[1])
		local leaseID = ARGV[2]
		local now = tonumber(redis.call('TIME')[1])
		local expireBefore = now - ttl
		redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
		if redis.call('ZSCORE', key, leaseID) == false then
			return 0
		end
		redis.call('ZADD', key, now, leaseID)
		redis.call('EXPIRE', key, ttl)
		return 1
	`)

	// incrementWaitScript - refreshes TTL on each increment to keep queue depth accurate
	// KEYS[1] = wait queue key
	// ARGV[1] = maxWait
	// ARGV[2] = TTL in seconds
	// 返回 {是否成功, Redis 当前秒}，供 Go 侧免额外 TIME 往返写活跃索引。
	incrementWaitScript = redis.NewScript(`
		-- Redis 3.2-4.x compat: opt into effects replication so redis.call('TIME')
		-- replicates correctly. No-op on Redis 5.0+ (effects replication is default).
		redis.replicate_commands()
		local current = redis.call('GET', KEYS[1])
		if current == false then
			current = 0
		else
			current = tonumber(current)
		end
		local now = tonumber(redis.call('TIME')[1])

		if current >= tonumber(ARGV[1]) then
			return {0, now}
		end

		redis.call('INCR', KEYS[1])

		-- Refresh TTL so long-running traffic doesn't expire active queue counters.
		redis.call('EXPIRE', KEYS[1], ARGV[2])

		return {1, now}
	`)

	// incrementAccountWaitScript - account-level wait queue count (refresh TTL on each increment)
	// 返回值同 incrementWaitScript：{是否成功, Redis 当前秒}。
	incrementAccountWaitScript = redis.NewScript(`
		-- Redis 3.2-4.x compat: opt into effects replication so redis.call('TIME')
		-- replicates correctly. No-op on Redis 5.0+ (effects replication is default).
		redis.replicate_commands()
		local current = redis.call('GET', KEYS[1])
		if current == false then
			current = 0
		else
			current = tonumber(current)
		end
		local now = tonumber(redis.call('TIME')[1])

		if current >= tonumber(ARGV[1]) then
			return {0, now}
		end

		redis.call('INCR', KEYS[1])

		-- Refresh TTL so long-running traffic doesn't expire active queue counters.
		redis.call('EXPIRE', KEYS[1], ARGV[2])

		return {1, now}
	`)

	// decrementWaitScript - same as before
	decrementWaitScript = redis.NewScript(`
			local current = redis.call('GET', KEYS[1])
			if current ~= false and tonumber(current) > 0 then
				redis.call('DECR', KEYS[1])
			end
			return 1
		`)

	// cleanupExpiredSlotsScript 清理单个账号/用户有序集合中过期槽位
	// KEYS[1] = 有序集合键
	// ARGV[1] = TTL（秒）
	cleanupExpiredSlotsScript = redis.NewScript(`
		-- Redis 3.2-4.x compat: opt into effects replication so redis.call('TIME')
		-- replicates correctly. No-op on Redis 5.0+ (effects replication is default).
		redis.replicate_commands()
		local key = KEYS[1]
		local ttl = tonumber(ARGV[1])
		local timeResult = redis.call('TIME')
		local now = tonumber(timeResult[1])
		local expireBefore = now - ttl
		redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
		if redis.call('ZCARD', key) == 0 then
			redis.call('DEL', key)
		else
			redis.call('EXPIRE', key, ttl)
		end
		return 1
	`)

	// reconcileSlotIndexScript rechecks load and updates the index in one operation.
	// KEYS: index, regular slots, live slots, wait count. ARGV: member, regular
	// TTL, live TTL, wait TTL, optional startup prefix, captured wait value/PTTL,
	// captured index score, then captured regular member/score pairs. Startup only removes captured,
	// unchanged slots; a newly acquired slot must never become a cleanup victim.
	// This cache uses standalone Redis/Valkey (these keys are not cluster tagged).
	reconcileSlotIndexScript = redis.NewScript(`
		redis.replicate_commands()
		local member = ARGV[1]
		local slotTTL = tonumber(ARGV[2])
		local liveTTL = tonumber(ARGV[3])
		local waitTTL = tonumber(ARGV[4])
		local now = tonumber(redis.call('TIME')[1])
		-- Validate all key types before any writes: Redis script errors do not roll back.
		local score = redis.call('ZSCORE', KEYS[1], member)
		local regular = redis.call('ZCARD', KEYS[2])
		local live = redis.call('ZCARD', KEYS[3])
		local waitValue = redis.call('GET', KEYS[4])
		local waitPTTL = redis.call('PTTL', KEYS[4])
		if ARGV[5] ~= '' and (score or '') == ARGV[8] then
			for i = 9, #ARGV, 2 do
				local request = ARGV[i]
				if string.sub(request, 1, string.len(ARGV[5])) ~= ARGV[5] then
					local current = redis.call('ZSCORE', KEYS[2], request)
					if current and tonumber(current) == tonumber(ARGV[i + 1]) then
						redis.call('ZREM', KEYS[2], request)
					end
				end
			end
			-- Preserve a wait counter refreshed since candidate capture.
			if waitValue and waitValue == ARGV[6] and waitPTTL <= tonumber(ARGV[7]) then
				redis.call('DEL', KEYS[4])
				waitValue = false
			end
		end
		redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now - slotTTL)
		redis.call('ZREMRANGEBYSCORE', KEYS[3], '-inf', now - liveTTL)
		regular = redis.call('ZCARD', KEYS[2])
		live = redis.call('ZCARD', KEYS[3])
		local ttl = 0
		if regular > 0 then ttl = slotTTL end
		if live > 0 and liveTTL > ttl then ttl = liveTTL end
		if (tonumber(waitValue) or 0) > 0 and waitTTL > ttl then ttl = waitTTL end
		if ttl == 0 then
			return redis.call('ZREM', KEYS[1], member)
		end
		local expires = now + ttl
		-- A cleanup snapshot must not shorten an acquisition's fresh index TTL.
		if not score or tonumber(score) < expires then
			redis.call('ZADD', KEYS[1], expires, member)
		end
		-- Never reset slot/wait key TTLs from a stale cleanup snapshot.
		return 0
	`)

	removeInvalidSlotIndexMemberScript = redis.NewScript(`
		local score = redis.call('ZSCORE', KEYS[1], ARGV[1])
		if score and tonumber(score) <= tonumber(ARGV[2]) then
			return redis.call('ZREM', KEYS[1], ARGV[1])
		end
		return 0
	`)
)

type concurrencyCache struct {
	rdb                 *redis.Client
	slotTTLSeconds      int // 槽位过期时间（秒）
	waitQueueTTLSeconds int // 等待队列过期时间（秒）
}

// NewConcurrencyCache 创建并发控制缓存
// slotTTLMinutes: 槽位过期时间（分钟），0 或负数使用默认值 15 分钟
// waitQueueTTLSeconds: 等待队列过期时间（秒），0 或负数使用 slot TTL
func NewConcurrencyCache(rdb *redis.Client, slotTTLMinutes int, waitQueueTTLSeconds int) service.ConcurrencyCache {
	if slotTTLMinutes <= 0 {
		slotTTLMinutes = defaultSlotTTLMinutes
	}
	if waitQueueTTLSeconds <= 0 {
		waitQueueTTLSeconds = slotTTLMinutes * 60
	}
	return &concurrencyCache{
		rdb:                 rdb,
		slotTTLSeconds:      slotTTLMinutes * 60,
		waitQueueTTLSeconds: waitQueueTTLSeconds,
	}
}

// Helper functions for key generation
func accountSlotKey(accountID int64) string {
	return fmt.Sprintf("%s%d", accountSlotKeyPrefix, accountID)
}

func userSlotKey(userID int64) string {
	return fmt.Sprintf("%s%d", userSlotKeyPrefix, userID)
}

func apiKeySlotKey(apiKeyID int64) string {
	return fmt.Sprintf("%s%d", apiKeySlotKeyPrefix, apiKeyID)
}

func liveAccountSlotKey(accountID int64) string {
	return fmt.Sprintf("%s%d", liveAccountSlotKeyPrefix, accountID)
}

func liveUserSlotKey(userID int64) string {
	return fmt.Sprintf("%s%d", liveUserSlotKeyPrefix, userID)
}

func liveAPIKeySlotKey(apiKeyID int64) string {
	return fmt.Sprintf("%s%d", liveAPIKeySlotKeyPrefix, apiKeyID)
}

func openAIWSIngressLeaseKey(apiKeyID int64) string {
	return fmt.Sprintf("%s%d", openAIWSIngressLeaseKeyPrefix, apiKeyID)
}

func waitQueueKey(userID int64) string {
	return fmt.Sprintf("%s%d", waitQueueKeyPrefix, userID)
}

func accountWaitKey(accountID int64) string {
	return fmt.Sprintf("%s%d", accountWaitKeyPrefix, accountID)
}

// redisUnixSeconds 统一使用 Redis 服务器时间，避免多实例本地时钟漂移导致索引提前/延后过期。
func (c *concurrencyCache) redisUnixSeconds(ctx context.Context) (int64, error) {
	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return 0, fmt.Errorf("redis TIME: %w", err)
	}
	return now.Unix(), nil
}

// slotIndexSpec 描述一个活跃索引及其对应的槽位/等待键构造方式。
// 用具名字段避免把 slotKey/waitKey 两个同签名函数按位置传参时写反。
type slotIndexSpec struct {
	indexKey string
	slotKey  func(int64) string
	liveKey  func(int64) string
	waitKey  func(int64) string
}

var (
	accountSlotIndex = slotIndexSpec{indexKey: accountActiveIndexKey, slotKey: accountSlotKey, liveKey: liveAccountSlotKey, waitKey: accountWaitKey}
	userSlotIndex    = slotIndexSpec{indexKey: userActiveIndexKey, slotKey: userSlotKey, liveKey: liveUserSlotKey, waitKey: waitQueueKey}
)

// touchActiveIndexAt 是写路径上的轻量标记：主操作已成功时，尽力把 ID 放入活跃索引，
// score 为给定的绝对过期时间（Redis Unix 秒）。索引失败不影响并发槽位/等待队列本身，
// 后续释放或清理会再次校正，因此只记日志不上抛。
func (c *concurrencyCache) touchActiveIndexAt(ctx context.Context, indexKey string, id int64, expireAt int64) {
	if c == nil || c.rdb == nil || id <= 0 || expireAt <= 0 {
		return
	}
	if err := c.rdb.ZAdd(ctx, indexKey, redis.Z{
		Score:  float64(expireAt),
		Member: strconv.FormatInt(id, 10),
	}).Err(); err != nil {
		logger.LegacyPrintf("repository.concurrency", "Warning: touch active index %s for %d failed: %v", indexKey, id, err)
	}
}

func (c *concurrencyCache) refreshAccountActiveIndex(ctx context.Context, accountID int64) {
	c.refreshActiveIndex(ctx, accountActiveIndexKey, accountID, accountSlotKey(accountID), accountWaitKey(accountID))
}

func (c *concurrencyCache) refreshUserActiveIndex(ctx context.Context, userID int64) {
	c.refreshActiveIndex(ctx, userActiveIndexKey, userID, userSlotKey(userID), waitQueueKey(userID))
}

// refreshActiveIndex 以 Redis 中的真实槽位/等待数为准重建索引状态。
// 释放槽位、等待计数减少、清理过期成员后都会调用它，防止索引残留。
// 索引维护是 best-effort：失败只记日志，不影响主流程。
func (c *concurrencyCache) refreshActiveIndex(ctx context.Context, indexKey string, id int64, slotKey, waitKey string) {
	if c == nil || c.rdb == nil || id <= 0 {
		return
	}
	liveKey := liveAccountSlotKey(id)
	if indexKey == userActiveIndexKey {
		liveKey = liveUserSlotKey(id)
	}
	if err := reconcileSlotIndexScript.Run(ctx, c.rdb,
		[]string{indexKey, slotKey, liveKey, waitKey},
		strconv.FormatInt(id, 10), c.slotTTLSeconds, liveLeaseTTLSeconds, c.waitQueueTTLSeconds, "", "", -2,
	).Err(); err != nil {
		logger.LegacyPrintf("repository.concurrency", "Warning: refresh active index %s for %d failed: %v", indexKey, id, err)
	}
}

// reconcileIndexMembers pipelines atomic per-member rechecks, not stale load decisions.
func (c *concurrencyCache) reconcileIndexMembers(ctx context.Context, spec slotIndexSpec, members []string, cutoff int64) error {
	for start := 0; start < len(members); start += activeIndexPipelineChunkSize {
		end := start + activeIndexPipelineChunkSize
		if end > len(members) {
			end = len(members)
		}
		pipe := c.rdb.Pipeline()
		for _, member := range members[start:end] {
			id, err := strconv.ParseInt(member, 10, 64)
			if err != nil || id <= 0 {
				removeInvalidSlotIndexMemberScript.Eval(ctx, pipe, []string{spec.indexKey}, member, cutoff)
				continue
			}
			reconcileSlotIndexScript.Eval(ctx, pipe,
				[]string{spec.indexKey, spec.slotKey(id), spec.liveKey(id), spec.waitKey(id)},
				member, c.slotTTLSeconds, liveLeaseTTLSeconds, c.waitQueueTTLSeconds, "", "", -2)
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return fmt.Errorf("reconcile active index %s: %w", spec.indexKey, err)
		}
	}
	return nil
}

// runScriptInt64Pair 执行返回两元素整数数组的 Lua 脚本并解析（如 {result, now}、{removed, remaining}）。
func runScriptInt64Pair(ctx context.Context, rdb *redis.Client, script *redis.Script, keys []string, args ...any) (int64, int64, error) {
	raw, err := script.Run(ctx, rdb, keys, args...).Result()
	if err != nil {
		return 0, 0, err
	}
	first, err := redisScriptInt64At(raw, 0)
	if err != nil {
		return 0, 0, fmt.Errorf("parse script value 0: %w", err)
	}
	second, err := redisScriptInt64At(raw, 1)
	if err != nil {
		return 0, 0, fmt.Errorf("parse script value 1: %w", err)
	}
	return first, second, nil
}

// Account slot operations

func (c *concurrencyCache) AcquireAccountSlot(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
	key := accountSlotKey(accountID)
	// 时间戳在 Lua 脚本内使用 Redis TIME 命令获取，确保多实例时钟一致
	result, now, err := runScriptInt64Pair(ctx, c.rdb, acquireScript, []string{key, liveAccountSlotKey(accountID)}, maxConcurrency, c.slotTTLSeconds, requestID)
	if err != nil {
		return false, err
	}
	if result == 1 {
		// 成功占槽后标记活跃账号，后台清理即可从索引定位候选账号。
		c.touchActiveIndexAt(ctx, accountActiveIndexKey, accountID, now+int64(c.slotTTLSeconds))
	}
	return result == 1, nil
}

func (c *concurrencyCache) ReleaseAccountSlot(ctx context.Context, accountID int64, requestID string) error {
	key := accountSlotKey(accountID)
	if err := c.rdb.ZRem(ctx, key, requestID).Err(); err != nil {
		return err
	}
	// 释放后用真实负载刷新索引；若没有槽位和等待计数，会移除索引 member。
	c.refreshAccountActiveIndex(ctx, accountID)
	return nil
}

func (c *concurrencyCache) GetAccountConcurrency(ctx context.Context, accountID int64) (int, error) {
	key := accountSlotKey(accountID)
	// 时间戳在 Lua 脚本内使用 Redis TIME 命令获取
	result, err := getCountScript.Run(ctx, c.rdb, []string{key, liveAccountSlotKey(accountID)}, c.slotTTLSeconds).Int()
	if err != nil {
		return 0, err
	}
	return result, nil
}

func (c *concurrencyCache) GetAccountConcurrencyBatch(ctx context.Context, accountIDs []int64) (map[int64]int, error) {
	if len(accountIDs) == 0 {
		return map[int64]int{}, nil
	}

	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TIME: %w", err)
	}
	cutoffTime := now.Unix() - int64(c.slotTTLSeconds)

	pipe := c.rdb.Pipeline()
	type accountCmd struct {
		accountID int64
		zcardCmd  *redis.IntCmd
		liveCmd   *redis.IntCmd
	}
	cmds := make([]accountCmd, 0, len(accountIDs))
	for _, accountID := range accountIDs {
		slotKey := accountSlotKeyPrefix + strconv.FormatInt(accountID, 10)
		liveKey := liveAccountSlotKeyPrefix + strconv.FormatInt(accountID, 10)
		pipe.ZRemRangeByScore(ctx, slotKey, "-inf", strconv.FormatInt(cutoffTime, 10))
		pipe.ZRemRangeByScore(ctx, liveKey, "-inf", strconv.FormatInt(now.Unix()-liveLeaseTTLSeconds, 10))
		cmds = append(cmds, accountCmd{
			accountID: accountID,
			zcardCmd:  pipe.ZCard(ctx, slotKey),
			liveCmd:   pipe.ZCard(ctx, liveKey),
		})
	}

	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("pipeline exec: %w", err)
	}

	result := make(map[int64]int, len(accountIDs))
	for _, cmd := range cmds {
		result[cmd.accountID] = int(cmd.zcardCmd.Val() + cmd.liveCmd.Val())
	}
	return result, nil
}

// User slot operations

func (c *concurrencyCache) AcquireUserSlot(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
	key := userSlotKey(userID)
	// 时间戳在 Lua 脚本内使用 Redis TIME 命令获取，确保多实例时钟一致
	result, now, err := runScriptInt64Pair(ctx, c.rdb, acquireScript, []string{key, liveUserSlotKey(userID)}, maxConcurrency, c.slotTTLSeconds, requestID)
	if err != nil {
		return false, err
	}
	if result == 1 {
		// 成功占槽后标记活跃用户，避免启动清理依赖全量 SCAN。
		c.touchActiveIndexAt(ctx, userActiveIndexKey, userID, now+int64(c.slotTTLSeconds))
	}
	return result == 1, nil
}

func (c *concurrencyCache) ReleaseUserSlot(ctx context.Context, userID int64, requestID string) error {
	key := userSlotKey(userID)
	if err := c.rdb.ZRem(ctx, key, requestID).Err(); err != nil {
		return err
	}
	// 释放后按 Redis 中剩余负载修正索引状态。
	c.refreshUserActiveIndex(ctx, userID)
	return nil
}

func (c *concurrencyCache) GetUserConcurrency(ctx context.Context, userID int64) (int, error) {
	key := userSlotKey(userID)
	// 时间戳在 Lua 脚本内使用 Redis TIME 命令获取
	result, err := getCountScript.Run(ctx, c.rdb, []string{key, liveUserSlotKey(userID)}, c.slotTTLSeconds).Int()
	if err != nil {
		return 0, err
	}
	return result, nil
}

func (c *concurrencyCache) TrackAPIKeySlot(ctx context.Context, apiKeyID int64, requestID string) error {
	key := apiKeySlotKey(apiKeyID)
	_, err := trackSlotScript.Run(ctx, c.rdb, []string{key}, c.slotTTLSeconds, requestID).Result()
	return err
}

func (c *concurrencyCache) ReleaseAPIKeySlot(ctx context.Context, apiKeyID int64, requestID string) error {
	key := apiKeySlotKey(apiKeyID)
	return c.rdb.ZRem(ctx, key, requestID).Err()
}

func (c *concurrencyCache) AcquireOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, maxConnections int, leaseID string) (bool, error) {
	if c == nil || c.rdb == nil || apiKeyID <= 0 || maxConnections <= 0 || leaseID == "" {
		return false, nil
	}
	result, err := acquireOpenAIWSIngressLeaseScript.Run(
		ctx,
		c.rdb,
		[]string{openAIWSIngressLeaseKey(apiKeyID)},
		maxConnections,
		openAIWSIngressLeaseTTLSeconds,
		leaseID,
	).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (c *concurrencyCache) RefreshOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, leaseID string) (bool, error) {
	if c == nil || c.rdb == nil || apiKeyID <= 0 || leaseID == "" {
		return false, nil
	}
	result, err := refreshOpenAIWSIngressLeaseScript.Run(
		ctx,
		c.rdb,
		[]string{openAIWSIngressLeaseKey(apiKeyID)},
		openAIWSIngressLeaseTTLSeconds,
		leaseID,
	).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (c *concurrencyCache) ReleaseOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, leaseID string) error {
	if c == nil || c.rdb == nil || apiKeyID <= 0 || leaseID == "" {
		return nil
	}
	return c.rdb.ZRem(ctx, openAIWSIngressLeaseKey(apiKeyID), leaseID).Err()
}

func (c *concurrencyCache) AcquireLiveLease(
	ctx context.Context,
	accountID int64,
	accountMax int,
	userID int64,
	userMax int,
	apiKeyID int64,
	leaseID string,
	replacingRegularSlots bool,
) (bool, error) {
	if c == nil || c.rdb == nil || accountID <= 0 || userID <= 0 || apiKeyID <= 0 || leaseID == "" {
		return false, nil
	}
	replacing := 0
	if replacingRegularSlots {
		replacing = 1
	}
	result, err := acquireLiveLeaseScript.Run(ctx, c.rdb, []string{
		accountSlotKey(accountID),
		liveAccountSlotKey(accountID),
		userSlotKey(userID),
		liveUserSlotKey(userID),
		liveAPIKeySlotKey(apiKeyID),
	}, accountMax, userMax, liveLeaseTTLSeconds, leaseID, replacing).Int()
	return result == 1, err
}

func (c *concurrencyCache) RefreshLiveLease(ctx context.Context, accountID, userID, apiKeyID int64, leaseID string) (bool, error) {
	if c == nil || c.rdb == nil || leaseID == "" {
		return false, nil
	}
	result, err := refreshLiveLeaseScript.Run(ctx, c.rdb, []string{
		liveAccountSlotKey(accountID),
		liveUserSlotKey(userID),
		liveAPIKeySlotKey(apiKeyID),
	}, liveLeaseTTLSeconds, leaseID).Int()
	return result == 1, err
}

func (c *concurrencyCache) ReleaseLiveLease(ctx context.Context, accountID, userID, apiKeyID int64, leaseID string) error {
	if c == nil || c.rdb == nil || leaseID == "" {
		return nil
	}
	pipe := c.rdb.TxPipeline()
	pipe.ZRem(ctx, liveAccountSlotKey(accountID), leaseID)
	pipe.ZRem(ctx, liveUserSlotKey(userID), leaseID)
	pipe.ZRem(ctx, liveAPIKeySlotKey(apiKeyID), leaseID)
	_, err := pipe.Exec(ctx)
	return err
}

func (c *concurrencyCache) GetAPIKeyConcurrencyBatch(ctx context.Context, apiKeyIDs []int64) (map[int64]int, error) {
	if len(apiKeyIDs) == 0 {
		return map[int64]int{}, nil
	}

	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TIME: %w", err)
	}
	cutoffTime := now.Unix() - int64(c.slotTTLSeconds)

	pipe := c.rdb.Pipeline()
	type apiKeyCmd struct {
		apiKeyID int64
		zcardCmd *redis.IntCmd
		liveCmd  *redis.IntCmd
	}
	cmds := make([]apiKeyCmd, 0, len(apiKeyIDs))
	for _, apiKeyID := range apiKeyIDs {
		slotKey := apiKeySlotKeyPrefix + strconv.FormatInt(apiKeyID, 10)
		liveKey := liveAPIKeySlotKeyPrefix + strconv.FormatInt(apiKeyID, 10)
		pipe.ZRemRangeByScore(ctx, slotKey, "-inf", strconv.FormatInt(cutoffTime, 10))
		pipe.ZRemRangeByScore(ctx, liveKey, "-inf", strconv.FormatInt(now.Unix()-liveLeaseTTLSeconds, 10))
		cmds = append(cmds, apiKeyCmd{
			apiKeyID: apiKeyID,
			zcardCmd: pipe.ZCard(ctx, slotKey),
			liveCmd:  pipe.ZCard(ctx, liveKey),
		})
	}

	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("pipeline exec: %w", err)
	}

	result := make(map[int64]int, len(apiKeyIDs))
	for _, cmd := range cmds {
		result[cmd.apiKeyID] = int(cmd.zcardCmd.Val() + cmd.liveCmd.Val())
	}
	return result, nil
}

// Wait queue operations

func (c *concurrencyCache) IncrementWaitCount(ctx context.Context, userID int64, maxWait int) (bool, error) {
	key := waitQueueKey(userID)
	result, now, err := runScriptInt64Pair(ctx, c.rdb, incrementWaitScript, []string{key}, maxWait, c.waitQueueTTLSeconds)
	if err != nil {
		return false, err
	}
	if result == 1 {
		// 等待队列也会让用户保持“活跃”，否则槽位为 0 时后台任务可能漏看等待计数。
		c.touchActiveIndexAt(ctx, userActiveIndexKey, userID, now+int64(c.waitQueueTTLSeconds))
	}
	return result == 1, nil
}

func (c *concurrencyCache) DecrementWaitCount(ctx context.Context, userID int64) error {
	key := waitQueueKey(userID)
	_, err := decrementWaitScript.Run(ctx, c.rdb, []string{key}).Result()
	if err == nil {
		// 等待数减少后重新判断是否还需要保留索引。
		c.refreshUserActiveIndex(ctx, userID)
	}
	return err
}

// Account wait queue operations

func (c *concurrencyCache) IncrementAccountWaitCount(ctx context.Context, accountID int64, maxWait int) (bool, error) {
	key := accountWaitKey(accountID)
	result, now, err := runScriptInt64Pair(ctx, c.rdb, incrementAccountWaitScript, []string{key}, maxWait, c.waitQueueTTLSeconds)
	if err != nil {
		return false, err
	}
	if result == 1 {
		// 账号级等待队列同样写入账号活跃索引，供负载查询和清理任务使用。
		c.touchActiveIndexAt(ctx, accountActiveIndexKey, accountID, now+int64(c.waitQueueTTLSeconds))
	}
	return result == 1, nil
}

func (c *concurrencyCache) DecrementAccountWaitCount(ctx context.Context, accountID int64) error {
	key := accountWaitKey(accountID)
	_, err := decrementWaitScript.Run(ctx, c.rdb, []string{key}).Result()
	if err == nil {
		// 等待计数归零后索引需要同步删除，避免后台任务反复处理空账号。
		c.refreshAccountActiveIndex(ctx, accountID)
	}
	return err
}

func (c *concurrencyCache) GetAccountWaitingCount(ctx context.Context, accountID int64) (int, error) {
	key := accountWaitKey(accountID)
	val, err := c.rdb.Get(ctx, key).Int()
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, err
	}
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return val, nil
}

func (c *concurrencyCache) GetAccountsLoadBatch(ctx context.Context, accounts []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error) {
	if len(accounts) == 0 {
		return map[int64]*service.AccountLoadInfo{}, nil
	}

	// 使用 Pipeline 替代 Lua 脚本，兼容 Redis Cluster（Lua 内动态拼 key 会 CROSSSLOT）。
	// 每个账号执行 3 个命令：ZREMRANGEBYSCORE（清理过期）、ZCARD（并发数）、GET（等待数）。
	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TIME: %w", err)
	}
	cutoffTime := now.Unix() - int64(c.slotTTLSeconds)

	pipe := c.rdb.Pipeline()

	type accountCmds struct {
		id             int64
		maxConcurrency int
		zcardCmd       *redis.IntCmd
		liveCmd        *redis.IntCmd
		getCmd         *redis.StringCmd
	}
	cmds := make([]accountCmds, 0, len(accounts))
	for _, acc := range accounts {
		slotKey := accountSlotKeyPrefix + strconv.FormatInt(acc.ID, 10)
		liveKey := liveAccountSlotKeyPrefix + strconv.FormatInt(acc.ID, 10)
		waitKey := accountWaitKeyPrefix + strconv.FormatInt(acc.ID, 10)
		pipe.ZRemRangeByScore(ctx, slotKey, "-inf", strconv.FormatInt(cutoffTime, 10))
		pipe.ZRemRangeByScore(ctx, liveKey, "-inf", strconv.FormatInt(now.Unix()-liveLeaseTTLSeconds, 10))
		ac := accountCmds{
			id:             acc.ID,
			maxConcurrency: acc.MaxConcurrency,
			zcardCmd:       pipe.ZCard(ctx, slotKey),
			liveCmd:        pipe.ZCard(ctx, liveKey),
			getCmd:         pipe.Get(ctx, waitKey),
		}
		cmds = append(cmds, ac)
	}

	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("pipeline exec: %w", err)
	}

	loadMap := make(map[int64]*service.AccountLoadInfo, len(accounts))
	for _, ac := range cmds {
		currentConcurrency := int(ac.zcardCmd.Val() + ac.liveCmd.Val())
		waitingCount := 0
		if v, err := ac.getCmd.Int(); err == nil {
			waitingCount = v
		}
		loadRate := 0
		if ac.maxConcurrency > 0 {
			loadRate = (currentConcurrency + waitingCount) * 100 / ac.maxConcurrency
		}
		loadMap[ac.id] = &service.AccountLoadInfo{
			AccountID:          ac.id,
			CurrentConcurrency: currentConcurrency,
			WaitingCount:       waitingCount,
			LoadRate:           loadRate,
		}
	}

	return loadMap, nil
}

func (c *concurrencyCache) GetUsersLoadBatch(ctx context.Context, users []service.UserWithConcurrency) (map[int64]*service.UserLoadInfo, error) {
	if len(users) == 0 {
		return map[int64]*service.UserLoadInfo{}, nil
	}

	// 使用 Pipeline 替代 Lua 脚本，兼容 Redis Cluster。
	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TIME: %w", err)
	}
	cutoffTime := now.Unix() - int64(c.slotTTLSeconds)

	pipe := c.rdb.Pipeline()

	type userCmds struct {
		id             int64
		maxConcurrency int
		zcardCmd       *redis.IntCmd
		liveCmd        *redis.IntCmd
		getCmd         *redis.StringCmd
	}
	cmds := make([]userCmds, 0, len(users))
	for _, u := range users {
		slotKey := userSlotKeyPrefix + strconv.FormatInt(u.ID, 10)
		liveKey := liveUserSlotKeyPrefix + strconv.FormatInt(u.ID, 10)
		waitKey := waitQueueKeyPrefix + strconv.FormatInt(u.ID, 10)
		pipe.ZRemRangeByScore(ctx, slotKey, "-inf", strconv.FormatInt(cutoffTime, 10))
		pipe.ZRemRangeByScore(ctx, liveKey, "-inf", strconv.FormatInt(now.Unix()-liveLeaseTTLSeconds, 10))
		uc := userCmds{
			id:             u.ID,
			maxConcurrency: u.MaxConcurrency,
			zcardCmd:       pipe.ZCard(ctx, slotKey),
			liveCmd:        pipe.ZCard(ctx, liveKey),
			getCmd:         pipe.Get(ctx, waitKey),
		}
		cmds = append(cmds, uc)
	}

	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("pipeline exec: %w", err)
	}

	loadMap := make(map[int64]*service.UserLoadInfo, len(users))
	for _, uc := range cmds {
		currentConcurrency := int(uc.zcardCmd.Val() + uc.liveCmd.Val())
		waitingCount := 0
		if v, err := uc.getCmd.Int(); err == nil {
			waitingCount = v
		}
		loadRate := 0
		if uc.maxConcurrency > 0 {
			loadRate = (currentConcurrency + waitingCount) * 100 / uc.maxConcurrency
		}
		loadMap[uc.id] = &service.UserLoadInfo{
			UserID:             uc.id,
			CurrentConcurrency: currentConcurrency,
			WaitingCount:       waitingCount,
			LoadRate:           loadRate,
		}
	}

	return loadMap, nil
}

func (c *concurrencyCache) CleanupExpiredAccountSlots(ctx context.Context, accountID int64) error {
	key := accountSlotKey(accountID)
	_, err := cleanupExpiredSlotsScript.Run(ctx, c.rdb, []string{key}, c.slotTTLSeconds).Result()
	if err == nil {
		// 单账号清理后同步索引，保持后台批量清理的候选集准确。
		c.refreshAccountActiveIndex(ctx, accountID)
	}
	return err
}

// CleanupExpiredAccountSlotKeys 处理账号与用户两个活跃索引中已到期的候选。
// （方法名中的 Account 是历史遗留，保留以避免接口变更；实际同时回收两个索引，
// 否则 user 索引的过期成员没有任何清理路径，会无界累积。）
func (c *concurrencyCache) CleanupExpiredAccountSlotKeys(ctx context.Context) error {
	if err := c.reconcileExpiredIndexCandidates(ctx, accountSlotIndex); err != nil {
		return err
	}
	return c.reconcileExpiredIndexCandidates(ctx, userSlotIndex)
}

// reconcileExpiredIndexCandidates 处理单个活跃索引中 score 已到期的候选：
// 无真实负载则移除 member；仍有负载则按真实负载批量刷新 score。
func (c *concurrencyCache) reconcileExpiredIndexCandidates(ctx context.Context, spec slotIndexSpec) error {
	now, err := c.redisUnixSeconds(ctx)
	if err != nil {
		return err
	}
	members, err := c.rdb.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key:     spec.indexKey,
		Start:   "-inf",
		Stop:    strconv.FormatInt(now, 10),
		ByScore: true,
		Count:   activeIndexCleanupBatchSize,
	}).Result()
	if err != nil {
		return fmt.Errorf("read expired index %s: %w", spec.indexKey, err)
	}

	return c.reconcileIndexMembers(ctx, spec, members, now)
}

// CleanupStaleProcessSlots 启动时清理非当前进程前缀的槽位。
// 清理范围来自活跃索引（含 score 已过期的成员——它们往往正是崩溃进程留下的残留），
// 避免在 Redis 上 SCAN 全部 concurrency:* 键；另有一次性迁移清扫兜底索引机制上线前的遗留等待计数。
// API Key 槽位（concurrency:api_key:*）是 stats-only 数据：每次 Track/读取都会按分数
// 裁剪过期成员，key 自带 TTL，可在一个 slot TTL 内自愈，因此不参与启动清理。
func (c *concurrencyCache) CleanupStaleProcessSlots(ctx context.Context, activeRequestPrefix string) error {
	if activeRequestPrefix == "" {
		return nil
	}
	if err := c.sweepLegacyWaitKeysOnce(ctx); err != nil {
		return err
	}
	now, err := c.redisUnixSeconds(ctx)
	if err != nil {
		return err
	}

	accountMembers, err := c.allIndexMembers(ctx, accountActiveIndexKey)
	if err != nil {
		return err
	}
	if err := c.cleanupStaleProcessSlotsForIndex(ctx, accountSlotIndex, accountMembers, activeRequestPrefix, now); err != nil {
		return err
	}

	userMembers, err := c.allIndexMembers(ctx, userActiveIndexKey)
	if err != nil {
		return err
	}
	return c.cleanupStaleProcessSlotsForIndex(ctx, userSlotIndex, userMembers, activeRequestPrefix, now)
}

// sweepLegacyWaitKeysOnce 一次性清扫活跃索引机制上线前遗留的等待计数键。
// 等待计数在有流量时会不断刷新 TTL、无法自然过期，而索引不认识旧键，
// 因此这里例外地做一次 SCAN，用 marker 键保证整个 Redis 数据生命周期内只执行一次。
// 先清扫后写 marker：清扫失败时下次启动会重试；并发实例重复清扫是幂等的。
func (c *concurrencyCache) sweepLegacyWaitKeysOnce(ctx context.Context) error {
	exists, err := c.rdb.Exists(ctx, legacyWaitSweepMarkerKey).Result()
	if err != nil {
		return fmt.Errorf("check legacy wait sweep marker: %w", err)
	}
	if exists > 0 {
		return nil
	}
	for _, pattern := range []string{accountWaitKeyPrefix + "*", waitQueueKeyPrefix + "*"} {
		var cursor uint64
		for {
			keys, next, err := c.rdb.Scan(ctx, cursor, pattern, 200).Result()
			if err != nil {
				return fmt.Errorf("scan legacy wait keys %s: %w", pattern, err)
			}
			if len(keys) > 0 {
				if err := c.rdb.Del(ctx, keys...).Err(); err != nil {
					return fmt.Errorf("delete legacy wait keys: %w", err)
				}
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	}
	if err := c.rdb.Set(ctx, legacyWaitSweepMarkerKey, "1", 0).Err(); err != nil {
		return fmt.Errorf("set legacy wait sweep marker: %w", err)
	}
	return nil
}

// allIndexMembers 返回索引中全部 member（含 score 已过期的）。
// 启动清理必须覆盖过期成员：长时间停机后 score 过期的候选恰恰最可能持有死进程残留。
func (c *concurrencyCache) allIndexMembers(ctx context.Context, indexKey string) ([]string, error) {
	members, err := c.rdb.ZRange(ctx, indexKey, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("read active index %s: %w", indexKey, err)
	}
	return members, nil
}

// cleanupStaleProcessSlotsForIndex captures cleanup candidates, then atomically
// rechecks them and rebuilds the index from current regular/live/wait load.
func (c *concurrencyCache) cleanupStaleProcessSlotsForIndex(
	ctx context.Context,
	spec slotIndexSpec,
	members []string,
	activeRequestPrefix string,
	now int64,
) error {
	type capturedCleanup struct {
		id   int64
		args []any
	}
	captured := make([]capturedCleanup, 0, len(members))
	for _, member := range members {
		id, err := strconv.ParseInt(member, 10, 64)
		if err != nil || id <= 0 {
			if err := removeInvalidSlotIndexMemberScript.Run(ctx, c.rdb, []string{spec.indexKey}, member, now).Err(); err != nil {
				return fmt.Errorf("remove invalid index member %s: %w", member, err)
			}
			continue
		}
		pipe := c.rdb.Pipeline()
		slots := pipe.ZRangeWithScores(ctx, spec.slotKey(id), 0, -1)
		wait := pipe.Get(ctx, spec.waitKey(id))
		waitTTL := pipe.PTTL(ctx, spec.waitKey(id))
		indexScore := pipe.ZScore(ctx, spec.indexKey, member)
		if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
			return fmt.Errorf("capture stale process slots %s: %w", spec.slotKey(id), err)
		}
		args := []any{member, c.slotTTLSeconds, liveLeaseTTLSeconds, c.waitQueueTTLSeconds,
			activeRequestPrefix, wait.Val(), waitTTL.Val().Milliseconds(), ""}
		if indexScore.Err() == nil {
			args[7] = strconv.FormatFloat(indexScore.Val(), 'f', -1, 64)
		}
		for _, slot := range slots.Val() {
			args = append(args, slot.Member, slot.Score)
		}
		captured = append(captured, capturedCleanup{id: id, args: args})
	}
	for _, candidate := range captured {
		id, args := candidate.id, candidate.args
		if err := reconcileSlotIndexScript.Run(ctx, c.rdb,
			[]string{spec.indexKey, spec.slotKey(id), spec.liveKey(id), spec.waitKey(id)}, args...).Err(); err != nil {
			return fmt.Errorf("cleanup stale process slots %s: %w", spec.slotKey(id), err)
		}
	}
	return nil
}
