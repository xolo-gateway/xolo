package config

import "time"

type Storage struct {
	Database Database `envPrefix:"DATABASE_"`
}

type Database struct {
	// DSN selects the backend as well as the database: a "postgres://" URL (or
	// a libpq "host=... dbname=..." string) targets PostgreSQL, anything else
	// is a SQLite file path.
	DSN string `env:"DSN,expand" envDefault:"data.sqlite"`
	// Pool tunes the connection pool. Ignored on SQLite, which is pinned to a
	// single connection.
	Pool  DatabasePool `envPrefix:"POOL_"`
	Cache struct {
		Users     StoreCache `envPrefix:"USERS_"`
		Providers StoreCache `envPrefix:"PROVIDERS_"`
		Quota     QuotaCache `envPrefix:"QUOTA_"`
	} `envPrefix:"CACHE_"`
}

type DatabasePool struct {
	MaxOpenConns    int           `env:"MAX_OPEN_CONNS,expand" envDefault:"25"`
	MaxIdleConns    int           `env:"MAX_IDLE_CONNS,expand" envDefault:"5"`
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
