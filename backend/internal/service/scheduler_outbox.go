package service

import (
	"context"
	"time"
)

type SchedulerOutboxEvent struct {
	ID        int64
	EventType string
	AccountID *int64
	GroupID   *int64
	Payload   map[string]any
	CreatedAt time.Time
}

// SchedulerOutboxRepository 提供调度 outbox 的读取接口。
type SchedulerOutboxRepository interface {
	// ListPendingAndReleaseDedup releases dedup before authoritative reads, including late lower IDs.
	ListPendingAndReleaseDedup(ctx context.Context, limit int) ([]SchedulerOutboxEvent, error)
	// FirstCreatedAtAfter 返回指定水位之后第一条待消费事件的创建时间，不领取事件或修改去重键。
	FirstCreatedAtAfter(ctx context.Context, afterID int64) (time.Time, bool, error)
	MaxID(ctx context.Context) (int64, error)
	// AcknowledgeProcessed removes only exact IDs whose effects completed successfully.
	AcknowledgeProcessed(ctx context.Context, ids []int64) error
}
