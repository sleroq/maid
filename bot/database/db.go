package database

import (
	"database/sql"
	"errors"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"time"
)

// User is data saved in database, where id is userID+roomID
type User struct {
	Id       string
	Verified bool
	Muted    bool
	LastJoin time.Time
}

// DB is a wrapper around sql.DB
type DB struct {
	base *sql.DB
}

func New(location string) (*DB, error) {
	base, err := sql.Open("sqlite3", location)
	if err != nil {
		return nil, err
	}

	db := new(DB)
	db.base = base

	_, err = db.base.Exec(
		`create table if not exists users (
                 id text not null primary key,
                 verified integer not null,
                 muted integer not null,
                 last_join timestamp not null
             );`,
	)
	if err != nil {
		return nil, fmt.Errorf("создание таблицы пользователей: %w", err)
	}

	return db, nil
}

var NotFound = errors.New("not found")

func (db *DB) GetUser(userID string, roomID string) (User, error) {
	row := db.base.QueryRow(
		`select * from users
		 where id = :id`,
		userID+roomID,
	)
	user := User{}
	err := row.Scan(&user.Id, &user.Verified, &user.Muted, &user.LastJoin)

	if err == sql.ErrNoRows {
		return User{}, NotFound
	}

	if err != nil {
		return User{}, fmt.Errorf("scanning user: %w", err)
	}

	return user, nil
}

// VerifyUser adds or updates user in database as verified
func (db *DB) VerifyUser(userID string, roomID string) error {
	_, err := db.base.Exec(
		`insert into users (id, verified, muted, last_join)
			 values (:id, 1, 0, :lastJoin)
			 on conflict(id) do update set verified = 1`,
		userID+roomID, time.Now(),
	)
	if err != nil {
		return fmt.Errorf("verifying user: %w", err)
	}

	return nil
}

// UpdateUser updates user's data in database
func (db *DB) UpdateUser(user User) error {
	_, err := db.base.Exec(
		`update users 
             set verified = :verified, muted = :muted, last_join = :lastJoin
             where id = :id`,
		user.Verified, user.Muted, user.LastJoin,
		user.Id,
	)
	if err != nil {
		return fmt.Errorf("updating user: %w", err)
	}

	return nil
}

// Close closes database
func (db *DB) Close() error {
	return db.base.Close()
}
