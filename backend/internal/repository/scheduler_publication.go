package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/redis/go-redis/v9"
)

// No cloud read consults legacy keys and all mutation entry points are denied.
// No local memo can outlive PG authority.
type publicationSchedulerCache struct {
	db    *sql.DB
	redis *redis.Client
}

type publicationManifest struct {
	Generation int64
	ID         string
	Payloads   map[string]string
	Buckets    map[string][]int64
}

func NewPublicationSchedulerCache(db *sql.DB, rdb *redis.Client) service.SchedulerCache {
	return &publicationSchedulerCache{db: db, redis: rdb}
}
func (*publicationSchedulerCache) ReadOnlyPublicationCache() {}
func publicationPrefix(p service.SchedulerPublication) string {
	return fmt.Sprintf("sched:publication:%d:%s:", p.Generation, p.ID)
}
func publicationDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (c *publicationSchedulerCache) manifest(ctx context.Context) (service.SchedulerPublication, publicationManifest, error) {
	var p service.SchedulerPublication
	var m publicationManifest
	// Deliberately query PG on EVERY read; no old pointer on PG error.
	err := c.db.QueryRowContext(ctx, "SELECT generation, publication_id, manifest_digest FROM scheduler_publication WHERE singleton_id = 1").Scan(&p.Generation, &p.ID, &p.Digest)
	if err != nil {
		return p, m, err
	}
	raw, err := c.redis.Get(ctx, publicationPrefix(p)+"manifest").Bytes()
	if err != nil {
		return p, m, err
	}
	if publicationDigest(raw) != p.Digest {
		return p, m, errors.New("publication manifest digest mismatch")
	}
	if err = json.Unmarshal(raw, &m); err != nil {
		return p, m, err
	}
	if m.Generation != p.Generation || m.ID != p.ID || m.Payloads == nil || m.Buckets == nil {
		return p, m, errors.New("publication manifest identity mismatch")
	}
	return p, m, nil
}
func (c *publicationSchedulerCache) payload(ctx context.Context, p service.SchedulerPublication, m publicationManifest, key string, id int64) (*service.Account, error) {
	digest, ok := m.Payloads[key]
	if !ok {
		return nil, errors.New("publication payload absent from manifest")
	}
	raw, err := c.redis.Get(ctx, publicationPrefix(p)+key).Bytes()
	if err != nil {
		return nil, err
	}
	if publicationDigest(raw) != digest {
		return nil, errors.New("publication payload digest mismatch")
	}
	var account service.Account
	if err = json.Unmarshal(raw, &account); err != nil {
		return nil, err
	}
	if account.ID != id {
		return nil, errors.New("publication account identity mismatch")
	}
	return &account, nil
}
func (c *publicationSchedulerCache) GetSnapshot(ctx context.Context, bucket service.SchedulerBucket) ([]*service.Account, bool, error) {
	p, m, err := c.manifest(ctx)
	if err != nil {
		return nil, false, err
	}
	ids, ok := m.Buckets[bucket.String()]
	if !ok {
		return nil, false, nil
	}
	accounts := make([]*service.Account, 0, len(ids))
	for _, id := range ids {
		account, err := c.payload(ctx, p, m, "slim:"+strconv.FormatInt(id, 10), id)
		if err != nil {
			return nil, false, err
		}
		accounts = append(accounts, account)
	}
	return accounts, true, nil
}
func (c *publicationSchedulerCache) GetAccount(ctx context.Context, id int64) (*service.Account, error) {
	p, m, err := c.manifest(ctx)
	if err != nil {
		return nil, err
	}
	key := "full:" + strconv.FormatInt(id, 10)
	if _, ok := m.Payloads[key]; !ok {
		return nil, nil
	}
	return c.payload(ctx, p, m, key, id)
}

var errPublicationReadOnly = errors.New("immutable scheduler publication is read-only")

func (*publicationSchedulerCache) SetAccount(context.Context, *service.Account) error {
	return errPublicationReadOnly
}
func (*publicationSchedulerCache) DeleteAccount(context.Context, int64) error {
	return errPublicationReadOnly
}
func (*publicationSchedulerCache) UpdateLastUsed(context.Context, map[int64]time.Time) error {
	return errPublicationReadOnly
}
func (*publicationSchedulerCache) CaptureBucketWriteToken(context.Context, service.SchedulerBucket) (service.SchedulerBucketWriteToken, error) {
	return service.SchedulerBucketWriteToken{}, errPublicationReadOnly
}
func (*publicationSchedulerCache) SetSnapshot(context.Context, service.SchedulerBucket, service.SchedulerBucketWriteToken, []service.Account) error {
	return errPublicationReadOnly
}
func (*publicationSchedulerCache) RetireBucket(context.Context, service.SchedulerBucket, service.SchedulerGroupLifecycleLease) error {
	return errPublicationReadOnly
}
func (*publicationSchedulerCache) ReopenBucket(context.Context, service.SchedulerBucket, service.SchedulerGroupLifecycleLease) (service.SchedulerBucketWriteToken, error) {
	return service.SchedulerBucketWriteToken{}, errPublicationReadOnly
}
func (*publicationSchedulerCache) TryAcquireGroupLifecycleLease(context.Context, int64, time.Duration) (service.SchedulerGroupLifecycleLease, bool, error) {
	return service.SchedulerGroupLifecycleLease{}, false, errPublicationReadOnly
}
func (*publicationSchedulerCache) ReleaseGroupLifecycleLease(context.Context, service.SchedulerGroupLifecycleLease) error {
	return errPublicationReadOnly
}
func (*publicationSchedulerCache) TryLockBucket(context.Context, service.SchedulerBucket, time.Duration) (service.SchedulerBucketLease, bool, error) {
	return service.SchedulerBucketLease{}, false, errPublicationReadOnly
}
func (*publicationSchedulerCache) UnlockBucket(context.Context, service.SchedulerBucketLease) error {
	return errPublicationReadOnly
}
func (*publicationSchedulerCache) ListBuckets(context.Context) ([]service.SchedulerBucket, error) {
	return nil, errPublicationReadOnly
}
func (*publicationSchedulerCache) GetOutboxWatermark(context.Context) (int64, error) {
	return 0, errPublicationReadOnly
}
func (*publicationSchedulerCache) SetOutboxWatermark(context.Context, int64) error {
	return errPublicationReadOnly
}

func (c *publicationSchedulerCache) BindPublisher(session service.MaintenanceSession) (service.SchedulerPublicationPublisher, error) {
	repo, err := BindFencedSchedulerOutbox(c.db, session)
	if err != nil {
		return nil, err
	}
	bound, ok := repo.(*fencedSchedulerOutboxRepository)
	if !ok {
		return nil, errors.New("unexpected fenced repository type")
	}
	return &schedulerPublicationPublisher{fencedSchedulerOutboxRepository: bound, cache: c}, nil
}

type schedulerPublicationPublisher struct {
	*fencedSchedulerOutboxRepository
	cache  *publicationSchedulerCache
	mu     sync.Mutex
	staged map[string]service.SchedulerPublication
}

// Publication callers cannot acknowledge independently of pointer activation.
func (*schedulerPublicationPublisher) AcknowledgeProcessed(context.Context, []int64) error {
	return errors.New("publication acknowledgement requires atomic activation")
}

func (r *schedulerPublicationPublisher) Stage(ctx context.Context, data service.SchedulerPublicationData) (service.SchedulerPublication, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return service.SchedulerPublication{}, err
	}
	p := service.SchedulerPublication{Generation: r.session.fence.Generation, ID: hex.EncodeToString(token[:])}
	// Register before any Redis write; ambiguous registration abandons this ID.
	if err := r.session.withGeneration(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO scheduler_publication_lifecycle(publication_id,generation,state) VALUES($1,$2,'staging')", p.ID, p.Generation)
		return err
	}); err != nil {
		return service.SchedulerPublication{}, err
	}

	m := publicationManifest{Generation: p.Generation, ID: p.ID, Payloads: make(map[string]string), Buckets: data.Buckets}
	if m.Buckets == nil {
		m.Buckets = make(map[string][]int64)
	}
	// SET NX prevents accidental namespace reuse. Failed/ambiguous staging is
	// abandoned, never repaired or activated. Manifest is written last.
	// Active data has no TTL: a healthy publication must survive a prolonged
	// worker outage. Collect retires obsolete/staging identities in PG before
	// deleting their Redis payloads; no active TTL is ever installed.
	put := func(key string, value any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		ok, err := writePublicationKey.Run(ctx, r.cache.redis, []string{publicationPrefix(p) + "retired", publicationPrefix(p) + key}, raw).Bool()
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("publication ID collision")
		}
		m.Payloads[key] = publicationDigest(raw)
		return nil
	}
	known := make(map[int64]bool)
	for _, account := range data.Accounts {
		if account.ID <= 0 || known[account.ID] {
			return service.SchedulerPublication{}, errors.New("invalid duplicate publication account")
		}
		known[account.ID] = true
		id := strconv.FormatInt(account.ID, 10)
		if err := put("full:"+id, account); err != nil {
			return service.SchedulerPublication{}, err
		}
		if err := put("slim:"+id, buildSchedulerMetadataAccount(account)); err != nil {
			return service.SchedulerPublication{}, err
		}
	}
	for _, ids := range m.Buckets {
		for _, id := range ids {
			if !known[id] {
				return service.SchedulerPublication{}, errors.New("unknown publication member")
			}
		}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return service.SchedulerPublication{}, err
	}
	ok, err := writePublicationKey.Run(ctx, r.cache.redis, []string{publicationPrefix(p) + "retired", publicationPrefix(p) + "manifest"}, raw).Bool()
	if err != nil {
		return service.SchedulerPublication{}, err
	}
	if !ok {
		return service.SchedulerPublication{}, errors.New("publication ID collision")
	}
	p.Digest = publicationDigest(raw)
	r.mu.Lock()
	if r.staged == nil {
		r.staged = make(map[string]service.SchedulerPublication)
	}
	r.staged[p.ID] = p
	r.mu.Unlock()
	return p, nil
}
func (r *schedulerPublicationPublisher) Activate(ctx context.Context, p service.SchedulerPublication, ids []int64) error {
	if p.Generation != r.session.fence.Generation || len(p.ID) != 64 || len(p.Digest) != 64 {
		return errors.New("invalid publication capability")
	}
	// Consume before SQL, even when COMMIT is ambiguous. Never reactivate an ID.
	r.mu.Lock()
	staged, ok := r.staged[p.ID]
	delete(r.staged, p.ID)
	r.mu.Unlock()
	if !ok || staged != p {
		return errors.New("publication not staged or already attempted")
	}
	// SQL only: PG pointer and exact acknowledgement commit or roll back together.
	return r.session.withGeneration(ctx, func(tx *sql.Tx) error {
		// A retired/obsolete/previously activated capability is never eligible again.
		result, err := tx.ExecContext(ctx, "UPDATE scheduler_publication_lifecycle SET state='active',changed_at=clock_timestamp() WHERE publication_id=$1 AND generation=$2 AND state='staging'", p.ID, p.Generation)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("publication permanently retired or not staged")
		}
		_, err = tx.ExecContext(ctx, "UPDATE scheduler_publication_lifecycle SET state='obsolete',changed_at=clock_timestamp() WHERE state='active' AND publication_id<>$1", p.ID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO scheduler_publication(singleton_id,generation,publication_id,manifest_digest) VALUES(1,$1,$2,$3)
 ON CONFLICT(singleton_id) DO UPDATE SET generation=EXCLUDED.generation,publication_id=EXCLUDED.publication_id,manifest_digest=EXCLUDED.manifest_digest`, p.Generation, p.ID, p.Digest)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM scheduler_outbox WHERE id = ANY($1)", pq.Array(ids))
		return err
	})
}

// Retirement markers fence delayed Redis staging writes after GC. They are
// outside the immutable payload, never authorize reads, and are kept as tiny
// tombstones. PG retirement is authoritative even if Redis rolls back a marker.
var writePublicationKey = redis.NewScript(`
if redis.call('EXISTS',KEYS[1]) == 1 then return 0 end
if redis.call('SETNX',KEYS[2],ARGV[1]) == 1 then return 1 end
return 0
`)

// Healthy validates ALL required payloads, not just manifest existence. A
// successor always publishes its own generation, even if its predecessor is intact.
func (r *schedulerPublicationPublisher) Healthy(ctx context.Context) (bool, error) {
	var generation int64
	err := r.cache.db.QueryRowContext(ctx, "SELECT generation FROM scheduler_publication WHERE singleton_id=1").Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if generation != r.session.fence.Generation {
		return false, nil
	}
	p, m, err := r.cache.manifest(ctx)
	if err != nil {
		// Redis misses/corruption trigger rebuild. Recheck PG to distinguish PG
		// unavailability, never using a cached old authority pointer.
		if err := r.cache.db.QueryRowContext(ctx, "SELECT generation FROM scheduler_publication WHERE singleton_id=1").Scan(&generation); err != nil {
			return false, err
		}
		return false, nil
	}
	if p.Generation != r.session.fence.Generation {
		return false, nil
	}
	for key, digest := range m.Payloads {
		raw, err := r.cache.redis.Get(ctx, publicationPrefix(p)+key).Bytes()
		if err != nil || publicationDigest(raw) != digest {
			return false, nil
		}
	}
	for _, ids := range m.Buckets {
		for _, id := range ids {
			if _, ok := m.Payloads["full:"+strconv.FormatInt(id, 10)]; !ok {
				return false, nil
			}
			if _, ok := m.Payloads["slim:"+strconv.FormatInt(id, 10)]; !ok {
				return false, nil
			}
		}
	}
	return true, nil
}

// Collect first permanently retires IDs in a short fenced PG transaction.
// Redis work happens after commit, never in that transaction. A two-minute
// obsolete grace accommodates ordinary readers; any slower reader may miss and
// must use DB fallback. Current publication is excluded by PG identity, including
// after an ambiguous activation commit. Staging has a five-minute abandonment
// deadline (worker cycle is two minutes); successor generations abandon sooner.
func (r *schedulerPublicationPublisher) Collect(ctx context.Context) error {
	var retired []service.SchedulerPublication
	err := r.session.withGeneration(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE scheduler_publication_lifecycle l SET state='retired',changed_at=clock_timestamp()
 WHERE NOT EXISTS(SELECT 1 FROM scheduler_publication p WHERE p.publication_id=l.publication_id)
 AND ((l.state='obsolete' AND l.changed_at<clock_timestamp()-interval '2 minutes')
 OR (l.state='staging' AND (l.generation<$1 OR l.changed_at<clock_timestamp()-interval '5 minutes')))`, r.session.fence.Generation)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT generation,publication_id FROM scheduler_publication_lifecycle l
 WHERE state='retired' AND NOT EXISTS(SELECT 1 FROM scheduler_publication p WHERE p.publication_id=l.publication_id)
 ORDER BY changed_at LIMIT 200`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var p service.SchedulerPublication
			if err := rows.Scan(&p.Generation, &p.ID); err != nil {
				return err
			}
			retired = append(retired, p)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	} // Never delete after an ambiguous retirement commit.
	for _, p := range retired {
		prefix := publicationPrefix(p)
		// Marker first; atomic staging script prevents post-cleanup orphan writes.
		if err := r.cache.redis.Set(ctx, prefix+"retired", "1", 0).Err(); err != nil {
			return err
		}
		var cursor uint64
		for {
			keys, next, err := r.cache.redis.Scan(ctx, cursor, prefix+"*", 256).Result()
			if err != nil {
				return err
			}
			remove := make([]string, 0, len(keys))
			for _, key := range keys {
				if !strings.HasSuffix(key, prefix+"retired") {
					remove = append(remove, key)
				}
			}
			if len(remove) > 0 {
				if err := r.cache.redis.Unlink(ctx, remove...).Err(); err != nil {
					return err
				}
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
		// Rotate tombstones so Redis rollback resurrection is swept again, without
		// starving later IDs. Never erase PG retirement authorization.
		if err := r.session.withGeneration(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "UPDATE scheduler_publication_lifecycle SET changed_at=clock_timestamp() WHERE publication_id=$1 AND state='retired'", p.ID)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}
