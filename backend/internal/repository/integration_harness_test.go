//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/lib/pq"
	redisclient "github.com/redis/go-redis/v9"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

const (
	redisImageTag    = "redis:8.4-alpine"
	postgresImageTag = "postgres:18.1-alpine3.23"
)

var (
	integrationDB        *sql.DB
	integrationEntClient *dbent.Client
	integrationRedis     *redisclient.Client

	redisNamespaceSeq uint64

	// Database integration tests are serial (no t.Parallel). Track ownership so
	// cleanup is registered before transaction rollbacks, including tests that
	// obtain a committing client after opening a rollback-only transaction.
	integrationFixtureTests        sync.Map // map[*testing.T]*integrationFixtureTest
	integrationFixtureTables       []string
	integrationFixtureSeededTables []string
)

type integrationFixtureTest struct {
	committed bool
}

func TestMain(m *testing.M) {
	ctx := context.Background()

	if err := timezone.Init("UTC"); err != nil {
		log.Printf("failed to init timezone: %v", err)
		os.Exit(1)
	}

	if !dockerIsAvailable(ctx) {
		// In CI we expect Docker to be available so integration tests should fail loudly.
		if os.Getenv("CI") != "" {
			log.Printf("docker is not available (CI=true); failing integration tests")
			os.Exit(1)
		}
		log.Printf("docker is not available; skipping integration tests (start Docker to enable)")
		os.Exit(0)
	}

	postgresImage := selectDockerImage(ctx, postgresImageTag)
	pgContainer, err := tcpostgres.Run(
		ctx,
		postgresImage,
		tcpostgres.WithDatabase("sub2api_test"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		log.Printf("failed to start postgres container: %v", err)
		os.Exit(1)
	}
	defer func() { _ = pgContainer.Terminate(ctx) }()

	redisContainer, err := tcredis.Run(
		ctx,
		redisImageTag,
	)
	if err != nil {
		log.Printf("failed to start redis container: %v", err)
		os.Exit(1)
	}
	defer func() { _ = redisContainer.Terminate(ctx) }()

	dsn, err := pgContainer.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	if err != nil {
		log.Printf("failed to get postgres dsn: %v", err)
		os.Exit(1)
	}

	integrationDB, err = openSQLWithRetry(ctx, dsn, 30*time.Second)
	if err != nil {
		log.Printf("failed to open sql db: %v", err)
		os.Exit(1)
	}
	if err := ApplyMigrations(ctx, integrationDB); err != nil {
		log.Printf("failed to apply db migrations: %v", err)
		os.Exit(1)
	}

	if err := snapshotIntegrationFixtures(ctx); err != nil {
		log.Printf("failed to snapshot migration baseline: %v", err)
		os.Exit(1)
	}

	// 创建 ent client 用于集成测试
	drv := entsql.OpenDB(dialect.Postgres, integrationDB)
	integrationEntClient = dbent.NewClient(dbent.Driver(drv))

	redisHost, err := redisContainer.Host(ctx)
	if err != nil {
		log.Printf("failed to get redis host: %v", err)
		os.Exit(1)
	}
	redisPort, err := redisContainer.MappedPort(ctx, "6379/tcp")
	if err != nil {
		log.Printf("failed to get redis port: %v", err)
		os.Exit(1)
	}

	integrationRedis = redisclient.NewClient(&redisclient.Options{
		Addr: fmt.Sprintf("%s:%d", redisHost, redisPort.Int()),
		DB:   0,
	})
	if err := integrationRedis.Ping(ctx).Err(); err != nil {
		log.Printf("failed to ping redis: %v", err)
		os.Exit(1)
	}

	code := m.Run()

	_ = integrationEntClient.Close()
	_ = integrationRedis.Close()
	_ = integrationDB.Close()

	os.Exit(code)
}

func dockerIsAvailable(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, "docker", "info")
	cmd.Env = os.Environ()
	return cmd.Run() == nil
}

// selectDockerImage resolves the container image for the harness.
//
// SUB2API_TEST_POSTGRES_IMAGE overrides the PostgreSQL image so the suite can be
// run against the oldest documented-supported server, not just the newest. That
// matters for SQL that behaves differently across major versions: jsonpath
// .datetime() only accepts the ISO-8601 "Z" designator from PostgreSQL 17 on, so
// a suite pinned to 18 cannot observe breakage on 14-16.
//
//	SUB2API_TEST_POSTGRES_IMAGE=postgres:15-alpine go test -tags integration ./internal/repository/
func selectDockerImage(ctx context.Context, preferred string) string {
	if override := strings.TrimSpace(os.Getenv("SUB2API_TEST_POSTGRES_IMAGE")); override != "" &&
		strings.HasPrefix(preferred, "postgres:") {
		return override
	}
	if dockerImageExists(ctx, preferred) {
		return preferred
	}

	return preferred
}

func dockerImageExists(ctx context.Context, image string) bool {
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", image)
	cmd.Env = os.Environ()
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

func openSQLWithRetry(ctx context.Context, dsn string, timeout time.Duration) (*sql.DB, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			lastErr = err
			time.Sleep(250 * time.Millisecond)
			continue
		}

		if err := pingWithTimeout(ctx, db, 2*time.Second); err != nil {
			lastErr = err
			_ = db.Close()
			time.Sleep(250 * time.Millisecond)
			continue
		}

		return db, nil
	}

	return nil, fmt.Errorf("db not ready after %s: %w", timeout, lastErr)
}

func pingWithTimeout(ctx context.Context, db *sql.DB, timeout time.Duration) error {
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return db.PingContext(pingCtx)
}

// snapshotIntegrationFixtures records the migrated state, including settings and
// seeded groups, in a private schema in the disposable PostgreSQL container.
// Migration bookkeeping is never truncated; maintenance_generation is also
// excluded so its fencing authority remains monotonic across test boundaries.
// Never use this reset mechanism with an external/shared application database.
func snapshotIntegrationFixtures(ctx context.Context) error {
	if _, err := integrationDB.ExecContext(ctx, "CREATE SCHEMA integration_fixture_baseline"); err != nil {
		return err
	}
	rows, err := integrationDB.QueryContext(ctx, `SELECT tablename FROM pg_tables
WHERE schemaname = 'public'
  AND tablename NOT IN ('schema_migrations', 'atlas_schema_revisions', 'maintenance_generation')
ORDER BY tablename`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			_ = rows.Close()
			return err
		}
		integrationFixtureTables = append(integrationFixtureTables, pq.QuoteIdentifier("public")+"."+pq.QuoteIdentifier(table))
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, table := range integrationFixtureTables {
		baseline := strings.Replace(table, `"public".`, `"integration_fixture_baseline".`, 1)
		result, err := integrationDB.ExecContext(ctx, "CREATE TABLE "+baseline+" AS TABLE "+table)
		if err != nil {
			return fmt.Errorf("snapshot %s: %w", table, err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count > 0 {
			integrationFixtureSeededTables = append(integrationFixtureSeededTables, table)
		}
	}
	return nil
}

func trackIntegrationFixtures(t *testing.T) *integrationFixtureTest {
	t.Helper()
	state := &integrationFixtureTest{}
	actual, loaded := integrationFixtureTests.LoadOrStore(t, state)
	if loaded {
		return actual.(*integrationFixtureTest)
	}
	t.Cleanup(func() {
		defer integrationFixtureTests.Delete(t)
		if !state.committed {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		tx, err := integrationDB.BeginTx(ctx, nil)
		require.NoError(t, err, "begin committed fixture cleanup")
		defer func() { _ = tx.Rollback() }()
		// Restore exact seed rows without firing application triggers or depending
		// on FK insertion order. This is transaction-local and requires the
		// disposable container's superuser; it never changes the shared pool.
		_, err = tx.ExecContext(ctx, "SET LOCAL session_replication_role = replica")
		require.NoError(t, err, "disable triggers while restoring migration baseline")
		_, err = tx.ExecContext(ctx, "TRUNCATE TABLE "+strings.Join(integrationFixtureTables, ", "))
		require.NoError(t, err, "truncate committed fixtures")
		for _, table := range integrationFixtureSeededTables {
			baseline := strings.Replace(table, `"public".`, `"integration_fixture_baseline".`, 1)
			_, err = tx.ExecContext(ctx, "INSERT INTO "+table+" SELECT * FROM "+baseline)
			require.NoError(t, err, "restore migration baseline for %s", table)
		}
		// Deliberately do not restart sequences: callers must not depend on IDs
		// being reused, and seeded primary keys must never collide with fixtures.
		require.NoError(t, tx.Commit(), "commit fixture cleanup")
	})
	return state
}

func testTx(t *testing.T) *sql.Tx {
	t.Helper()
	trackIntegrationFixtures(t)

	tx, err := integrationDB.BeginTx(context.Background(), nil)
	require.NoError(t, err, "begin tx")
	t.Cleanup(func() {
		err := tx.Rollback()
		if !errors.Is(err, sql.ErrTxDone) {
			require.NoError(t, err, "rollback test transaction")
		}
	})
	return tx
}

// testEntClient returns a committing client for code that manages its own
// transactions. Once all test transactions and explicit cleanups have finished,
// the harness removes committed fixtures and restores the migration baseline.
// Database integration tests using this helper must not call t.Parallel.
func testEntClient(t *testing.T) *dbent.Client {
	t.Helper()
	trackIntegrationFixtures(t).committed = true
	return integrationEntClient
}

// testEntTx 返回一个 ent 事务，用于需要事务隔离的测试。
// 测试结束后会自动回滚，不会影响数据库状态。
func testEntTx(t *testing.T) *dbent.Tx {
	t.Helper()
	trackIntegrationFixtures(t)

	tx, err := integrationEntClient.Tx(context.Background())
	require.NoError(t, err, "begin ent tx")
	t.Cleanup(func() {
		err := tx.Rollback()
		if !errors.Is(err, sql.ErrTxDone) {
			require.NoError(t, err, "rollback test transaction")
		}
	})
	return tx
}

// testEntSQLTx 已弃用：不要在新测试中使用此函数。
// 基于 *sql.Tx 创建的 ent client 在调用 client.Tx() 时会 panic。
// 对于需要测试内部使用事务的代码，请使用 testEntClient。
// 对于需要事务隔离的测试，请使用 testEntTx。
//
// Deprecated: Use testEntClient or testEntTx instead.
func testEntSQLTx(t *testing.T) (*dbent.Client, *sql.Tx) {
	t.Helper()

	// 直接失败，避免旧测试误用导致的事务嵌套 panic。
	t.Fatalf("testEntSQLTx 已弃用：请使用 testEntClient 或 testEntTx")
	return nil, nil
}

func TestCommittedIntegrationFixtureCleanup(t *testing.T) {
	ctx := context.Background()
	var userID, accountID int64
	t.Run("committed fixtures", func(t *testing.T) {
		// Exercise the same ordering as AccountRepoSuite's durable tests.
		testEntTx(t)
		client := testEntClient(t)
		require.Same(t, client, testEntClient(t), "repeated helper calls share one cleanup")
		user, err := client.User.Create().SetEmail("harness-cleanup@example.com").SetPasswordHash("test").Save(ctx)
		require.NoError(t, err)
		userID = user.ID
		account, err := client.Account.Create().SetName("harness-cleanup").SetPlatform("openai").SetType("oauth").Save(ctx)
		require.NoError(t, err)
		accountID = account.ID
		_, err = integrationDB.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('integration_fixture_cleanup', 'test')`)
		require.NoError(t, err)
	})
	var remaining int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM users WHERE id = $1) +
		(SELECT COUNT(*) FROM accounts WHERE id = $2) +
		(SELECT COUNT(*) FROM settings WHERE key = 'integration_fixture_cleanup')`, userID, accountID).Scan(&remaining))
	require.Zero(t, remaining, "committed fixtures must not leak past a test boundary")
	var baselineMatches bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT NOT EXISTS (
		(SELECT * FROM public.settings EXCEPT SELECT * FROM integration_fixture_baseline.settings)
		UNION ALL
		(SELECT * FROM integration_fixture_baseline.settings EXCEPT SELECT * FROM public.settings)
	)`).Scan(&baselineMatches))
	require.True(t, baselineMatches, "cleanup must preserve exact migration settings seeds")
}

func testRedis(t *testing.T) *redisclient.Client {
	t.Helper()

	prefix := fmt.Sprintf(
		"it:%s:%d:%d:",
		sanitizeRedisNamespace(t.Name()),
		time.Now().UnixNano(),
		atomic.AddUint64(&redisNamespaceSeq, 1),
	)

	opts := *integrationRedis.Options()
	rdb := redisclient.NewClient(&opts)
	rdb.AddHook(prefixHook{prefix: prefix})

	t.Cleanup(func() {
		ctx := context.Background()

		var cursor uint64
		for {
			keys, nextCursor, err := integrationRedis.Scan(ctx, cursor, prefix+"*", 500).Result()
			require.NoError(t, err, "scan redis keys for cleanup")
			if len(keys) > 0 {
				require.NoError(t, integrationRedis.Unlink(ctx, keys...).Err(), "unlink redis keys for cleanup")
			}

			cursor = nextCursor
			if cursor == 0 {
				break
			}
		}

		_ = rdb.Close()
	})

	return rdb
}

func assertTTLWithin(t *testing.T, ttl time.Duration, min, max time.Duration) {
	t.Helper()
	require.GreaterOrEqual(t, ttl, min, "ttl should be >= min")
	require.LessOrEqual(t, ttl, max, "ttl should be <= max")
}

func sanitizeRedisNamespace(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, " ", "_")
	return name
}

type prefixHook struct {
	prefix string
}

func (h prefixHook) DialHook(next redisclient.DialHook) redisclient.DialHook { return next }

func (h prefixHook) ProcessHook(next redisclient.ProcessHook) redisclient.ProcessHook {
	return func(ctx context.Context, cmd redisclient.Cmder) error {
		h.prefixCmd(cmd)
		return next(ctx, cmd)
	}
}

func (h prefixHook) ProcessPipelineHook(next redisclient.ProcessPipelineHook) redisclient.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redisclient.Cmder) error {
		for _, cmd := range cmds {
			h.prefixCmd(cmd)
		}
		return next(ctx, cmds)
	}
}

func (h prefixHook) prefixCmd(cmd redisclient.Cmder) {
	args := cmd.Args()
	if len(args) < 2 {
		return
	}

	prefixOne := func(i int) {
		if i < 0 || i >= len(args) {
			return
		}

		switch v := args[i].(type) {
		case string:
			if v != "" && !strings.HasPrefix(v, h.prefix) {
				args[i] = h.prefix + v
			}
		case []byte:
			s := string(v)
			if s != "" && !strings.HasPrefix(s, h.prefix) {
				args[i] = []byte(h.prefix + s)
			}
		}
	}

	switch strings.ToLower(cmd.Name()) {
	case "get", "set", "setnx", "setex", "psetex", "incr", "decr", "incrby", "expire", "pexpire", "ttl", "pttl",
		"hgetall", "hget", "hset", "hdel", "hincrbyfloat", "exists",
		"zadd", "zcard", "zrange", "zrangebyscore", "zrem", "zremrangebyscore", "zrevrange", "zrevrangebyscore", "zscore":
		prefixOne(1)
	case "mget":
		for i := 1; i < len(args); i++ {
			prefixOne(i)
		}
	case "del", "unlink":
		for i := 1; i < len(args); i++ {
			prefixOne(i)
		}
	case "eval", "evalsha", "eval_ro", "evalsha_ro":
		if len(args) < 3 {
			return
		}
		numKeys, err := strconv.Atoi(fmt.Sprint(args[2]))
		if err != nil || numKeys <= 0 {
			return
		}
		for i := 0; i < numKeys && 3+i < len(args); i++ {
			prefixOne(3 + i)
		}
	case "scan":
		for i := 2; i+1 < len(args); i++ {
			if strings.EqualFold(fmt.Sprint(args[i]), "match") {
				prefixOne(i + 1)
				break
			}
		}
	}
}

// IntegrationRedisSuite provides a base suite for Redis integration tests.
// Embedding suites should call SetupTest to initialize ctx and rdb.
type IntegrationRedisSuite struct {
	suite.Suite
	ctx context.Context
	rdb *redisclient.Client
}

// SetupTest initializes ctx and rdb for each test method.
func (s *IntegrationRedisSuite) SetupTest() {
	s.ctx = context.Background()
	s.rdb = testRedis(s.T())
}

// RequireNoError is a convenience method wrapping require.NoError with s.T().
func (s *IntegrationRedisSuite) RequireNoError(err error, msgAndArgs ...any) {
	s.T().Helper()
	require.NoError(s.T(), err, msgAndArgs...)
}

// AssertTTLWithin asserts that ttl is within [min, max].
func (s *IntegrationRedisSuite) AssertTTLWithin(ttl, min, max time.Duration) {
	s.T().Helper()
	assertTTLWithin(s.T(), ttl, min, max)
}

// IntegrationDBSuite provides a base suite for DB integration tests.
// Embedding suites should call SetupTest to initialize ctx and client.
type IntegrationDBSuite struct {
	suite.Suite
	ctx    context.Context
	client *dbent.Client
	tx     *dbent.Tx
}

// SetupTest initializes ctx and client for each test method.
func (s *IntegrationDBSuite) SetupTest() {
	s.ctx = context.Background()
	// 统一使用 ent.Tx，确保每个测试都有独立事务并自动回滚。
	tx := testEntTx(s.T())
	s.tx = tx
	s.client = tx.Client()
}

// RequireNoError is a convenience method wrapping require.NoError with s.T().
func (s *IntegrationDBSuite) RequireNoError(err error, msgAndArgs ...any) {
	s.T().Helper()
	require.NoError(s.T(), err, msgAndArgs...)
}
