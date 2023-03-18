package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/caarlos0/env/v6"
	"github.com/chzyer/readline"
	_ "github.com/mattn/go-sqlite3"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type config struct {
	UserID   id.UserID `env:"MATRIX_IDENTIFIER,notEmpty"`
	Password string    `env:"MATRIX_PASSWORD,notEmpty"`
	Debug    bool      `env:"DEBUG"`
}

var database = "sl-maid.db"

func main() {
	cfg := config{}
	if err := env.Parse(&cfg); err != nil {
		fmt.Printf("Error parsing config: %s", err)
		os.Exit(1)
	}

	if cfg.UserID == "" || cfg.Password == "" {
		fmt.Println("Required env not set")
		os.Exit(1)
	}

	client, err := mautrix.NewClient(cfg.UserID.Homeserver(), "", "")
	if err != nil {
		panic(err)
	}
	rl, err := readline.New("[no room]> ")
	if err != nil {
		panic(err)
	}
	defer rl.Close()
	log := zerolog.New(zerolog.NewConsoleWriter(func(w *zerolog.ConsoleWriter) {
		w.Out = rl.Stdout()
		w.TimeFormat = time.Stamp
	})).With().Timestamp().Logger()
	if !cfg.Debug {
		log = log.Level(zerolog.InfoLevel)
	}
	client.Log = log

	var lastRoomID id.RoomID

	syncer := client.Syncer.(*mautrix.DefaultSyncer)
	syncer.OnEventType(event.EventMessage, func(source mautrix.EventSource, evt *event.Event) {
		lastRoomID = evt.RoomID
		rl.SetPrompt(fmt.Sprintf("%s> ", lastRoomID))
		log.Info().
			Str("sender", evt.Sender.String()).
			Str("type", evt.Type.String()).
			Str("id", evt.ID.String()).
			Str("body", evt.Content.AsMessage().Body).
			Msg("Received message")
	})
	syncer.OnEventType(event.StateMember, func(source mautrix.EventSource, evt *event.Event) {
		if evt.GetStateKey() == client.UserID.String() && evt.Content.AsMember().Membership == event.MembershipInvite {
			_, err := client.JoinRoomByID(evt.RoomID)
			if err == nil {
				lastRoomID = evt.RoomID
				rl.SetPrompt(fmt.Sprintf("%s> ", lastRoomID))
				log.Info().
					Str("room_id", evt.RoomID.String()).
					Str("inviter", evt.Sender.String()).
					Msg("Joined room after invite")
			} else {
				log.Error().Err(err).
					Str("room_id", evt.RoomID.String()).
					Str("inviter", evt.Sender.String()).
					Msg("Failed to join room after invite")
			}
		}
	})

	cryptoHelper, err := cryptohelper.NewCryptoHelper(client, []byte("meow"), database)
	if err != nil {
		panic(err)
	}

	// You can also store the user/device IDs and access token and put them in the client beforehand instead of using LoginAs.
	//client.UserID = "..."
	//client.DeviceID = "..."
	//client.AccessToken = "..."
	// You don't need to set a device ID in LoginAs because the crypto helper will set it for you if necessary.
	cryptoHelper.LoginAs = &mautrix.ReqLogin{
		Type:       mautrix.AuthTypePassword,
		Identifier: mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: cfg.UserID.Localpart()},
		Password:   cfg.Password,
	}
	// If you want to use multiple clients with the same DB, you should set a distinct database account ID for each one.
	//cryptoHelper.DBAccountID = ""
	err = cryptoHelper.Init()
	if err != nil {
		panic(err)
	}
	// Set the client crypto helper in order to automatically encrypt outgoing messages
	client.Crypto = cryptoHelper

	log.Info().Msg("Now running")
	syncCtx, cancelSync := context.WithCancel(context.Background())
	var syncStopWait sync.WaitGroup
	syncStopWait.Add(1)

	//go func() {
	err = client.SyncWithContext(syncCtx)
	defer syncStopWait.Done()
	if err != nil && !errors.Is(err, context.Canceled) {
		panic(err)
	}
	//}()

	cancelSync()
	syncStopWait.Wait()
	err = cryptoHelper.Close()
	if err != nil {
		log.Error().Err(err).Msg("Error closing database")
	}
}
