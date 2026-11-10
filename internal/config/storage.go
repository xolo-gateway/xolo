package config

import "time"

type Storage struct {
	// AutoMigrate applies pending schema changes before starting the server.
	AutoMigrate bool     `env:"AUTO_MIGRATE" envDefault:"true"`
	Database    Database `envPrefix:"DATABASE_"`
}

type Database struct {
	// DSN selects the backend as well as the database: a "postgres://" URL (or
	// a libpq "host=... dbname=..." string) targets PostgreSQL, anything else
	// is a SQLite file path.
	DSN string `env:"DSN,expand" envDefault:"data.sqlite"`
	// Pool tunes the connection pool. On SQLite, where writes go through a
	// single connection, it sizes the pool of read-only connections serving
	// the plain reads.
	Pool  DatabasePool `envPrefix:"POOL_"`
	Cache struct {
		Users     StoreCache `envPrefix:"USERS_"`
		Providers StoreCache `envPrefix:"PROVIDERS_"`
		Quota     QuotaCache `envPrefix:"QUOTA_"`
		// Coherence keeps the user, token, provider and model caches of the
		// instances sharing the database consistent.
		Coherence CacheCoherence `envPrefix:"COHERENCE_"`
	} `envPrefix:"CACHE_"`
}

type DatabasePool struct {
	MaxOpenConns int `env:"MAX_OPEN_CONNS,expand" envDefault:"25"`
	// MaxIdleConns is how many connections the pool keeps open while idle.
	// Unset, as many as MaxOpenConns: with fewer, the pool closes the
	// connections a burst returns and opens them again on the next one, a TCP
	// and authentication handshake each (see IdleConns).
	MaxIdleConns    *int          `env:"MAX_IDLE_CONNS,expand"`
	ConnMaxLifetime time.Duration `env:"CONN_MAX_LIFETIME,expand" envDefault:"60m"`
}

type StoreCache struct {
	Enabled bool          `env:"ENABLED,expand" envDefault:"true"`
	Size    int           `env:"SIZE,expand" envDefault:"25"`
	TTL     time.Duration `env:"TTL,expand" envDefault:"60m"`
}

// QuotaCache tunes the cache holding the budget totals read on the proxy hot
// path. Its defaults differ from StoreCache: the entries are small and
// numerous — one per budget scope and window — and they must stay close to the
// counters they mirror, so the cache is wide and short-lived rather than narrow
// and long-lived.
type QuotaCache struct {
	Enabled bool `env:"ENABLED,expand" envDefault:"true"`
	// Size is the maximum number of cached totals. Each active organization
	// holds three entries, plus three per active user.
	Size int `env:"SIZE,expand" envDefault:"4096"`
	// TTL bounds how long a total may be reused without being read again.
	// Increments are applied to the cached entries as usage is recorded, so a
	// single replica stays exact within the TTL; with several replicas the TTL
	// is how long one may miss another's spending. "0" disables reuse and reads
	// the counters on every check.
	TTL time.Duration `env:"TTL,expand" envDefault:"10s"`
}

// CacheCoherence sets how often an instance checks whether another one changed
// a cached entry. That is how long a revoked token, a deactivated user or a
// changed provider key may still be served by the other instances. Zero turns
// it off: entries then live until their TTL on every other instance, which is
// only safe with a single instance.
type CacheCoherence struct {
	Interval time.Duration `env:"INTERVAL,expand" envDefault:"1s"`
}

// IdleConns is the number of idle connections the pool keeps: MaxIdleConns
// when set, MaxOpenConns otherwise.
//
// The former default, 5, made the pool churn under load: measured on
// PostgreSQL at 300 req/s, about 40 new sessions per second, a quarter of the
// blocked time spent waiting for a connection, and a 2 vCPU instance capped at
// 420 req/s instead of 500.
//
// MaxOpenConns at 0 or below leaves the pool unbounded, with nothing to follow:
// the idle connections then default to unboundedPoolIdleConns.
func (p DatabasePool) IdleConns() int {
	if p.MaxIdleConns != nil {
		return *p.MaxIdleConns
	}
	if p.MaxOpenConns <= 0 {
		return unboundedPoolIdleConns
	}
	return p.MaxOpenConns
}

// unboundedPoolIdleConns is the default of MaxOpenConns.
const unboundedPoolIdleConns = 25
