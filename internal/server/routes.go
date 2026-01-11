package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"tellarr/internal/pkg/models"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

func (s *Server) RegisterRoutes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"https://*", "http://*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Get("/", s.HelloWorldHandler)

	r.Post("/add", s.add)

	//r.Get("/health", s.healthHandler)

	return r
}

func (s *Server) HelloWorldHandler(w http.ResponseWriter, r *http.Request) {
	resp := make(map[string]string)
	resp["message"] = "Hello World"

	jsonResp, err := json.Marshal(resp)
	if err != nil {
		log.Fatalf("error handling JSON marshal. Err: %v", err)
	}

	_, _ = w.Write(jsonResp)
}

func (s *Server) add(w http.ResponseWriter, r *http.Request) {
	fmt.Println("tessst")
	var telegramUser models.AuthRequest
	err := json.NewDecoder(r.Body).Decode(&telegramUser)
	fmt.Println(telegramUser)
	if err != nil {
		http.Error(w, "Invalid credentials", http.StatusBadRequest)
		return
	}

	client := telegram.NewClient(telegramUser.AppId, telegramUser.AppHash, telegram.Options{
		SessionStorage: &session.FileStorage{
			Path: "session.json",
		},
	})

	err = client.Run(context.Background(), func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return err
		}
		if !status.Authorized {
			sendCodeRes, sendCodeErr := client.Auth().SendCode(ctx, "phone", auth.SendCodeOptions{})
			if sendCodeErr != nil {
				fmt.Printf("This is the error from the send code: %v\n", sendCodeErr)
			}

			fmt.Printf("Please input the code sent to you...\n")
			var textCode string
			fmt.Scan(&textCode)
			sendCode, _ := sendCodeRes.(*tg.AuthSentCode)
			authRes, signInErr := client.Auth().SignIn(ctx, "phone", textCode, sendCode.PhoneCodeHash)
			if signInErr != nil {
				fmt.Printf("This is the error from the signin: %v\n", signInErr)
			}

			fmt.Printf("This is the authorization response: %v\n", authRes)

		}

		fmt.Printf("Client is running...\n")
		api := client.API()
		dialogs, err := api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
			OffsetPeer: &tg.InputPeerEmpty{},
			Limit:      100,
		})
		if err != nil {
			return fmt.Errorf("get dialogs failed: %w", err)
		}

		// Find your channel in the dialogs
		dialogsSlice := dialogs.(*tg.MessagesDialogsSlice) // or *tg.MessagesDialogs
		for _, chat := range dialogsSlice.Chats {
			if ch, ok := chat.(*tg.Channel); ok {
				fmt.Printf("Channel: %s, ID: %d, AccessHash: %d\n", ch.Title, ch.ID, ch.AccessHash)
				// Find the one named "database 1"
				if ch.Title == "DATABASE 1" {
					peer := &tg.InputPeerChannel{
						ChannelID:  ch.ID,
						AccessHash: ch.AccessHash,
					}
					messages, err := api.MessagesSearch(ctx, &tg.MessagesSearchRequest{
						Peer:   peer,
						Q:      "Mission impossible",
						Limit:  10,
						Filter: &tg.InputMessagesFilterEmpty{},
					})
					if err != nil {
						fmt.Println(err)
					}
					res, ok := messages.(*tg.MessagesChannelMessages)
					if !ok {
						return fmt.Errorf("unexpected type: %T", messages)
					}
					msgs := res.Messages
					for _, m := range msgs {
						msg, ok := m.(*tg.Message)
						if !ok {
							fmt.Println("error")
						}
						fmt.Println(msg.Media.TypeName())
					}
					fmt.Println(messages)
				}
			}
		}
		return nil
	})
	if err != nil {
		http.Error(w, "Authentication error", http.StatusInternalServerError)
	}
	http.Error(w, "successj", http.StatusAccepted)
}

// func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
// 	jsonResp, _ := json.Marshal(s.db.Health())
// 	_, _ = w.Write(jsonResp)
// }
