package server

import (
	"context"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/httplog/v3"
	"github.com/google/uuid"
	"log/slog"
	"net/http"
	"os"

	"tellarr/internal/pkg/models"
)

func (s *Server) RegisterRoutes() http.Handler {
	r := chi.NewRouter()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	slog.SetLogLoggerLevel(slog.LevelError)
	r.Use(loggingMiddleware)
	r.Use(httplog.RequestLogger(logger, &httplog.Options{
		Level:  slog.LevelInfo,
		Schema: httplog.SchemaECS.Concise(true),
	}))

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"https://*", "http://*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	s.RegisterAuthRouts(r)
	s.RegisterTelegramRoutes(r)
	s.RegisterTorznabRoutes(r)
	s.RegisterQBitRoutes(r)
	r.NotFound(s.webNotFound)
	s.RegisterWebRoutes(r)
	return r
}

func (s *Server) webNotFound(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/ui") || r.URL.Path == "/" {
		http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
		return
	}
	models.NewResponse(w, &models.Response{Message: "not found"}, http.StatusNotFound)
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		requestID := r.Header.Get("requestID")
		if requestID == "" {
			requestID = uuid.New().String()
		}
		ctx = context.WithValue(ctx, middleware.RequestIDKey, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
