package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"

	db "tellarr/internal/database/models"
	"tellarr/internal/web/views"
)

const sessionCookie = "tellarr_session"

func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) RegisterWebRoutes(r chi.Router) {
	// Embedded assets have no modtime, so without explicit headers browsers
	// may heuristically cache stale JS/CSS forever. Force revalidation.
	r.Handle("/static/*", http.StripPrefix("/static/", noCache(http.FileServer(http.FS(views.StaticFS())))))

	r.Get("/ui/login", func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie(sessionCookie); err == nil {
			http.Redirect(w, r, "/ui/downloads", http.StatusSeeOther)
			return
		}
		_ = views.LoginPage(r.URL.Query().Get("err"), s.canRegister()).Render(r.Context(), w)
	})
	r.Post("/ui/login", s.webLogin)
	r.Get("/ui/register", s.webRegisterPage)
	r.Post("/ui/register", s.webRegister)

	r.Route("/ui", func(r chi.Router) {
		r.Use(s.WebAuth)
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/ui/downloads", http.StatusSeeOther)
		})
		r.Post("/logout", s.webLogout)
		r.Get("/telegram", s.webTelegramPage)
		r.Post("/telegram/phone", s.webTelegramPhone)
		r.Post("/telegram/code", s.webTelegramCode)
		r.Post("/telegram/password", s.webTelegramPassword)
		r.Get("/indexers", s.webIndexers)
		r.Post("/indexers/toggle", s.webIndexerToggle)
		r.Post("/indexers/prowlarr", s.webIndexerProwlarr)
		r.Get("/indexers/prowlarr/yml", s.webIndexerProwlarrYML)
		r.Get("/indexers/prowlarr/yml/view", s.webIndexerProwlarrYMLView)
		r.Get("/downloads", s.webDownloads)
		r.Get("/downloads/table", s.webDownloadsTable)
		r.Post("/downloads/add", s.webDownloadAdd)
		r.Post("/downloads/{id}/{action}", s.webDownloadAction)
		r.Get("/settings", s.webSettings)
		r.Post("/settings/downloads", s.webSettingsDownloads)
		r.Post("/settings/tokens", s.webTokenCreate)
		r.Post("/settings/tokens/{id}/delete", s.webTokenDelete)
		r.Get("/settings/qbit/test", s.webQBitTest)
		r.Get("/account", s.webAccount)
		r.Post("/account/password", s.webAccountPassword)
	})
}

// WebAuth authenticates browser sessions via HttpOnly JWT cookie.
func (s *Server) WebAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
			return
		}
		claims, err := parseToken(c.Value)
		if err != nil {
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
			http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), "claims", claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) setSessionCookie(w http.ResponseWriter, u *db.User) error {
	token, err := generateTokenWithTTL(u, 24*30*time.Hour)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(24 * 30 * time.Hour),
	})
	return nil
}

// canRegister reports whether registration is open. Tellarr is single-user,
// so it closes permanently once the first account exists.
func (s *Server) canRegister() bool {
	exists, err := s.userRepo.HasAnyUser()
	if err != nil {
		slog.Error("failed to check for existing users", "err", err)
		return false
	}
	return !exists
}

func (s *Server) webRegisterPage(w http.ResponseWriter, r *http.Request) {
	if _, err := r.Cookie(sessionCookie); err == nil {
		http.Redirect(w, r, "/ui/downloads", http.StatusSeeOther)
		return
	}
	_ = views.RegisterPage(r.URL.Query().Get("err"), s.canRegister()).Render(r.Context(), w)
}

func (s *Server) webLogin(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")
	u, err := s.findUser(username, 0)
	if err != nil || u == nil {
		_ = views.LoginPage("invalid credentials", s.canRegister()).Render(r.Context(), w)
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		_ = views.LoginPage("invalid credentials", s.canRegister()).Render(r.Context(), w)
		return
	}
	if err := s.setSessionCookie(w, u); err != nil {
		_ = views.LoginPage("could not create session", s.canRegister()).Render(r.Context(), w)
		return
	}
	http.Redirect(w, r, "/ui/downloads", http.StatusSeeOther)
}

func (s *Server) webRegister(w http.ResponseWriter, r *http.Request) {
	if !s.canRegister() {
		http.Redirect(w, r, "/ui/login?err="+url.QueryEscape("registration is closed — an account already exists"), http.StatusSeeOther)
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")
	if len(password) < 6 {
		_ = views.RegisterPage("password must be at least 6 characters", true).Render(r.Context(), w)
		return
	}
	u, err := s.createUser(username, password)
	if err != nil {
		_ = views.RegisterPage(err.Error(), true).Render(r.Context(), w)
		return
	}
	if err := s.setSessionCookie(w, u); err != nil {
		_ = views.RegisterPage("could not create session", true).Render(r.Context(), w)
		return
	}
	http.Redirect(w, r, "/ui/telegram", http.StatusSeeOther)
}

func (s *Server) webLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
}

func renderTelegram(w http.ResponseWriter, r *http.Request, step string, sessionId int64, msg, errMsg string) {
	_ = views.TelegramPage(step, sessionId, msg, errMsg).Render(r.Context(), w)
}

const flashCookiePrefix = "tellarr_flash_"

// setFlash stashes a one-time flash message in a short-lived cookie rather
// than the redirect URL. Query-string flash messages end up in browser
// history, get echoed back in Referer headers, and show up verbatim when a
// request is copied (e.g. "copy as curl") — a cookie only round-trips to
// this server and is cleared the moment it's read.
func setFlash(w http.ResponseWriter, r *http.Request, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookiePrefix + name,
		Value:    url.QueryEscape(value),
		Path:     "/",
		MaxAge:   10,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
}

// popFlash reads a flash cookie set by setFlash and clears it, so it's only
// ever shown once (on the page load right after the redirect).
func popFlash(w http.ResponseWriter, r *http.Request, name string) string {
	c, err := r.Cookie(flashCookiePrefix + name)
	if err != nil || c.Value == "" {
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookiePrefix + name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
	v, err := url.QueryUnescape(c.Value)
	if err != nil {
		return ""
	}
	return v
}

func redirectFlash(w http.ResponseWriter, r *http.Request, path string, msg, errMsg string) {
	if msg != "" {
		setFlash(w, r, "msg", msg)
	}
	if errMsg != "" {
		setFlash(w, r, "err", errMsg)
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}
