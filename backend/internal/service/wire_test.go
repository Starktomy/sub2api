package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zeromicro/go-zero/core/collection"
)

func TestProvideTimingWheelService_ReturnsError(t *testing.T) {
	original := newTimingWheel
	t.Cleanup(func() { newTimingWheel = original })

	newTimingWheel = func(_ time.Duration, _ int, _ collection.Execute) (*collection.TimingWheel, error) {
		return nil, errors.New("boom")
	}

	lifecycle := NewRuntimeLifecycle(nil)
	svc, err := ProvideTimingWheelService(lifecycle)
	if err != nil || svc == nil {
		t.Fatalf("managed construction should succeed before startup: %v", err)
	}
	if err := lifecycle.Start(context.Background()); err == nil {
		t.Fatal("expected wheel creation failure during lifecycle startup")
	}
	_ = lifecycle.Stop(context.Background())
}

func TestProvideTimingWheelService_Success(t *testing.T) {
	lifecycle := NewRuntimeLifecycle(nil)
	svc, err := ProvideTimingWheelService(lifecycle)
	if err != nil {
		t.Fatalf("期望 err 为 nil，但得到: %v", err)
	}
	if svc == nil {
		t.Fatalf("期望 svc 非空，但得到 nil")
	}
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
