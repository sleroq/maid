package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caarlos0/env/v6"
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
var cryptoDatabase = "crypto.db"

const TIME_TO_SOLVE = time.Minute * 5
const TIME_TO_REMIND = time.Hour * 6

func initDB(location string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", location)
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(
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

	db, err := initDB(database)
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

		err = handleMessage(evt, client, db, log)
		if err != nil {
			log.Error().Err(err).
				Str("room_id", evt.RoomID.String()).
				Str("userID", evt.Sender.String()).
				Msg("Failed to process message")
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

	cryptoHelper, err := cryptohelper.NewCryptoHelper(client, []byte("meow"), cryptoDatabase)
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

type UserData struct {
	Solution   int
	Expiry     int64
	Tries      int
	BotMessage id.EventID
}
type Chat struct {
	Users map[string]*UserData
}
type Tests struct {
	Chats map[string]*Chat
	mu    sync.Mutex
}

var tests = new(Tests)

func getUserData(data *Tests, roomID string, userID string) *UserData {
	data.mu.Lock()
	defer data.mu.Unlock()
	m := data.Chats
	if m == nil {
		data.Chats = make(map[string]*Chat)
	}
	_, ok := data.Chats[roomID]
	if !ok {
		data.Chats[roomID] = &Chat{Users: map[string]*UserData{}}
	}
	_, ok = data.Chats[roomID].Users[userID]
	if !ok {
		expiry := time.Now().Add(time.Second * 30).Unix()
		data.Chats[roomID].Users[userID] = &UserData{Solution: 0, Expiry: expiry, Tries: 0, BotMessage: ""}
	}

	return data.Chats[roomID].Users[userID]
}

func randInRange(min, max int) int {
	return rand.Intn(max-min) + min
}

func welcomeNewUser(evt *event.Event, client *mautrix.Client, db *sql.DB, log zerolog.Logger) error {
	user, err := saveOrGetUser(evt.Sender.String(), evt.RoomID.String(), false, db)
	if err != nil {
		return fmt.Errorf("getting/saving joined user: %w", err)
	}

	longTimeAgo := time.Now().Add(-(TIME_TO_REMIND))

	fmt.Println(longTimeAgo)
	fmt.Println(user.lastJoin)
	userData := getUserData(tests, evt.RoomID.String(), evt.Sender.String())

	if !user.verified && (user.lastJoin.Before(longTimeAgo) || userData.Solution == 0) {
		task := ""
		numbersLen := randInRange(2, 3)
		answer := 0

		for i := 0; i < numbersLen; i++ {
			number := randInRange(0, 50)
			answer += number

			if i > 0 && number >= 0 {
				task += " + "
			}
			task += strconv.Itoa(number)
		}

		uData := getUserData(tests, evt.RoomID.String(), evt.Sender.String())
		tests.mu.Lock()
		uData.Solution = answer
		tests.mu.Unlock()

		reply := fmt.Sprintf("Welcume, %s! Solve this to send messages:\n", evt.Sender.String())
		reply += fmt.Sprintf("Привет, %s! Реши эту штуку чтобы писать в чат:\n", evt.Sender.String())
		reply += fmt.Sprintf("%s", task)
		res, err := client.SendNotice(evt.RoomID, reply)
		if err != nil {
			return fmt.Errorf("welcoming user: %w", err)
		}
		tests.mu.Lock()
		uData.BotMessage = res.EventID
		tests.mu.Unlock()

		go func() {
			time.Sleep(TIME_TO_SOLVE)
			user, err = getUser(evt.Sender.String(), evt.RoomID.String(), db)
			if !user.verified {
				_, err = client.KickUser(evt.RoomID, &mautrix.ReqKickUser{UserID: evt.Sender, Reason: "Did not solve math in time"})
				if err != nil {
					log.Error().Err(err).
						Str("user", user.id).
						Msg("Failed to kick the user")
				}
			}
		}()
	}

	return nil
}

func handleMessage(evt *event.Event, client *mautrix.Client, db *sql.DB, log zerolog.Logger) error {
	user, err := getUser(evt.Sender.String(), evt.RoomID.String(), db)
	if err == notFound {
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting user: %w", err)
	}

	message := evt.Content.AsMessage()
	if !user.verified {
		data := getUserData(tests, evt.RoomID.String(), evt.Sender.String())

		if userIsRight(message, data.Solution) {
			user.verified = true
			err = updateUser(user, db)
			if err != nil {
				return fmt.Errorf("updating user: %w", err)
			}

			_, err = client.SendReaction(evt.RoomID, evt.ID, "👍️")
			if err != nil {
				return fmt.Errorf("reacting to user message: %w", err)
			}

			go func() {
				time.Sleep(time.Minute)

				err = redactEvents(client, evt.RoomID, evt.ID, data.BotMessage)
				if err != nil {
					log.Error().Err(err)
				}
			}()

			return nil
		} else {
			data.Tries++
			if data.Tries > 2 {
				_, err = client.RedactEvent(evt.RoomID, evt.ID)
				if err == notFound {
					return fmt.Errorf("deleting message: %s", err)
				}

				_, err = client.KickUser(evt.RoomID, &mautrix.ReqKickUser{
					Reason: "Stupid?",
					UserID: evt.Sender,
				})

				if err != nil {
					return fmt.Errorf("kicking user: %w", err)
				}

				go func() {
					time.Sleep(time.Second * 5)
					data.Tries = 0
				}()
			}
		}

		_, err = client.RedactEvent(evt.RoomID, evt.ID)
		if err == notFound {
			return fmt.Errorf("deleting message: %s", err)
		}
	}

	return nil
}

func redactEvents(client *mautrix.Client, roomID id.RoomID, events ...id.EventID) (err error) {
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

type User struct {
	id       string
	verified bool
	muted    bool
	lastJoin time.Time
}

var notFound = errors.New("not found")

func getUser(userID string, roomID string, db *sql.DB) (*User, error) {
	row := db.QueryRow(
		`select * from users
		 where id = :id`,
		userID+roomID,
	)
	user := new(User)
	err := row.Scan(&user.id, &user.verified, &user.muted, &user.lastJoin)

	if err == sql.ErrNoRows {
		return nil, notFound
	}

	if err != nil {
		return nil, fmt.Errorf("scanning user: %w", err)
	}

	return user, nil
}

func saveOrGetUser(userID, roomID string, verified bool, db *sql.DB) (*User, error) {
	user, err := getUser(userID, roomID, db)
	if err == notFound {
		user = &User{id: userID, verified: verified, muted: false, lastJoin: time.Now()}
		_, err = db.Exec(
			`insert into users (id, verified, muted, last_join)
             values (:id, :verified, :muted, :lastJoin)`,
			user.id+roomID, user.verified, user.muted, user.lastJoin,
		)
		if err != nil {
			return nil, fmt.Errorf("saving new user: %w", err)
		}
		err = nil
	}

	if err != nil {
		return nil, fmt.Errorf("getting user: %w", err)
	}

	return user, nil
}

func updateUser(user *User, db *sql.DB) error {
	_, err := db.Exec(
		`update users 
    	 set verified = :verified, muted = :muted, last_join = :lastJoin
         where id = :id`,
		user.verified, user.muted, user.lastJoin,
		user.id,
	)

	if err != nil {
		return fmt.Errorf("updating user: %w", err)
	}

	return nil
}
