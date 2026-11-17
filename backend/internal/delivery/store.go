package delivery

import (
	"agent-platform/backend/internal/planning"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/001_archive.sql
var migration string

//go:embed migrations/002_simulation.sql
var simulationMigration string

type Store struct{ pool *pgxpool.Pool }

// D4.1 is a local deployment: one explicit loopback host, database, user and
// password. No implicit pgpass/service lookup or remote plaintext connection.
func Open(ctx context.Context, dsn string) (*Store, error) {
	if os.Getenv("PGSERVICE") != "" {
		return nil, ErrConfiguration
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.User == nil || u.User.Username() == "" || u.Fragment != "" {
		return nil, ErrConfiguration
	}
	ip := net.ParseIP(u.Hostname())
	port, err := strconv.Atoi(u.Port())
	password, hasPassword := u.User.Password()
	if ip == nil || !ip.IsLoopback() || err != nil || port < 1 || port > 65535 || !hasPassword || len(password) < 16 || len(password) > 4096 || len(u.Query()) != 1 || len(u.Query()["sslmode"]) != 1 || u.Query().Get("sslmode") != "disable" || len(u.Path) < 2 || strings.ContainsAny(u.Path[1:], "/\\ \t\n") {
		return nil, ErrConfiguration
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, ErrConfiguration
	}
	config.ConnConfig.RuntimeParams = map[string]string{"application_name": "agent-platform-delivery"}
	config.ConnConfig.Host = u.Hostname()
	config.ConnConfig.Port = uint16(port)
	config.ConnConfig.User = u.User.Username()
	config.ConnConfig.Password = password
	config.ConnConfig.Database = u.Path[1:]
	config.ConnConfig.DialFunc = (&net.Dialer{Timeout: 5 * time.Second}).DialContext
	config.ConnConfig.LookupFunc = func(context.Context, string) ([]string, error) { return []string{ip.String()}, nil }
	config.ConnConfig.ValidateConnect = nil
	config.ConnConfig.MaxProtocolMessageBodyLen = 2*MaxBundleBytes + 4096
	config.ConnConfig.Fallbacks = nil
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	config.MaxConns = 4
	config.MinConns = 0
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, ErrUnavailable
	}
	s := &Store{pool: pool}
	check, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if pool.Ping(check) != nil || s.migrate(check) != nil {
		pool.Close()
		return nil, ErrUnavailable
	}
	return s, nil
}
func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}
func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(1733172601)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS agent_delivery; CREATE TABLE IF NOT EXISTS agent_delivery.migrations(version integer PRIMARY KEY, checksum text NOT NULL)`); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, "SELECT version,checksum FROM agent_delivery.migrations ORDER BY version")
	if err != nil {
		return err
	}
	count := 0
	valid := true
	checksums := []string{hashMigration(), planning.Hash(simulationMigration)}
	for rows.Next() {
		var version int
		var checksum string
		if rows.Scan(&version, &checksum) != nil || version != count+1 || version > len(checksums) || checksum != checksums[version-1] {
			valid = false
		}
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil || !valid || count > len(checksums) {
		return ErrUnavailable
	}
	for i := count; i < len(checksums); i++ {
		if _, err = tx.Exec(ctx, []string{migration, simulationMigration}[i]); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO agent_delivery.migrations(version,checksum) VALUES($1,$2)", i+1, checksums[i]); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func hashMigration() string { return Request{ChangeID: migration}.digest() }
func (s *Store) Replay(ctx context.Context, r Request) (Bundle, bool, error) {
	if !r.Valid() {
		return Bundle{}, false, ErrInvalid
	}
	var data []byte
	var digest, requestHash string
	err := s.pool.QueryRow(ctx, `SELECT a.payload,a.digest,r.request_hash FROM agent_delivery.requests r JOIN agent_delivery.archives a ON a.id=r.archive_id AND a.tenant_id=r.tenant_id WHERE r.tenant_id=$1 AND r.idempotency_key=$2`, r.TenantID, r.IdempotencyKey).Scan(&data, &digest, &requestHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return Bundle{}, false, nil
	}
	if err != nil {
		return Bundle{}, false, ErrUnavailable
	}
	b, err := decode(data, digest)
	if err == nil && requestHash != r.digest() {
		return b, true, ErrConflict
	}
	return b, true, err
}
func (s *Store) Save(ctx context.Context, r Request, b Bundle) (Bundle, bool, error) {
	if !r.Valid() || b.TenantID != r.TenantID || b.ChangeID != r.ChangeID || b.Plan.ID != r.PlanID || b.Plan.Hash != r.PlanHash {
		return Bundle{}, false, ErrInvalid
	}
	b.SchemaVersion = "delivery-archive.v1"
	b.ID = "delivery-" + rand.Text()
	b.CreatedAt = time.Now().UTC()
	b.Digest = ""
	if !b.Valid() {
		return Bundle{}, false, ErrInvalid
	}
	b.Digest = b.digest()
	data, err := json.Marshal(b)
	if err != nil || len(data) > MaxBundleBytes {
		return Bundle{}, false, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Bundle{}, false, ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Key serialization prevents competing writers from binding different content
	// to the same request. The unique source index also deduplicates new keys.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", r.TenantID+":"+r.IdempotencyKey); err != nil {
		return Bundle{}, false, ErrUnavailable
	}
	var requestHash, id string
	err = tx.QueryRow(ctx, "SELECT request_hash,archive_id FROM agent_delivery.requests WHERE tenant_id=$1 AND idempotency_key=$2", r.TenantID, r.IdempotencyKey).Scan(&requestHash, &id)
	if err == nil {
		if requestHash != r.digest() {
			return Bundle{}, false, ErrConflict
		}
		stored, err := loadRow(tx.QueryRow(ctx, "SELECT payload,digest FROM agent_delivery.archives WHERE tenant_id=$1 AND id=$2", r.TenantID, id))
		return stored, false, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Bundle{}, false, ErrUnavailable
	}
	command, err := tx.Exec(ctx, `INSERT INTO agent_delivery.archives(id,tenant_id,change_id,plan_id,plan_hash,created_at,payload,digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (tenant_id,change_id,plan_id,plan_hash) DO NOTHING`, b.ID, b.TenantID, b.ChangeID, b.Plan.ID, b.Plan.Hash, b.CreatedAt, data, b.Digest)
	if err != nil {
		return Bundle{}, false, ErrUnavailable
	}
	created := command.RowsAffected() == 1
	if !created {
		b, err = loadRow(tx.QueryRow(ctx, "SELECT payload,digest FROM agent_delivery.archives WHERE tenant_id=$1 AND change_id=$2 AND plan_id=$3 AND plan_hash=$4", r.TenantID, r.ChangeID, r.PlanID, r.PlanHash))
		if err != nil {
			return Bundle{}, false, err
		}
	}
	if _, err = tx.Exec(ctx, "INSERT INTO agent_delivery.requests(tenant_id,idempotency_key,request_hash,archive_id) VALUES($1,$2,$3,$4)", r.TenantID, r.IdempotencyKey, r.digest(), b.ID); err != nil {
		return Bundle{}, false, ErrUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return Bundle{}, false, ErrUnavailable
	}
	return b, created, nil
}
func loadRow(row pgx.Row) (Bundle, error) {
	var data []byte
	var digest string
	err := row.Scan(&data, &digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return Bundle{}, ErrNotFound
	}
	if err != nil {
		return Bundle{}, ErrUnavailable
	}
	return decode(data, digest)
}
func (s *Store) Get(ctx context.Context, tenant, id string) (Bundle, error) {
	return loadRow(s.pool.QueryRow(ctx, "SELECT payload,digest FROM agent_delivery.archives WHERE tenant_id=$1 AND id=$2", tenant, id))
}
func (s *Store) List(ctx context.Context, tenant string) ([]Bundle, error) {
	rows, err := s.pool.Query(ctx, "SELECT payload,digest FROM agent_delivery.archives WHERE tenant_id=$1 ORDER BY created_at DESC,id DESC", tenant)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	result := []Bundle{}
	for rows.Next() {
		b, err := loadRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	if rows.Err() != nil {
		return nil, ErrUnavailable
	}
	return result, nil
}
