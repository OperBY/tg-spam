package storage

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite" // sqlite driver loaded here

	"github.com/umputun/tg-spam/app/storage/engine"
	"github.com/umputun/tg-spam/lib/approved"
)

// ApprovedUsers is a storage for approved users
type ApprovedUsers struct {
	*engine.SQL
	engine.RWLocker
}

// approvedUsersInfo is a struct to store approved user info in the database
type approvedUsersInfo struct {
	UserID    string    `db:"uid"`
	GroupID   string    `db:"gid"`
	ChatID    string    `db:"cid"`
	UserName  string    `db:"name"`
	Timestamp time.Time `db:"timestamp"`
}

// all approved users queries
const (
	CmdCreateApprovedUsersTable engine.DBCmd = iota + 100
	CmdCreateApprovedUsersIndexes
	CmdAddApprovedUser
	CmdAddUIDColumn
	CmdAddGIDColumn
	CmdAddCIDColumn
)

// queries holds all approved users queries
var approvedUsersQueries = engine.NewQueryMap().
	Add(CmdCreateApprovedUsersTable, engine.Query{
		Sqlite: `CREATE TABLE IF NOT EXISTS approved_users (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            uid TEXT,
            gid TEXT DEFAULT '',
            cid TEXT DEFAULT '',
            name TEXT,
            timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
            UNIQUE(gid, cid, uid)
        )`,
		Postgres: `CREATE TABLE IF NOT EXISTS approved_users (
            id SERIAL PRIMARY KEY,
            uid TEXT,
            gid TEXT DEFAULT '',
            cid TEXT DEFAULT '',
            name TEXT,
            timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
            UNIQUE(gid, cid, uid)
        )`,
	}).
	AddSame(CmdCreateApprovedUsersIndexes, `
        CREATE INDEX IF NOT EXISTS idx_approved_users_uid ON approved_users(uid);
        CREATE INDEX IF NOT EXISTS idx_approved_users_gid ON approved_users(gid);
        CREATE INDEX IF NOT EXISTS idx_approved_users_cid ON approved_users(cid);
        CREATE INDEX IF NOT EXISTS idx_approved_users_name ON approved_users(name);
        CREATE INDEX IF NOT EXISTS idx_approved_users_timestamp ON approved_users(timestamp)
    `).
	Add(CmdAddApprovedUser, engine.Query{
		Sqlite:   "INSERT OR REPLACE INTO approved_users (uid, gid, cid, name, timestamp) VALUES (?, ?, ?, ?, ?)",
		Postgres: "INSERT INTO approved_users (uid, gid, cid, name, timestamp) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (gid, cid, uid) DO UPDATE SET name=EXCLUDED.name, timestamp=EXCLUDED.timestamp",
	}).
	Add(CmdAddUIDColumn, engine.Query{
		Sqlite:   "ALTER TABLE approved_users ADD COLUMN uid TEXT",
		Postgres: "ALTER TABLE approved_users ADD COLUMN IF NOT EXISTS uid TEXT",
	}).
	Add(CmdAddGIDColumn, engine.Query{
		Sqlite:   "ALTER TABLE approved_users ADD COLUMN gid TEXT DEFAULT ''",
		Postgres: "ALTER TABLE approved_users ADD COLUMN IF NOT EXISTS gid TEXT DEFAULT ''",
	}).
	Add(CmdAddCIDColumn, engine.Query{
		Sqlite:   "ALTER TABLE approved_users ADD COLUMN cid TEXT DEFAULT ''",
		Postgres: "ALTER TABLE approved_users ADD COLUMN IF NOT EXISTS cid TEXT DEFAULT ''",
	})

// NewApprovedUsers creates a new ApprovedUsers storage
func NewApprovedUsers(ctx context.Context, db *engine.SQL) (*ApprovedUsers, error) {
	if db == nil {
		return nil, fmt.Errorf("db connection is nil")
	}
	res := &ApprovedUsers{SQL: db, RWLocker: db.MakeLock()}
	cfg := engine.TableConfig{
		Name:          "approved_users",
		CreateTable:   CmdCreateApprovedUsersTable,
		CreateIndexes: CmdCreateApprovedUsersIndexes,
		MigrateFunc:   res.migrate,
		QueriesMap:    approvedUsersQueries,
	}
	if err := engine.InitTable(ctx, db, cfg); err != nil {
		return nil, fmt.Errorf("failed to init approved users storage: %w", err)
	}
	return res, nil
}

// Read returns a list of all approved users
func (au *ApprovedUsers) Read(ctx context.Context) ([]approved.UserInfo, error) {
	au.RLock()
	defer au.RUnlock()

	query := au.Adopt("SELECT uid, gid, cid, name, timestamp FROM approved_users WHERE gid = ? ORDER BY uid ASC")
	users := []approvedUsersInfo{}
	if err := au.SelectContext(ctx, &users, query, au.GID()); err != nil {
		return nil, fmt.Errorf("failed to get approved users: %w", err)
	}

	res := make([]approved.UserInfo, len(users))
	for i, u := range users {
		res[i] = approved.UserInfo{
			UserID:    u.UserID,
			ChatID:    u.ChatID,
			UserName:  u.UserName,
			Timestamp: u.Timestamp,
		}
	}
	log.Printf("[DEBUG] read %d approved users", len(res))
	return res, nil
}

// Write adds a user to the approved list
func (au *ApprovedUsers) Write(ctx context.Context, user approved.UserInfo) error {
	if user.UserID == "" {
		return fmt.Errorf("user id can't be empty")
	}
	if user.ChatID == "" {
		// fallback for legacy callers: use gid as chat scope if chatID not provided
		user.ChatID = au.GID()
	}

	au.Lock()
	defer au.Unlock()

	if user.Timestamp.IsZero() {
		user.Timestamp = time.Now()
	}

	query, err := approvedUsersQueries.Pick(au.Type(), CmdAddApprovedUser)
	if err != nil {
		return fmt.Errorf("failed to get write query: %w", err)
	}

	if _, err := au.ExecContext(ctx, query, user.UserID, au.GID(), user.ChatID, user.UserName, user.Timestamp); err != nil {
		return fmt.Errorf("failed to insert user %+v: %w", user, err)
	}

	log.Printf("[INFO] user %q (%s) added to approved users", user.UserName, user.UserID)
	return nil
}

// Delete removes a user from the approved list
func (au *ApprovedUsers) Delete(ctx context.Context, id string, chatID string) error {
	if id == "" {
		return fmt.Errorf("user id can't be empty")
	}
	if chatID == "" {
		chatID = au.GID()
	}

	au.Lock()
	defer au.Unlock()

	// check if user exists first
	var user approvedUsersInfo
	query := au.Adopt("SELECT uid, gid, cid, name, timestamp FROM approved_users WHERE uid = ? AND gid = ? AND cid = ?")
	if err := au.GetContext(ctx, &user, query, id, au.GID(), chatID); err != nil {
		return fmt.Errorf("failed to get approved user for id %s: %w", id, err)
	}

	// delete user  "DELETE FROM approved_users WHERE uid = ? AND gid = ?"
	query = au.Adopt("DELETE FROM approved_users WHERE uid = ? AND gid = ? AND cid = ?")
	if _, err := au.ExecContext(ctx, query, id, au.GID(), chatID); err != nil {
		return fmt.Errorf("failed to delete id %s: %w", id, err)
	}

	log.Printf("[INFO] user %q (%s) deleted from approved users", user.UserName, id)
	return nil
}

// migrateTableTx handles migration within a transaction
func (au *ApprovedUsers) migrate(ctx context.Context, tx *sqlx.Tx, gid string) error {
	// try to select with new structure, if works - already migrated
	var count int
	err := tx.GetContext(ctx, &count, "SELECT COUNT(*) FROM approved_users WHERE uid='' AND gid=''")
	if err == nil {
		log.Printf("[DEBUG] approved_users table already migrated")
		return nil
	}

	// add columns using db-specific queries
	addUIDQuery, err := approvedUsersQueries.Pick(au.Type(), CmdAddUIDColumn)
	if err != nil {
		return fmt.Errorf("failed to get add UID query: %w", err)
	}

	_, err = tx.ExecContext(ctx, addUIDQuery)
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("failed to add uid column: %w", err)
	}

	addGIDQuery, err := approvedUsersQueries.Pick(au.Type(), CmdAddGIDColumn)
	if err != nil {
		return fmt.Errorf("failed to get add GID query: %w", err)
	}

	_, err = tx.ExecContext(ctx, addGIDQuery)
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("failed to add gid column: %w", err)
	}

	addCIDQuery, err := approvedUsersQueries.Pick(au.Type(), CmdAddCIDColumn)
	if err != nil {
		return fmt.Errorf("failed to get add CID query: %w", err)
	}

	_, err = tx.ExecContext(ctx, addCIDQuery)
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("failed to add cid column: %w", err)
	}

	migrateQuery := au.Adopt("UPDATE approved_users SET uid = id, gid = ?, cid = ? WHERE uid IS NULL OR uid = '' OR cid IS NULL OR cid = ''")
	if _, err = tx.ExecContext(ctx, migrateQuery, gid, gid); err != nil {
		return fmt.Errorf("failed to migrate data: %w", err)
	}

	log.Printf("[DEBUG] approved_users table migrated")
	return nil
}
