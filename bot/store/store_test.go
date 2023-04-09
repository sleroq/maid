package fastore_test

import (
	"fmt"
	"github.com/sleroq/maid/bot/store"
	"testing"
	"time"
)

// Expected to receive empty UserData
func TestFastStore_GetUserData_GetNotExisting(t *testing.T) {
	store := fastore.FastStore{}
	data := store.GetUserData("room", "user")
	fmt.Println(data)
	if data.Verified != false {
		t.Errorf("Expected to be unverified by default")
	}

	emptyTime := time.Time{}
	if data.Challenge.Expiry != emptyTime {
		t.Errorf("Expected to have no expiry by default")
	}
}

// Expected to receive empty UserData
func TestFastStore_UpdateUserData(t *testing.T) {
	store := fastore.FastStore{}
	data := store.GetUserData("room", "user")
	data.Verified = true

	store.UpdateUserData("room", "user", data)
	dataNew := store.GetUserData("room", "user")
	if dataNew.Verified != true {
		t.Errorf("Expected to be verified")
	}
}
