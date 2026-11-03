package repository

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestLiveLeaseReplacesRegularSlotsAndCountsTowardLimits(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	regular := NewConcurrencyCache(client, 15, 900)
	live, ok := regular.(service.LiveConcurrencyCache)
	require.True(t, ok)
	ctx := context.Background()

	accountAcquired, err := regular.AcquireAccountSlot(ctx, 10, 1, "regular-account")
	require.NoError(t, err)
	require.True(t, accountAcquired)
	userAcquired, err := regular.AcquireUserSlot(ctx, 20, 1, "regular-user")
	require.NoError(t, err)
	require.True(t, userAcquired)

	acquired, err := live.AcquireLiveLease(ctx, 10, 1, 20, 1, 30, "live-lease", true)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, regular.ReleaseAccountSlot(ctx, 10, "regular-account"))
	require.NoError(t, regular.ReleaseUserSlot(ctx, 20, "regular-user"))

	accountCount, err := regular.GetAccountConcurrency(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, 1, accountCount)
	userCount, err := regular.GetUserConcurrency(ctx, 20)
	require.NoError(t, err)
	require.Equal(t, 1, userCount)
	accountAcquired, err = regular.AcquireAccountSlot(ctx, 10, 1, "ordinary-blocked")
	require.NoError(t, err)
	require.False(t, accountAcquired)

	refreshed, err := live.RefreshLiveLease(ctx, 10, 20, 30, "live-lease")
	require.NoError(t, err)
	require.True(t, refreshed)
	require.NoError(t, live.ReleaseLiveLease(ctx, 10, 20, 30, "live-lease"))
	accountAcquired, err = regular.AcquireAccountSlot(ctx, 10, 1, "ordinary-allowed")
	require.NoError(t, err)
	require.True(t, accountAcquired)
}

func TestLiveLeaseExpiresWithoutRefresh(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	regular := NewConcurrencyCache(client, 15, 900)
	live, ok := regular.(service.LiveConcurrencyCache)
	require.True(t, ok)
	ctx := context.Background()

	acquired, err := live.AcquireLiveLease(ctx, 10, 1, 20, 1, 30, "expired-live", false)
	require.NoError(t, err)
	require.True(t, acquired)

	redisServer.FastForward(61 * time.Second)
	acquired, err = regular.AcquireAccountSlot(ctx, 10, 1, "ordinary-after-expiry")
	require.NoError(t, err)
	require.True(t, acquired)
	refreshed, err := live.RefreshLiveLease(ctx, 10, 20, 30, "expired-live")
	require.NoError(t, err)
	require.False(t, refreshed)
}

// cleanupInterleaveHook runs a real second Redis client after candidate capture,
// immediately before cleanup applies. It never changes production cleanup code.
type cleanupInterleaveHook struct {
	before   func()
	pipeline bool
}

func (h *cleanupInterleaveHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h *cleanupInterleaveHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if !h.pipeline && h.before != nil && (cmd.Name() == "eval" || cmd.Name() == "evalsha") {
			before := h.before
			h.before = nil
			before()
		}
		return next(ctx, cmd)
	}
}

func (h *cleanupInterleaveHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		if h.pipeline && h.before != nil && len(cmds) > 0 && cmds[0].Name() == "eval" {
			before := h.before
			h.before = nil
			before()
		}
		return next(ctx, cmds)
	}
}

func TestConcurrencyIndexCleanupInterleavedAcquisition(t *testing.T) {
	for _, rebuild := range []bool{false, true} {
		for _, spec := range []slotIndexSpec{accountSlotIndex, userSlotIndex} {
			name := strings.TrimPrefix(spec.indexKey, "concurrency:")
			if rebuild {
				name += "/rebuild"
			} else {
				name += "/expired-batch"
			}
			t.Run(name, func(t *testing.T) {
				s := miniredis.RunT(t)
				s.SetTime(time.Unix(1800000000, 0))
				ctx := context.Background()
				client := redis.NewClient(&redis.Options{Addr: s.Addr()})
				writer := redis.NewClient(&redis.Options{Addr: s.Addr()})
				t.Cleanup(func() { _ = client.Close(); _ = writer.Close() })
				cache := NewConcurrencyCache(client, 15, 1800).(*concurrencyCache)
				acquirer := NewConcurrencyCache(writer, 15, 1800).(*concurrencyCache)
				now, err := cache.redisUnixSeconds(ctx)
				require.NoError(t, err)
				// 1: new regular; 2: new live; 3: new wait; 4: expired;
				// 5: same request ID re-acquired with a changed score; 6: stale-only.
				for id := int64(1); id <= 6; id++ {
					require.NoError(t, writer.ZAdd(ctx, spec.indexKey, redis.Z{Score: float64(now - 1), Member: strconv.FormatInt(id, 10)}).Err())
					require.NoError(t, writer.ZAdd(ctx, spec.slotKey(id), redis.Z{Score: float64(now - 901), Member: "old-request"}).Err())
				}
				require.NoError(t, writer.ZAdd(ctx, spec.liveKey(4), redis.Z{Score: float64(now - 61), Member: "expired-live"}).Err())
				require.NoError(t, writer.Set(ctx, spec.waitKey(4), 0, time.Minute).Err())
				// Startup capture includes this ID; interleave on the first apply so
				// all acquisitions happen before any destructive cleanup.
				hook := &cleanupInterleaveHook{pipeline: !rebuild}
				hook.before = func() {
					for _, id := range []int64{1, 5} {
						var acquired bool
						var err error
						if spec.indexKey == accountActiveIndexKey {
							acquired, err = acquirer.AcquireAccountSlot(ctx, id, 10, "old-request")
						} else {
							acquired, err = acquirer.AcquireUserSlot(ctx, id, 10, "old-request")
						}
						require.NoError(t, err)
						require.True(t, acquired)
					}
					acquired, err := acquirer.AcquireLiveLease(ctx, 2, 10, 2, 10, 2, "fresh-live", false)
					require.NoError(t, err)
					require.True(t, acquired)
					require.NoError(t, writer.Set(ctx, spec.waitKey(3), 2, 1800*time.Second).Err())
					// A newer, longer-lived index must not be shortened by cleanup.
					for _, id := range []int64{1, 2, 3, 5} {
						require.NoError(t, writer.ZAdd(ctx, spec.indexKey, redis.Z{Score: float64(now + 3600), Member: strconv.FormatInt(id, 10)}).Err())
					}
					require.NoError(t, writer.Expire(ctx, spec.slotKey(1), 2*time.Hour).Err())
				}
				client.AddHook(hook)
				if rebuild {
					require.NoError(t, writer.Set(ctx, legacyWaitSweepMarkerKey, "1", 0).Err())
					require.NoError(t, cache.CleanupStaleProcessSlots(ctx, "current-"))
				} else {
					require.NoError(t, cache.CleanupExpiredAccountSlotKeys(ctx))
				}
				require.Nil(t, hook.before, "interleave must execute")
				for _, id := range []int64{1, 2, 3, 5} {
					score, err := writer.ZScore(ctx, spec.indexKey, strconv.FormatInt(id, 10)).Result()
					require.NoError(t, err)
					require.Equal(t, float64(now+3600), score)
				}
				for _, id := range []int64{4, 6} {
					_, err := writer.ZScore(ctx, spec.indexKey, strconv.FormatInt(id, 10)).Result()
					require.ErrorIs(t, err, redis.Nil)
				}
				_, err = writer.ZScore(ctx, spec.slotKey(1), "old-request").Result()
				require.NoError(t, err)
				_, err = writer.ZScore(ctx, spec.liveKey(2), "fresh-live").Result()
				require.NoError(t, err)
				require.Equal(t, "2", writer.Get(ctx, spec.waitKey(3)).Val())
				require.Equal(t, 2*time.Hour, writer.TTL(ctx, spec.slotKey(1)).Val())
				require.EqualValues(t, 0, writer.ZCard(ctx, spec.slotKey(4)).Val())
				require.EqualValues(t, 0, writer.ZCard(ctx, spec.liveKey(4)).Val())
			})
		}
	}
}

func TestConcurrencyIndexCleanupErrorPreservesCandidates(t *testing.T) {
	s := miniredis.RunT(t)
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewConcurrencyCache(client, 15, 900).(*concurrencyCache)
	now, err := cache.redisUnixSeconds(ctx)
	require.NoError(t, err)
	require.NoError(t, client.ZAdd(ctx, accountActiveIndexKey, redis.Z{Score: float64(now - 1), Member: "1"}).Err())
	require.NoError(t, client.ZAdd(ctx, accountSlotKey(1), redis.Z{Score: float64(now - 1000), Member: "expired"}).Err())
	require.NoError(t, client.Set(ctx, liveAccountSlotKey(1), "wrong-type", 0).Err())
	err = cache.CleanupExpiredAccountSlotKeys(ctx)
	require.Error(t, err)
	require.False(t, errors.Is(err, redis.Nil))
	_, err = client.ZScore(ctx, accountActiveIndexKey, "1").Result()
	require.NoError(t, err)
	_, err = client.ZScore(ctx, accountSlotKey(1), "expired").Result()
	require.NoError(t, err, "script validates types before destructive writes")
}

func TestConcurrencyIndexRebuildRechecksCapturedMemberScore(t *testing.T) {
	s := miniredis.RunT(t)
	s.SetTime(time.Unix(1800000000, 0))
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: s.Addr()})
	writer := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { _ = client.Close(); _ = writer.Close() })
	cache := NewConcurrencyCache(client, 15, 900).(*concurrencyCache)
	now, err := cache.redisUnixSeconds(ctx)
	require.NoError(t, err)
	require.NoError(t, writer.ZAdd(ctx, accountActiveIndexKey, redis.Z{Score: float64(now - 1), Member: "1"}).Err())
	require.NoError(t, writer.ZAdd(ctx, accountSlotKey(1),
		redis.Z{Score: float64(now - 10), Member: "reacquired"},
		redis.Z{Score: float64(now - 10), Member: "dead-process"},
		redis.Z{Score: float64(now), Member: "current-kept"},
	).Err())
	require.NoError(t, writer.Set(ctx, accountWaitKey(1), 3, time.Minute).Err())
	hook := &cleanupInterleaveHook{before: func() {
		// Simulate the acquisition/index-publication gap: fresh slot scores
		// exist, but the index still has the exact captured score.
		_, _, err := runScriptInt64Pair(ctx, writer, acquireScript,
			[]string{accountSlotKey(1), liveAccountSlotKey(1)}, 10, 900, "reacquired")
		require.NoError(t, err)
		_, _, err = runScriptInt64Pair(ctx, writer, acquireScript,
			[]string{accountSlotKey(1), liveAccountSlotKey(1)}, 10, 900, "new-request")
		require.NoError(t, err)
	}}
	client.AddHook(hook)
	require.NoError(t, cache.cleanupStaleProcessSlotsForIndex(ctx, accountSlotIndex, []string{"1"}, "current-", now))
	require.Nil(t, hook.before)
	members, err := writer.ZRange(ctx, accountSlotKey(1), 0, -1).Result()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"reacquired", "new-request", "current-kept"}, members)
	_, err = writer.Get(ctx, accountWaitKey(1)).Result()
	require.ErrorIs(t, err, redis.Nil, "unchanged legacy wait count is still removed")
	score, err := writer.ZScore(ctx, accountActiveIndexKey, "1").Result()
	require.NoError(t, err)
	require.Equal(t, float64(now+900), score)
}

func TestConcurrencyIndexCleanupBatchChunksAndLimit(t *testing.T) {
	s := miniredis.RunT(t)
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewConcurrencyCache(client, 15, 900).(*concurrencyCache)
	now, err := cache.redisUnixSeconds(ctx)
	require.NoError(t, err)
	members := make([]redis.Z, 0, activeIndexCleanupBatchSize+1)
	for id := 1; id <= activeIndexCleanupBatchSize+1; id++ {
		members = append(members, redis.Z{Score: float64(now - 1), Member: strconv.Itoa(id)})
	}
	require.NoError(t, client.ZAdd(ctx, accountActiveIndexKey, members...).Err())
	require.NoError(t, cache.CleanupExpiredAccountSlotKeys(ctx))
	require.EqualValues(t, 1, client.ZCard(ctx, accountActiveIndexKey).Val(), "one call remains bounded across pipeline chunks")
	require.NoError(t, cache.CleanupExpiredAccountSlotKeys(ctx))
	require.EqualValues(t, 0, client.ZCard(ctx, accountActiveIndexKey).Val())
}
