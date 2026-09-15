package api

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/denizsincar29/mdcloud/internal/auth"
	"github.com/denizsincar29/mdcloud/internal/mdpath"
	"github.com/denizsincar29/mdcloud/internal/models"
)

// userView — то, что можно показывать наружу. Хеш пароля не покидает сервер.
type userView struct {
	ID          uint   `json:"id"`
	Username    string `json:"username"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"display_name"`
	IsAdmin     bool   `json:"is_admin"`
}

func viewUser(u *models.User) userView {
	return userView{
		ID:          u.ID,
		Username:    u.Username,
		Email:       u.EmailString(),
		DisplayName: u.DisplayName,
		IsAdmin:     u.IsAdmin,
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "time": time.Now().UTC()})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if !s.lim.allow("reg:"+clientIP(r), 10, time.Hour) {
		writeErr(w, http.StatusTooManyRequests, "слишком много попыток регистрации, попробуйте позже")
		return
	}
	var in struct {
		Username    string `json:"username"`
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if !decodeJSON(w, r, &in, 4096) {
		return
	}

	username := mdpath.CanonicalUsername(in.Username)
	if !mdpath.ValidUsername(username) {
		writeErr(w, http.StatusBadRequest,
			"имя: 3–32 символа, латиница в нижнем регистре, цифры, дефис и подчёркивание")
		return
	}
	if len(in.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "пароль короче восьми символов")
		return
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if email != "" && (!strings.Contains(email, "@") || len(email) > 255) {
		writeErr(w, http.StatusBadRequest, "адрес почты выглядит странно")
		return
	}

	var count int64
	if err := s.db.Model(&models.User{}).Count(&count).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}
	// Первый пользователь проходит всегда: он и есть хозяин облака.
	if !s.cfg.AllowRegistration && count > 0 {
		writeErr(w, http.StatusForbidden, "регистрация закрыта")
		return
	}

	if s.userExists(username, email) {
		writeErr(w, http.StatusConflict, "такое имя или почта уже заняты")
		return
	}

	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог захешировать пароль")
		return
	}
	u := models.User{
		Username:     username,
		PasswordHash: hash,
		DisplayName:  strings.TrimSpace(in.DisplayName),
	}
	if email != "" {
		u.Email = &email
	}
	if err := s.db.Create(&u).Error; err != nil {
		log.Printf("создание пользователя %s: %v", username, err)
		writeErr(w, http.StatusInternalServerError, "не смог создать пользователя")
		return
	}

	tok, exp, err := s.issueSession(u.ID, s.cfg.SessionTTL)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог выдать токен")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      tok,
		"expires_at": exp,
		"user":       viewUser(&u),
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &in, 4096) {
		return
	}
	if !s.lim.allow("login:"+clientIP(r), 20, 10*time.Minute) {
		writeErr(w, http.StatusTooManyRequests, "слишком много попыток входа, подождите немного")
		return
	}

	login := strings.ToLower(strings.TrimSpace(in.Login))
	var u models.User
	err := s.db.Where("username = ? OR email = ?", login, login).First(&u).Error
	if err != nil || !auth.CheckPassword(u.PasswordHash, in.Password) {
		// Одинаковая ошибка и небольшая задержка: не подсказываем, что
		// именно неверно — имя или пароль.
		time.Sleep(300 * time.Millisecond)
		writeErr(w, http.StatusUnauthorized, "неверное имя или пароль")
		return
	}

	tok, exp, err := s.issueSession(u.ID, s.cfg.SessionTTL)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог выдать токен")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      tok,
		"expires_at": exp,
		"user":       viewUser(&u),
	})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, u *models.User) {
	if tok := bearerToken(r); tok != "" {
		s.db.Where("token_hash = ? AND kind = ?", auth.HashToken(tok), models.KindSession).
			Delete(&models.Session{})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, u *models.User) {
	var docs int64
	s.db.Model(&models.Doc{}).Where("owner_id = ?", u.ID).Count(&docs)
	writeJSON(w, http.StatusOK, map[string]any{"user": viewUser(u), "docs": docs})
}

func (s *Server) userExists(username, email string) bool {
	var n int64
	q := s.db.Model(&models.User{}).Where("username = ?", username)
	if email != "" {
		q = q.Or("email = ?", email)
	}
	q.Count(&n)
	return n > 0
}
