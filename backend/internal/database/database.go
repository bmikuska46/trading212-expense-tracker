package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"budget-tracking/backend/internal/database/dbgen"
	"github.com/mattn/go-sqlite3"
)

type Store struct {
	DB            *sql.DB
	Queries       *dbgen.Queries
	CipherVersion string
	// Path is the absolute database file, and KeyFile the key that opened it.
	Path    string
	KeyFile string
}

type connector struct {
	dsn    string
	sqlite *sqlite3.SQLiteDriver
}

// safeError marks messages written by this package that never contain the DSN,
// the key, or stored values, so they can be reported instead of a generic error.
type safeError string

func (e safeError) Error() string { return string(e) }

const errWrongKey = safeError("cannot unlock database; check the key and database format")

func (c *connector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.sqlite.Open(c.dsn)
	if err != nil {
		var safe safeError
		if errors.As(err, &safe) {
			return nil, safe
		}
		// The driver's startup PRAGMAs read the first page, so a wrong key
		// usually fails there, before the connect hook runs.
		var sqliteErr sqlite3.Error
		if errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrNotADB {
			return nil, errWrongKey
		}
		return nil, safeError("cannot open database; verify SQLCipher linkage, the key, and database format")
	}
	return conn, nil
}
func (c *connector) Driver() driver.Driver { return c.sqlite }

func readKey(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("cannot read database key file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("database key must be a regular file")
	}
	if info.Mode().Perm()&0077 != 0 {
		return "", errors.New("database key file must not be accessible by group or others; use chmod 600")
	}
	if info.Size() > 66 {
		return "", errors.New("database key file must contain 64 hexadecimal characters")
	}
	content, err := io.ReadAll(io.LimitReader(file, 67))
	if err != nil {
		return "", errors.New("cannot read database key file")
	}
	key := strings.TrimSpace(string(content))
	decoded, err := hex.DecodeString(key)
	if err != nil || len(decoded) != 32 || len(key) != 64 {
		return "", errors.New("database key file must contain 64 hexadecimal characters")
	}
	return key, nil
}

func Open(ctx context.Context, path, keyFile string) (*Store, error) {
	key, err := readKey(keyFile)
	if err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("invalid database path")
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		return nil, errors.New("cannot create database directory")
	}
	// Pre-create with restrictive permissions before SQLite opens the file.
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, errors.New("cannot access database file")
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	if err := os.Chmod(absolute, 0600); err != nil {
		return nil, errors.New("cannot protect database file")
	}
	uri := url.URL{Scheme: "file", Path: absolute}
	// SQLCipher applies the URI key before the driver's startup PRAGMAs read the schema.
	// Keep this private DSN in memory. Never log it or include it in errors.
	parameters := url.Values{"mode": {"rwc"}, "_busy_timeout": {"5000"}, "_txlock": {"immediate"}, "key": {"x'" + key + "'"}}
	uri.RawQuery = parameters.Encode()
	sqlite := &sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error {
		version, err := cipherVersion(conn)
		if err != nil {
			return err
		}
		major, err := strconv.Atoi(strings.Split(version, ".")[0])
		if err != nil || major != 4 {
			return safeError("SQLCipher 4 is required")
		}
		if _, err := conn.Exec("SELECT count(*) FROM sqlite_master", nil); err != nil {
			return errWrongKey
		}
		rows, err := conn.Query("PRAGMA cipher_status", nil)
		if err != nil {
			return safeError("cannot verify active database encryption")
		}
		values := make([]driver.Value, len(rows.Columns()))
		encrypted := len(values) == 1 && rows.Next(values) == nil && values[0] == "1"
		rows.Close()
		if !encrypted {
			return safeError("SQLCipher must report active database encryption")
		}
		for _, statement := range []string{
			"PRAGMA cipher_memory_security = ON", "PRAGMA temp_store = MEMORY",
			"PRAGMA foreign_keys = ON", "PRAGMA recursive_triggers = ON",
			"PRAGMA secure_delete = ON", "PRAGMA journal_mode = WAL",
		} {
			if _, err := conn.Exec(statement, nil); err != nil {
				return safeError("cannot configure encrypted database")
			}
		}
		return nil
	}}
	db := sql.OpenDB(&connector{dsn: uri.String(), sqlite: sqlite})
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{DB: db, Queries: dbgen.New(db), Path: absolute, KeyFile: keyFile}
	if err := store.initialize(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func cipherVersion(conn *sqlite3.SQLiteConn) (string, error) {
	rows, err := conn.Query("PRAGMA cipher_version", nil)
	if err != nil {
		return "", safeError("cannot verify SQLCipher")
	}
	defer rows.Close()
	values := make([]driver.Value, len(rows.Columns()))
	if len(values) != 1 || rows.Next(values) != nil {
		return "", safeError("SQLCipher is unavailable; refusing unencrypted SQLite")
	}
	version, ok := values[0].(string)
	if !ok || version == "" {
		return "", safeError("SQLCipher is unavailable; refusing unencrypted SQLite")
	}
	return version, nil
}

func (s *Store) initialize(ctx context.Context) error {
	if err := s.DB.QueryRowContext(ctx, "PRAGMA cipher_version").Scan(&s.CipherVersion); err != nil {
		var safe safeError
		if errors.As(err, &safe) {
			return safe
		}
		return errors.New("cannot verify SQLCipher")
	}
	return s.migrate(ctx)
}

// Remove deletes a closed database file with its write-ahead log.
func Remove(path string) error {
	for _, name := range []string{path + "-wal", path + "-shm", path} {
		if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("cannot remove database file")
		}
	}
	return nil
}
