// rawsearch is a debugging tool: it connects to Telegram with the app's own
// stored session and runs the raw messages.search call the torznab endpoint
// performs, printing what Telegram actually returns before any filtering —
// message ids, media types, extracted URLs and the text Tellarr indexes.
// Run it on the host that owns the real DB offline.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/joho/godotenv"
	"tellarr/internal/database"
)

func main() {
	_ = godotenv.Load()
	dialogName := flag.String("dialog", "", "channel/dialog name as stored in tellarr DB")
	query := flag.String("q", "", "search text")
	msgId := flag.Int64("msg", 0, "fetch one raw message by id instead of searching")
	sessionId := flag.Int64("session", -1, "session id (defaults to first active)")
	limit := flag.Int("limit", 50, "search limit")
	flag.Parse()

	db := database.New()
	sessionRepo := database.NewSessionRepository(db.DB)
	dialogRepo := database.NewDialogsRepository(db.DB)

	sid := *sessionId
	if sid < 0 {
		ids, err := sessionRepo.GetAllSessionIds()
		if err != nil || len(ids) == 0 {
			fmt.Fprintln(os.Stderr, "no active sessions:", err)
			os.Exit(1)
		}
		sid = ids[0]
	}

	dialog, err := dialogRepo.GetDialogByName(*dialogName)
	if err != nil || dialog == nil {
		fmt.Fprintln(os.Stderr, "dialog not found:", *dialogName)
		os.Exit(1)
	}
	fmt.Println("dialog:", dialog.Name, "dialog_id:", dialog.DialogId, "session:", dialog.SessionId)

	appId, _ := strconv.Atoi(os.Getenv("APP_ID"))
	client := telegram.NewClient(appId, os.Getenv("APP_HASH"), telegram.Options{
		RetryInterval: 5 * time.Second,
		MaxRetries:    5,
		SessionStorage: &database.DBSessionStorage{
			SessionRepository: sessionRepo,
			SessionID:         sid,
		},
	})

	err = client.Run(context.Background(), func(ctx context.Context) error {
		api := client.API()

		if *msgId > 0 {
			res, err := api.MessagesGetMessages(ctx, []tg.InputMessageClass{&tg.InputMessageID{ID: int(*msgId)}})
			if err != nil {
				return err
			}
			msgs, err := printIDs(res)
			if err != nil {
				return err
			}
			for _, m := range msgs {
				if msg, ok := m.(*tg.Message); ok {
					printMsg(msg)
				} else {
					fmt.Println("  non-message:", m)
				}
			}
			return nil
		}

		res, err := api.MessagesSearch(ctx, &tg.MessagesSearchRequest{
			Peer: &tg.InputPeerChannel{
				ChannelID:  dialog.DialogId,
				AccessHash: dialog.AccessHash,
			},
			Q:      *query,
			Limit:  *limit,
			Filter: &tg.InputMessagesFilterEmpty{},
		})
		if err != nil {
			return err
		}
		cm, ok := res.(*tg.MessagesChannelMessages)
		if !ok {
			return fmt.Errorf("unexpected response type %T", res)
		}
		fmt.Printf("search %q -> %d messages (count field: %d)\n",
			*query, len(cm.Messages), cm.Count)
		for _, m := range cm.Messages {
			msg, ok := m.(*tg.Message)
			if !ok {
				fmt.Println("  non-message:", m)
				continue
			}
			printMsg(msg)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func printIDs(res tg.MessagesMessagesClass) ([]tg.MessageClass, error) {
	if res, ok := res.(*tg.MessagesChannelMessages); ok {
		return res.Messages, nil
	}
	if res, ok := res.(*tg.MessagesMessages); ok {
		return res.Messages, nil
	}
	if d, ok := res.(*tg.MessagesMessagesSlice); ok {
		return d.Messages, nil
	}
	return nil, fmt.Errorf("unexpected type %T", res)
}

func printMsg(msg *tg.Message) {
	fmt.Printf("\n[id %d] date %s media:", msg.ID, time.Unix(int64(msg.Date), 0).UTC().Format(time.RFC3339))
	switch md := msg.Media.(type) {
	case nil:
		fmt.Print(" none (text/link post)")
	case *tg.MessageMediaDocument:
		if doc, ok := md.Document.AsNotEmpty(); ok {
			fmt.Printf(" document mime=%s size=%d attrs=%d", doc.MimeType, doc.Size, len(doc.Attributes))
		} else {
			fmt.Print(" document (empty)")
		}
	default:
		fmt.Printf(" %T", msg.Media)
	}
	fmt.Println()
	fmt.Println("  text:", strconv.Quote(msg.Message))
	for _, e := range msg.Entities {
		if tu, ok := e.(*tg.MessageEntityTextURL); ok {
			fmt.Println("  text-url entity:", tu.URL)
		}
	}
}
