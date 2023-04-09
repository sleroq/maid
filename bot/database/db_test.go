package database_test

import (
	"github.com/sleroq/maid/bot/database"
	"os"
	"testing"
)

var testDBFile = "test.db"

// getTestDB returns a new DB instance with a new database
func getTestDB(t *testing.T) *database.DB {
	t.Helper()

	db, err := database.New(testDBFile)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return db
}

// cleanTestDB removes the database file
func cleanTestDB(t *testing.T) {
	t.Helper()

	if err := os.Remove("test.db"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

// TestDB_GetUser_emptyDB tests DB.GetUser function on empty database
func TestDB_GetUser_emptyDB(t *testing.T) {
	db := getTestDB(t)
	defer cleanTestDB(t)

	// Get user from empty database
	_, err := db.GetUser("user", "room")
	if err != nil {
		if err != database.NotFound {
			t.Fatalf("GetUser should return NotFound, if user is not in the database: %v", err)
		}
	}
}

// TestDB_GetUser tests DB.GetUser function
func TestDB_GetUser(t *testing.T) {
	db := getTestDB(t)
	defer cleanTestDB(t)

	// Add user to database
	err := db.VerifyUser("user", "room")
	if err != nil {
		t.Fatalf("Adding VerifiedUser: %v", err)
	}

	// Get verified user from database
	user, err := db.GetUser("user", "room")
	if err != nil {
		t.Fatalf("Getting user from database: %v", err)
	}
	if user.Verified != true {
		t.Fatalf("Expected user to be verified")
	}
}
