package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"tellarr/internal/pkg/models"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
)

func (s *Server) RegisterAuthRouts(r chi.Router) {
	r.Route("/api/auth", func(r chi.Router) {
		r.Post("/register", s.HandlRegister)
		r.Post("/login", s.HandleLogin)

		r.Group(func(r chi.Router) {
			r.Use(JWTAuth)
			r.Post("/refresh", s.HandleRefresh)
			r.Post("/logout", s.HandleLogout)
			r.Post("/reset", s.HandleReset)
		})
	})
}

func (s *Server) HandleLogin(w http.ResponseWriter, r *http.Request) {
	var req models.UserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Error("unable to decode request", "error", err)
		models.NewResponse(w, models.Response{Message: "unable to decode request"}, http.StatusInternalServerError)
		return
	}

	user, err := s.findUser(req.Username, 0)
	if err != nil {
		slog.Error(fmt.Sprintf("error while fetching user for %s", req.Username), "error", err)
		models.NewResponse(w, models.Response{Message: "error while fetching user"}, http.StatusInternalServerError)
		return
	}

	if user != nil {
		slog.Error(fmt.Sprintf("user nor found with %s", req.Username))
		models.NewResponse(w, models.Response{Message: "user not found"}, http.StatusNotFound)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		slog.Error("invalid credentials", "error", err)
		models.NewResponse(w, models.Response{Message: "invalid credentials"}, http.StatusUnauthorized)
		return
	}
	token, err := generateToken(user)
	if err != nil {
		slog.Error("error while generating token", "error", err)
		models.NewResponse(w, models.Response{Message: "error while generating token"}, http.StatusUnauthorized)
		return
	}

	refreshToken, err := s.generateRefreshToken(user.ID)
	if err != nil {
		slog.Error("error while generating refresh token", "error", err)
		models.NewResponse(w, models.Response{Message: "error while generating refresh token"}, http.StatusInternalServerError)
		return
	}
	models.NewResponse(w, models.AuthResponse{Username: user.Username, Token: token, RefreshToken: refreshToken.Token, ExpiresIn: 15 * 60}, http.StatusOK)
}

func (s *Server) HandlRegister(w http.ResponseWriter, r *http.Request) {
	var req models.UserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Error("unable to decode request", "error", err)
		models.NewResponse(w, models.Response{Message: "unable to decode request"}, http.StatusInternalServerError)
		return
	}

	if req.Username == "" || req.Password == "" {
		slog.Error("username or password empty")
		models.NewResponse(w, models.Response{Message: "username and password are required"}, http.StatusBadRequest)
		return
	}

	user, err := s.createUser(req.Username, req.Password)
	if err != nil || user == nil {
		slog.Error("error while creating user", "error", err)
		models.NewResponse(w, models.Response{Message: "user registration failed"}, http.StatusInternalServerError)
		return
	}
	models.NewResponse(w, models.AuthResponse{Id: user.ID, Username: user.Username}, http.StatusOK)

}

func (s *Server) HandleRefresh(w http.ResponseWriter, r *http.Request) {
	var req models.UserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Error("unable to decode request", "error", err)
		models.NewResponse(w, models.Response{Message: "unable to decode request"}, http.StatusInternalServerError)
		return
	}

	if req.UserId == 0 || req.Token == "" {
		slog.Error("userId or refreshToken empty")
		models.NewResponse(w, models.Response{Message: "userId and refreshToken are required"}, http.StatusInternalServerError)
		return
	}

	rt, err := s.validateRefreshToken(req.UserId, req.Token)
	if errors.Is(err, ErrTokenInvalid) {
		slog.Error("invalid token", "error", err)
		models.NewResponse(w, models.Response{Message: "invalid token"}, http.StatusUnauthorized)
		return
	}

	if errors.Is(err, ErrTokenExpired) {
		slog.Error("token expired", "error", err)
		models.NewResponse(w, models.Response{Message: "token expired"}, http.StatusUnauthorized)
		return
	}

	if err != nil {
		slog.Error("error while fetching refreshToken", "error", err)
		models.NewResponse(w, models.Response{Message: "invalid token"}, http.StatusInternalServerError)
		return
	}
	user, err := s.findUser("", req.UserId)
	if err != nil {
		slog.Error("error while fetching user", "error", err)
		models.NewResponse(w, models.Response{Message: "error while fetching user"}, http.StatusInternalServerError)
		return
	}
	if user != nil {
		slog.Error("user not found")
		models.NewResponse(w, models.Response{Message: "user not found"}, http.StatusNotFound)
		return
	}
	err = s.deleteRefreshToken(rt.ID, 0)
	if err != nil {
		slog.Error("error while deleting refresh token")
		models.NewResponse(w, models.Response{Message: "error while deleting refresh token"}, http.StatusInternalServerError)
		return
	}

	refreshToken, err := s.generateRefreshToken(user.ID)
	if err != nil {
		slog.Error("error while generating refresh token", "error", err)
		models.NewResponse(w, models.Response{Message: "error while generating refresh token"}, http.StatusInternalServerError)
		return
	}

	token, err := generateToken(user)
	if err != nil {
		slog.Error("error while generating token", "error", err)
		models.NewResponse(w, models.Response{Message: "error while generating token"}, http.StatusUnauthorized)
		return
	}
	models.NewResponse(w, models.AuthResponse{Username: user.Username, Token: token, RefreshToken: refreshToken.Token, ExpiresIn: 15 * 60}, http.StatusOK)
}

func (s *Server) HandleLogout(w http.ResponseWriter, r *http.Request) {
	var req models.UserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Error("unable to decode request", "error", err)
		models.NewResponse(w, models.Response{Message: "unable to decode request"}, http.StatusInternalServerError)
		return
	}

	if err := s.deleteRefreshToken(0, req.UserId); err != nil {
		slog.Error("error while deleting refresh token", "error", err)
		models.NewResponse(w, models.Response{Message: "logout failed"}, http.StatusInternalServerError)
		return
	}
	models.NewResponse(w, models.Response{Message: "logout successful"}, http.StatusOK)
}

func (s *Server) HandleReset(w http.ResponseWriter, r *http.Request) {
	var req models.UserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Error("unable to decode request", "error", err)
		models.NewResponse(w, models.Response{Message: "unable to decode request"}, http.StatusInternalServerError)
		return
	}

	if err := s.deleteRefreshToken(0, req.UserId); err != nil {
		slog.Error("error while deleting refresh token", "error", err)
		models.NewResponse(w, models.Response{Message: "logout failed"}, http.StatusInternalServerError)
		return
	}

	if req.Username == "" || req.Password == "" {
		slog.Error("username or password empty")
		models.NewResponse(w, models.Response{Message: "username and password are required"}, http.StatusBadRequest)
		return
	}

	user, err := s.updatePassword(req.Username, req.Password, req.CurrentPassword)
	if err != nil || user == nil {
		slog.Error("error while creating user", "error", err)
		models.NewResponse(w, models.Response{Message: "user registration failed"}, http.StatusInternalServerError)
		return
	}
	if err = s.deleteRefreshToken(0, user.ID); err != nil {
		slog.Error("error while deleting refresh token", "error", err)
		models.NewResponse(w, models.Response{Message: "password reset failed"}, http.StatusInternalServerError)
		return
	}

	models.NewResponse(w, models.AuthResponse{Id: user.ID, Username: user.Username}, http.StatusOK)

}
