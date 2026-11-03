package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestSupplierEmailCacheAtomicConsumption(t *testing.T) {
	srv := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	defer rdb.Close()
	c := &emailCache{rdb: rdb}
	ctx := context.Background()
	for _, key := range []string{"supplier:u@example.com", "u@example.com"} {
		require.NoError(t, c.SetVerificationCode(ctx, key, &service.VerificationCodeData{Code: "123456", ExpiresAt: time.Now().Add(time.Minute)}, time.Minute))
	}
	var wg sync.WaitGroup
	out := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := c.ConsumeSupplierVerificationCode(ctx, "supplier:u@example.com", "123456")
			if err != nil {
				out <- false
				return
			}
			out <- ok
		}()
	}
	wg.Wait()
	close(out)
	won := 0
	for ok := range out {
		if ok {
			won++
		}
	}
	require.Equal(t, 1, won)
	proof, err := c.GetVerificationCode(ctx, "u@example.com")
	require.NoError(t, err)
	require.Equal(t, "123456", proof.Code)
	_, err = c.ConsumeSupplierVerificationCode(ctx, "u@example.com", "123456")
	require.Error(t, err)
}
