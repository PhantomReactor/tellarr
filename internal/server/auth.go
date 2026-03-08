package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"os"
	"strings"
	db "tellarr/internal/database/models"
	"tellarr/internal/pkg/models"
	"time"
)

type Claims struct {
	UserID   int64  `json:"userId"`
	UserName string `json:"username"`
	jwt.RegisteredClaims
}

var ErrTokenExpired = errors.New("refresh token expired")
var ErrTokenInvalid = errors.New("invalid token")
var ErrPasswordInvalid = errors.New("invalid password")
var ErrUserNotFound = errors.New("user not found")

func (s *Server) createUser(username string, password string) (*db.User, error) {
	user, err := s.userRepo.GetUser(username, 0)
	if err != nil {
		return user, err
	}
	if user != nil {
		return nil, fmt.Errorf("username taken")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	u := db.User{Username: username, PasswordHash: string(hash), CreatedAt: time.Now(), UpdatedAt: time.Now()}
	id, err := s.userRepo.CreateUser(u)
	if err != nil {
		return nil, err
	}
	u.ID = id
	return &u, nil
}

func (s *Server) updatePassword(username string, currentPassowrd string, newPassword string) (*db.User, error) {
	user, err := s.userRepo.GetUser(username, 0)
	if err != nil {
		return user, err
	}
	if user == nil {
		return user, ErrUserNotFound
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(currentPassowrd)); err != nil {
		return nil, ErrPasswordInvalid
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	u := db.User{Username: username, PasswordHash: string(hash), UpdatedAt: time.Now()}
	err = s.userRepo.UpdateUser(u)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Server) findUser(username string, id int64) (*db.User, error) {
	user, err := s.userRepo.GetUser(username, id)
	if err != nil {
		return user, err
	}
	return user, nil
}

func (s *Server) deleteRefreshToken(id int64, userId int64) error {
	var err error
	if id != 0 {
		err = s.refreshTokenRepo.Delete(id)
	} else {
		err = s.refreshTokenRepo.DeleteByUserId(userId)
	}
	if err != nil {
		return err
	}
	return nil
}

func generateToken(u *db.User) (string, error) {
	jwtSecret := os.Getenv("JWT_SECRET")
	claims := &Claims{
		UserID:   u.ID,
		UserName: u.Username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

func (s *Server) generateRefreshToken(userId int64) (*db.RefreshToken, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	rt := &db.RefreshToken{
		Token:     hex.EncodeToString(b),
		UserId:    userId,
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	id, err := s.refreshTokenRepo.CreateRefreshToken(rt)
	if err != nil {
		return nil, err
	}
	rt.ID = id
	return rt, nil
}

func (s *Server) validateRefreshToken(userId int64, token string) (*db.RefreshToken, error) {
	rt, err := s.refreshTokenRepo.GetRefreshToken(userId)
	if err != nil {
		return nil, err
	}
	if rt == nil {
		return nil, nil
	}
	if rt.Token != token {
		return nil, ErrTokenInvalid
	}
	if time.Now().After(rt.ExpiresAt) {
		err := s.deleteRefreshToken(rt.ID, 0)
		if err != nil {
			return nil, err
		}
		return nil, ErrTokenExpired

	}
	return rt, nil
}

func parseToken(tokenString string) (*Claims, error) {
	jwtSecret := os.Getenv("JWT_SECRET")
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("invalid signing method")
		}
		return jwtSecret, nil
	})

	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)

	if !ok || !token.Valid {
		return nil, errors.New("invalid token")
	}

	return claims, nil
}

func JWTAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			models.NewResponse(w, models.Response{Message: "missing Authoriaton header"}, http.StatusUnauthorized)
			return
		}

		parts := strings.SplitN(" ", authHeader, 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
			models.NewResponse(w, models.Response{Message: "bearer token required"}, http.StatusUnauthorized)
			return
		}

		claims, err := parseToken(parts[1])
		if err != nil {
			models.NewResponse(w, models.Response{Message: "invalid or expired token"}, http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), "claims", claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
