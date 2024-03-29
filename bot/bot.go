package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/sleroq/maid/bot/store"
	"math/rand"
	"maunium.net/go/mautrix/format"
	"regexp"
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
	UserID       id.UserID `env:"MATRIX_IDENTIFIER,notEmpty"`
	Password     string    `env:"MATRIX_PASSWORD,notEmpty"`
	Debug        bool      `env:"DEBUG"`
	TimeToSolve  int       `env:"TIME_TO_SOLVE" envDefault:"10"`
	IgnoredUsers []string  `env:"IGNORED_USERS" envSeparator:"\n"`
}

var databaseFileName = "sl-maid.db"
var cryptoDatabaseFileName = "crypto.db"

var store = new(fastore.FastStore)

func main() {
	cfg, err := handleConfig()
	if err != nil {
		panic(fmt.Errorf("handling config: %w", err))
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
		shouldIgnore, err := checkIfShouldIgnore(evt, log, client, cfg.IgnoredUsers)
		if err != nil {
			log.Error().Err(err).
				Str("room_id", evt.RoomID.String()).
				Str("userID", evt.Sender.String()).
				Msg("Failed to check if event should be ignored")
		}

		if shouldIgnore {
			return
		}

		log.Info().
			Str("sender", evt.Sender.String()).
			Str("type", evt.Type.String()).
			Str("id", evt.ID.String()).
			Str("body", evt.Content.AsMessage().Body).
			Msg("Received message")

		err = handleMessage(evt, client, db, log)
		if err != nil {
			log.Error().Err(err).
				Str("room_id", evt.RoomID.String()).
				Str("userID", evt.Sender.String()).
				Msg("Failed to process message")
		}

		message := evt.Content.AsMessage().Body
		if strings.HasPrefix(message, "!maid") || strings.HasPrefix(message, "!help") || strings.HasPrefix(message, "!sl-maid") {
			user, err := db.GetUser(evt.Sender.String(), evt.RoomID.String())
			if err != nil {
				log.Warn().Err(err).Msg("Failed to get user")
				return
			}

			// Check if this is a new unverified user with challenge
			if !user.Verified {
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
		if evt.GetStateKey() == client.UserID.String() {
			// Join rooms on invite
			if evt.Content.AsMember().Membership == event.MembershipInvite {
				_, err := client.JoinRoomByID(evt.RoomID)
				if err == nil {
					store.UpdateJoinedAt(evt.RoomID.String(), time.Now())

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
			shouldIgnore, err := checkIfShouldIgnore(evt, log, client, cfg.IgnoredUsers)
			if err != nil {
				log.Error().Err(err).
					Str("room_id", evt.RoomID.String()).
					Str("userID", evt.Sender.String()).
					Msg("Failed to check if event should be ignored")
			}
			if shouldIgnore {
				return
			}

			// Welcome new users, if 30 seconds have passed since we joined the room
			// Because otherwise we will send a welcome message to users, already in the room
			if store.GetJoinedAt(evt.RoomID.String()).Before(time.Now().Add(-time.Second * 30)) {
				err = welcomeNewUser(evt, client, cfg.TimeToSolve, db, log)
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
			} else {
				log.Info().Msg("Ignoring user join event, because we just joined the room")
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
	// cryptoHelper.DBAccountID = ""
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

func handleConfig() (config, error) {
	cfg := config{}
	if err := env.Parse(&cfg); err != nil {
		return cfg, fmt.Errorf("parsing config: %w", err)
	}

	var ignoredPatterns []string
	// Trim spaces from ignored users
	for _, user := range cfg.IgnoredUsers {
		regexPattern := strings.TrimSpace(user)

		if regexPattern == "" {
			continue
		}

		// Check if regexp is valid
		_, err := regexp.Compile(regexPattern)
		if err != nil {
			return cfg, fmt.Errorf("parsing regexp for ignored user %s: %w", user, err)
		}

		ignoredPatterns = append(ignoredPatterns, regexPattern)
	}
	cfg.IgnoredUsers = ignoredPatterns

	if cfg.UserID == "" || cfg.Password == "" {
		return cfg, fmt.Errorf("required env not set")
	}

	return cfg, nil
}

func checkIfShouldIgnore(evt *event.Event, log zerolog.Logger, client *mautrix.Client, ignoredUsers []string) (bool, error) {
	// Filter out old events
	if evt.Timestamp < time.Now().Add(-time.Hour).UnixMilli() {
		log.Info().Msg("Ignoring too old event")
		return true, nil
	}

	// Get room permissions
	powerLevels := client.StateStore.GetPowerLevels(evt.RoomID)
	if powerLevels == nil {
		return false, fmt.Errorf("failed to get power levels for room %s", evt.RoomID)
	}

	// Ignore admins
	if powerLevels.Users[evt.Sender] >= 50 {
		log.Info().Msg("User is admin, ignoring")
		return true, nil
	}

	for _, ignoredUser := range ignoredUsers {
		match, err := regexp.Match(ignoredUser, []byte(evt.Sender.String()))
		if err != nil {
			return false, fmt.Errorf("failed to match regex: %w", err)
		}
		if match {
			log.Info().Msg("Ignoring event from ignored user " + evt.Sender.String() + " because of " + ignoredUser)
			return true, nil
		}
	}

	return false, nil
}

func randInRange(min, max int) int {
	return rand.Intn(max-min) + min
}

// createChallenge creates a new challenge for the user
// challenge is solving math with a random numbers between 1 and 50
func createChallenge(timeToSolve int) fastore.Challenge {
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
		Expiry:    time.Now().Add(time.Minute * time.Duration(timeToSolve)),
	}
}

func welcomeNewUser(evt *event.Event, client *mautrix.Client, timeToSolve int, db *database.DB, log zerolog.Logger) error {
	user, err := db.GetUser(evt.RoomID.String(), evt.Sender.String())
	if err != nil && !errors.Is(err, database.NotFound) {
		return fmt.Errorf("failed to get user: %w", err)
	}

	if !errors.Is(err, database.NotFound) && user.Verified {
		_, err := client.SendNotice(evt.RoomID, "Welcome back, "+evt.Sender.String()+"!")
		if err != nil {
			return fmt.Errorf("failed to send welcome message: %w", err)
		}
	}

	userData := store.GetUserData(evt.RoomID.String(), evt.Sender.String())

	// If the user does not have a valid challenge, send a new one
	if userData.Challenge.TaskMessageID == "" || userData.Challenge.Expiry.Before(time.Now()) {
		challenge := createChallenge(timeToSolve)

		welcomeMessage := fmt.Sprintf("Welcome, %s! Please solve the following challenge to stay in the room:\n", evt.Sender.String()) +
			fmt.Sprintf("Добро пожаловать, %s! Пожалуйста, решите следующее уравнение, чтобы вас не кикнул бот:\n", evt.Sender.String()) +
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

			user, err = db.GetUser(evt.Sender.String(), evt.RoomID.String())
			if err != nil && !errors.Is(err, database.NotFound) {
				log.Error().Err(err).
					Msg("Failed to get user from database")
				return
			}
			log.Info().
				Bool("isVerified", user.Verified).
				Msg("Checking if user solved the challenge in time")

			if errors.Is(err, database.NotFound) || !user.Verified {
				// This can fail if the user left the room or already got kicked
				_, err = client.KickUser(evt.RoomID, &mautrix.ReqKickUser{UserID: evt.Sender, Reason: "Did not solve math in time"})
				if err != nil {
					log.Error().Err(err).
						Msg("Failed to kick the user")
				}

				_, err := client.RedactEvent(evt.RoomID, challenge.TaskMessageID)
				if err != nil {
					log.Error().Err(err).
						Msg("Failed to remove challenge message")
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
	answerMatchPattern := regexp.MustCompile(`[-+]?[0-9]+`)
	answer := answerMatchPattern.FindAllString(message.Body, -1)
	if len(answer) == 0 {
		return false
	}

	userAnswer, err := strconv.ParseInt(answer[len(answer)-1], 10, 32)
	if err != nil {
		return false
	}

	if int(userAnswer) == correctAnswer {
		return true
	} else {
		return false
	}
}
