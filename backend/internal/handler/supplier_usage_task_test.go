package handler

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSupplierUsageTaskRunsSynchronouslyDespiteCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
		WorkerCount: 1, QueueSize: 1, TaskTimeout: time.Second, OverflowPolicy: "drop",
	})
	t.Cleanup(pool.Stop)
	block, release := make(chan struct{}), make(chan struct{})
	pool.Submit(func(context.Context) { close(block); <-release })
	<-block
	defer close(release)
	pool.Submit(func(context.Context) {})
	h := &OpenAIGatewayHandler{usageRecordWorkerPool: pool}
	called := false
	h.submitOpenAIUsageRecordTask(ctx, &service.OpenAIForwardResult{
		SupplierDispatch: &service.SupplierDispatchSnapshot{AttemptID: "server-attempt"},
	}, func(billingCtx context.Context) {
		require.NoError(t, billingCtx.Err())
		called = true
	})
	require.True(t, called, "supplier billing must finish before handler returns, never enter the async pool")
}
