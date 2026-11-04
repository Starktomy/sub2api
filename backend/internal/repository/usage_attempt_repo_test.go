package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/lib/pq"
)

func usageTestID(c string) string { return strings.Repeat(c, 64) }
func usageTestAttempt() service.UsageAttemptInput {
	return service.UsageAttemptInput{ID: usageTestID("a"), OperationID: usageTestID("b"), RequestDigest: usageTestID("c"), AccountID: 1, CredentialVersion: 1}
}
func usageTestUnknown() service.UsageFactInput {
	return service.UsageFactInput{ObservationID: usageTestID("d"), EvidenceDigest: usageTestID("e"), Source: "none", Quality: "unknown", Partial: true, Outcome: "timeout"}
}
func TestUsageJournalCommitAmbiguityNeverRetries(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewUsageAttemptRepository(db, 10)
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE usage_attempts SET send_intent").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(errors.New("private database diagnostic"))
	err = repo.MarkSendIntent(context.Background(), service.UsageAttemptFence{AttemptID: usageTestID("a"), Owner: usageTestID("f"), Generation: 1})
	if !errors.Is(err, service.ErrUsageJournalAmbiguous) {
		t.Fatalf("expected ambiguity: %v", err)
	}
	// Model a committed write whose acknowledgement was lost: local lookup
	// recovers the barrier, but a second MarkSendIntent can never succeed.
	mock.ExpectQuery("SELECT id,operation_id").WillReturnRows(sqlmock.NewRows([]string{"id", "operation_id", "account_id", "credential_version", "request_digest", "owner", "generation", "lease_until", "send_intent", "status"}).AddRow(usageTestID("a"), usageTestID("b"), 1, 1, usageTestID("c"), usageTestID("f"), 1, time.Now(), true, "send_intent"))
	a, lookupErr := repo.Lookup(context.Background(), usageTestID("a"))
	if lookupErr != nil || !a.SendIntent {
		t.Fatal("local recovery failed", lookupErr)
	}
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE usage_attempts SET send_intent").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	if err = repo.MarkSendIntent(context.Background(), service.UsageAttemptFence{AttemptID: usageTestID("a"), Owner: usageTestID("f"), Generation: 1}); !errors.Is(err, service.ErrUsageJournalFence) {
		t.Fatal("ambiguous send replayed", err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestUsageJournalUnavailable(t *testing.T) {
	repo := NewUsageAttemptRepository(nil, 10)
	if err := repo.Create(context.Background(), usageTestAttempt()); !errors.Is(err, service.ErrUsageJournalUnavailable) {
		t.Fatal(err)
	}
}

// Explicit disposable-local-PG opt-in only; never reads project/RDS configuration.
func TestUsageJournalLocalPostgres(t *testing.T) {
	dsn := os.Getenv("KIRO_JOURNAL_LOCAL_PG_DSN")
	if dsn == "" {
		t.Skip("temporary local PostgreSQL not requested")
	}
	if !strings.HasPrefix(dsn, "host=/tmp/kiro-journal-pg.") || strings.Contains(dsn, "hostaddr") || strings.Contains(dsn, "postgres://") || strings.Count(dsn, "host=") != 1 {
		t.Fatal("only disposable local Unix socket allowed")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(3)
	ctx := context.Background()
	repo := NewUsageAttemptRepository(db, 2)
	a := usageTestAttempt()
	if err = repo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err = repo.Create(ctx, a); err != nil {
		t.Fatal("duplicate", err)
	}
	conflict := a
	conflict.RequestDigest = usageTestID("f")
	if !errors.Is(repo.Create(ctx, conflict), service.ErrUsageJournalConflict) {
		t.Fatal("conflict accepted")
	}
	fence, err := repo.Claim(ctx, a.ID, usageTestID("1"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.MarkSendIntent(ctx, *fence); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(repo.MarkSendIntent(ctx, *fence), service.ErrUsageJournalFence) {
		t.Fatal("send barrier replayed")
	}
	unknown := usageTestUnknown()
	first, err := repo.Observe(ctx, *fence, unknown)
	if err != nil {
		t.Fatal(err)
	}
	again, err := repo.Observe(ctx, *fence, unknown)
	if err != nil || first.Revision != again.Revision {
		t.Fatal("duplicate not idempotent", err)
	}
	changed := unknown
	changed.Outcome = "cancelled"
	if _, err = repo.Observe(ctx, *fence, changed); !errors.Is(err, service.ErrUsageJournalConflict) {
		t.Fatal("observation conflict accepted", err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE usage_attempts SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	next, err := repo.Claim(ctx, a.ID, usageTestID("2"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	late := unknown
	late.ObservationID = usageTestID("3")
	if _, err = repo.Observe(ctx, *fence, late); !errors.Is(err, service.ErrUsageJournalFence) {
		t.Fatal("late owner accepted", err)
	}
	if !errors.Is(repo.MarkSendIntent(ctx, *next), service.ErrUsageJournalFence) {
		t.Fatal("reclaim allowed upstream replay")
	}
	zero := 0.0
	partial := service.UsageFactInput{ObservationID: usageTestID("4"), EvidenceDigest: usageTestID("5"), Source: "provider_reported", Quality: "reported", Outcome: "observed", Partial: true, Metrics: []service.UsageMetric{{Name: "usage", Unit: "credits", Value: &zero}}}
	if _, err = repo.Observe(ctx, *next, partial); err != nil {
		t.Fatal(err)
	}
	pending, err := repo.Pending(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].Reason != "usage_unknown" {
		t.Fatal("partial became known", pending, err)
	}
	complete := partial
	complete.Partial = false
	complete.ObservationID = usageTestID("6")
	complete.Supersedes = 2
	if _, err = repo.Observe(ctx, *next, complete); err != nil {
		t.Fatal(err)
	}
	pending, err = repo.Pending(ctx, 10)
	if err != nil || pending[0].Reason != "pricing_missing" || pending[0].State != "blocked" {
		t.Fatal("unpriced did not block", pending, err)
	}
	saved, err := repo.LookupObservation(ctx, a.ID, unknown.ObservationID)
	if err != nil || saved.Outcome != "timeout" || len(saved.Metrics) != 0 {
		t.Fatal("past fact mutated", err)
	}
	got, err := repo.Lookup(ctx, a.ID)
	if err != nil || !got.SendIntent {
		t.Fatal("lookup lost send barrier", err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE usage_fact_revisions SET content_digest=$1 WHERE attempt_id=$2`, usageTestID("9"), a.ID); err == nil {
		t.Fatal("fact update accepted")
	}
	second := a
	second.ID = usageTestID("7")
	second.OperationID = usageTestID("8")
	if err = repo.Create(ctx, second); err != nil {
		t.Fatal(err)
	}
	third := a
	third.ID = usageTestID("9")
	third.OperationID = usageTestID("0")
	if !errors.Is(repo.Create(ctx, third), service.ErrUsageJournalFull) {
		t.Fatal("capacity not enforced")
	}
	for i := 4; i <= 256; i++ {
		f := unknown
		f.ObservationID = fmt.Sprintf("%064x", i+100)
		if _, err = repo.Observe(ctx, *next, f); err != nil {
			t.Fatal("bounded append", i, err)
		}
	}
	overflow := unknown
	overflow.ObservationID = fmt.Sprintf("%064x", 999)
	if _, err = repo.Observe(ctx, *next, overflow); !errors.Is(err, service.ErrUsageJournalFull) {
		t.Fatal("revision bound not enforced", err)
	}
}
