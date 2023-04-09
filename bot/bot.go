package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/sleroq/maid/bot/store"
	"math/rand"
	"maunium.net/go/mautrix/format"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caarlos0/env/v6"
	_ "github.com/mattn/go-sqlite3"
	"github.com/rs/zerolog"
	"github.com/sleroq/maid/bot/database"
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

var databaseFileName = "sl-maid.db"
var cryptoDatabaseFileName = "crypto.db"

const TimeToSolve = time.Minute * 1

var store = new(fastore.FastStore)

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

	db, err := database.New(databaseFileName)
	if err != nil {
		panic(fmt.Errorf("initializing database: %w", err))
	}

	log := zerolog.New(zerolog.NewConsoleWriter(func(w *zerolog.ConsoleWriter) {
		w.TimeFormat = time.Stamp
	})).With().Timestamp().Logger()
	if cfg.Debug {
		log = log.Level(zerolog.DebugLevel)
	} else {
		log = log.Level(zerolog.InfoLevel)
	}

	client, err := mautrix.NewClient(cfg.UserID.Homeserver(), "", "")
	if err != nil {
		panic(err)
	}
	client.Log = log

	syncer := client.Syncer.(*mautrix.DefaultSyncer)
	syncer.OnEventType(event.EventMessage, func(source mautrix.EventSource, evt *event.Event) {
		if strings.HasPrefix(evt.Sender.String(), "@telegram_") ||
			strings.HasPrefix(evt.Sender.String(), "@discord_") ||
			strings.HasPrefix(evt.Sender.String(), "@_xmpp_") {
			return
		}
		log.Info().
			Str("sender", evt.Sender.String()).
			Str("type", evt.Type.String()).
			Str("id", evt.ID.String()).
			Str("body", evt.Content.AsMessage().Body).
			Msg("Received message")

		if evt.Timestamp < time.Now().Add(-time.Hour).Unix() {
			log.Info().Msg("Ignoring too old event")
			return
		}

		err = handleMessage(evt, client, db, log)
		if err != nil {
			log.Error().Err(err).
				Str("room_id", evt.RoomID.String()).
				Str("userID", evt.Sender.String()).
				Msg("Failed to process message")
		}

		message := evt.Content.AsMessage().Body
		if strings.HasPrefix(message, "!maid") || strings.HasPrefix(message, "!help") || strings.HasPrefix(message, "!sl-maid") {

			userData := store.GetUserData(evt.RoomID.String(), evt.Sender.String())

			// Check if this is a new unverified user with challenge
			if !userData.Verified && userData.Challenge.TaskMessageID != "" {
				return
			}

			helpMessage := "Hello! I'm a bot that helps you to verify that you are a human. " +
				"I do not have any special commands yet"

			_, err = client.SendNotice(evt.RoomID, helpMessage)
			if err != nil {
				log.Error().Err(err).
					Str("room_id", evt.RoomID.String()).
					Str("userID", evt.Sender.String()).
					Msg("Failed to send help message")
			}
		}
	})
	syncer.OnEventType(event.StateMember, func(source mautrix.EventSource, evt *event.Event) {
		if strings.HasPrefix(evt.Sender.String(), "@telegram_") ||
			strings.HasPrefix(evt.Sender.String(), "@discord_") ||
			strings.HasPrefix(evt.Sender.String(), "@_xmpp_") {
			return
		}
		if evt.GetStateKey() == client.UserID.String() {
			// Join rooms on invite
			if evt.Content.AsMember().Membership == event.MembershipInvite {
				_, err := client.JoinRoomByID(evt.RoomID)
				if err == nil {
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
			return
		}

		if evt.Content.AsMember().Membership == event.MembershipJoin {
			err = welcomeNewUser(evt, client, db, log)
			if err != nil {
				log.Error().Err(err).
					Str("room_id", evt.RoomID.String()).
					Str("userID", evt.Sender.String()).
					Msg("Failed to welcome user")
			} else {
				log.Info().
					Str("room_id", evt.RoomID.String()).
					Str("userID", evt.Sender.String()).
					Msg("User just joined")
			}
		}
	})

	cryptoHelper, err := cryptohelper.NewCryptoHelper(client, []byte("meow"), cryptoDatabaseFileName)
	if err != nil {
		panic(err)
	}

	cryptoHelper.LoginAs = &mautrix.ReqLogin{
		Type:       mautrix.AuthTypePassword,
		Identifier: mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: cfg.UserID.Localpart()},
		Password:   cfg.Password,
	}
	// If you want to use multiple clients with the same DB, you should set a distinct cryptoDatabase account ID for each one.
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

	err = client.SyncWithContext(syncCtx)
	defer syncStopWait.Done()
	if err != nil && !errors.Is(err, context.Canceled) {
		panic(err)
	}

	cancelSync()
	syncStopWait.Wait()
	err = cryptoHelper.Close()
	if err != nil {
		log.Error().Err(err).Msg("Error closing cryptoDatabase")
	}
}

func randInRange(min, max int) int {
	return rand.Intn(max-min) + min
}

// createChallenge creates a new challenge for the user
// challenge is solving math with a random numbers between 1 and 50
func createChallenge() fastore.Challenge {
	op := randInRange(0, 2)
	a := randInRange(1, 50)
	b := randInRange(1, 50)
	var res int
	var challenge string
	switch op {
	case 0:
		challenge = fmt.Sprintf("%d + %d = ?", a, b)
		res = a + b
	case 1:
		challenge = fmt.Sprintf("%d - %d = ?", a, b)
		res = a - b
	case 2:
		challenge = fmt.Sprintf("%d * %d = ?", a, b)
		res = a * b
	}
	return fastore.Challenge{
		Challenge: challenge,
		Solution:  res,
		Expiry:    time.Now().Add(TimeToSolve),
	}
}

func welcomeNewUser(evt *event.Event, client *mautrix.Client, db *database.DB, log zerolog.Logger) error {
	user, err := db.GetUser(evt.RoomID.String(), evt.Sender.String())
	if err != nil && err != database.NotFound {
		return fmt.Errorf("failed to get user: %w", err)
	}

	if err != database.NotFound && user.Verified {
		_, err := client.SendNotice(evt.RoomID, "Welcome back, "+evt.Sender.String()+"!")
		if err != nil {
			return fmt.Errorf("failed to send welcome message: %w", err)
		}
	}

	userData := store.GetUserData(evt.RoomID.String(), evt.Sender.String())

	// If the user does not have a valid challenge, send a new one
	if userData.Challenge.TaskMessageID == "" || userData.Challenge.Expiry.Before(time.Now()) {
		challenge := createChallenge()

		welcomeMessage := fmt.Sprintf("Welcome, %s ! Please solve the following challenge to send messages:\n", evt.Sender.String()) +
			fmt.Sprintf("Добро пожаловать, %s ! Пожалуйста, решите следующее уравнение, чтобы отправлять сообщения:\n", evt.Sender.String()) +
			fmt.Sprintf("```%s```", challenge.Challenge)

		content := format.RenderMarkdown(welcomeMessage, true, false)
		challengeEvent, err := client.SendMessageEvent(evt.RoomID, event.EventMessage, &content)
		// Somehow this is possible, so we need to check for nil
		if challengeEvent == nil {
			return fmt.Errorf("failed to send challenge, because replyEvent is nil")
		}

		challenge.TaskMessageID = challengeEvent.EventID

		userData.Challenge = challenge
		store.UpdateUserData(evt.RoomID.String(), evt.Sender.String(), userData)

		// Kick the user if they don't solve the challenge in time
		go func() {
			time.Sleep(challenge.Expiry.Sub(time.Now()))
			user, err = db.GetUser(evt.RoomID.String(), evt.Sender.String())
			if err != nil && err != database.NotFound {
				log.Error().Err(err).
					Msg("Failed to get user from database")
				return
			}
			log.Info().
				Bool("user", user.Verified).
				Msg("Checking if user solved the challenge")

			if err == database.NotFound || !user.Verified {
				_, err = client.KickUser(evt.RoomID, &mautrix.ReqKickUser{UserID: evt.Sender, Reason: "Did not solve math in time"})
				if err != nil {
					log.Error().Err(err).
						Msg("Failed to kick the user")
				}
			}
		}()
	}

	return nil
}

func handleMessage(evt *event.Event, client *mautrix.Client, db *database.DB, log zerolog.Logger) error {
	message := evt.Content.AsMessage()

	// Handle unverified users without looking up database
	userData := store.GetUserData(evt.RoomID.String(), evt.Sender.String())

	// Check if this is a new unverified user with challenge
	if !userData.Verified && userData.Challenge.TaskMessageID != "" {
		if userIsRight(message, userData.Challenge.Solution) {
			userData.Verified = true
			store.UpdateUserData(evt.RoomID.String(), evt.Sender.String(), userData)

			err := db.VerifyUser(evt.Sender.String(), evt.RoomID.String())
			if err != nil {
				return fmt.Errorf("adding verified user: %w", err)
			}

			_, err = client.SendReaction(evt.RoomID, evt.ID, "👍️")
			if err != nil {
				return fmt.Errorf("reacting to user message: %w", err)
			}

			// Delete messages after 1 minute
			go func() {
				time.Sleep(time.Minute)

				err = deleteEvents(client, evt.RoomID, evt.ID, userData.Challenge.TaskMessageID)
				if err != nil {
					log.Error().Err(err)
				}
			}()

			return nil
		} else {
			// Deleting wrong answers ASAP
			_, err := client.RedactEvent(evt.RoomID, evt.ID)
			if err != nil {
				return fmt.Errorf("deleting message: %w", err)
			}

			// If user failed to solve the challenge 3 times in a row - kick them
			// but do not reset the challenge and do not delete the task message
			// so user can't spam it by rejoining

			userData.Challenge.Tries++
			store.UpdateUserData(evt.RoomID.String(), evt.Sender.String(), userData)
			if userData.Challenge.Tries > 2 {
				_, err := client.KickUser(evt.RoomID, &mautrix.ReqKickUser{
					Reason: "Stupid?",
					UserID: evt.Sender,
				})
				if err != nil {
					return fmt.Errorf("kicking user: %w", err)
				}
			}
		}
	}

	return nil
}

func deleteEvents(client *mautrix.Client, roomID id.RoomID, events ...id.EventID) (err error) {
	for _, eventID := range events {
		_, err = client.RedactEvent(roomID, eventID)
		if err != nil {
			return fmt.Errorf("redacting event: %w", err)
		}
	}

	return nil
}

func userIsRight(message *event.MessageEventContent, correctAnswer int) bool {
	answer := ""
	if len(message.FormattedBody) > 0 {
		parts := strings.Split(message.FormattedBody, "</mx-reply>")
		answer = strings.TrimSpace(parts[len(parts)-1])
	}
	if answer == "" {
		answer = strings.TrimSpace(message.Body)
	}

	userAnswer, err := strconv.ParseInt(message.Body, 10, 32)
	if err != nil {
		return false
	}

	if int(userAnswer) == correctAnswer {
		return true
	} else {
		return false
	}
}
