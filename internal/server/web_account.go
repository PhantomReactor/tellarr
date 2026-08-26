package server

import (
	"errors"
	"log/slog"
	"net/http"

	"tellarr/internal/web/views"
)

func (s *Server) webAccount(w http.ResponseWriter, r *http.Request) {
	claims, _ := r.Context().Value("claims").(*Claims)
	username := ""
	if claims != nil {
		username = claims.UserName
	}
	msg, errMsg := popFlash(w, r, "msg"), popFlash(w, r, "err")
	_ = views.AccountPage(username, msg, errMsg).Render(r.Context(), w)
}

// webAccountPassword changes the signed-in user's password. The current
// password is always required, even though the session cookie is valid.
func (s *Server) webAccountPassword(w http.ResponseWriter, r *http.Request) {
	claims, _ := r.Context().Value("claims").(*Claims)
	if claims == nil {
		http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
		return
	}

	current := r.FormValue("current_password")
	next := r.FormValue("new_password")
	if len(next) < 6 {
		redirectFlash(w, r, "/ui/account", "", "new password must be at least 6 characters")
		return
	}

	u, err := s.findUser("", claims.UserID)
	if err != nil || u == nil {
		slog.Error("account page could not load current user", "userId", claims.UserID, "err", err)
		redirectFlash(w, r, "/ui/account", "", "could not update password")
		return
	}

	if _, err := s.updatePassword(u.Username, current, next); err != nil {
		if errors.Is(err, ErrPasswordInvalid) {
			redirectFlash(w, r, "/ui/account", "", "current password is incorrect")
			return
		}
		slog.Error("failed to update password", "userId", claims.UserID, "err", err)
		redirectFlash(w, r, "/ui/account", "", "could not update password")
		return
	}

	// Invalidate API refresh tokens so old sessions can't be renewed.
	if err := s.deleteRefreshToken(0, claims.UserID); err != nil {
		slog.Error("failed to revoke refresh tokens after password change", "userId", claims.UserID, "err", err)
	}
	slog.Info("password updated", "userId", claims.UserID)
	redirectFlash(w, r, "/ui/account", "password updated", "")
}
