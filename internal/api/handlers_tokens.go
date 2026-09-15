package api

// API-ключи аккаунта.
//
// Ключ нужен тому, кто работает с облаком не из браузера: скрипту, пайплайну,
// ассистенту. Он живёт до срока или до отзыва и ходит в заголовке
// Authorization: Bearer. В базе лежит только хеш, поэтому ключ показывается
// ровно один раз — при выдаче; потеряли — выписывайте новый, старый отзовите.

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/denizsincar29/mdcloud/internal/auth"
	"github.com/denizsincar29/mdcloud/internal/models"
)

// tokenView — ключ наружу. Самого ключа здесь нет и быть не может.
type tokenView struct {
	ID         uint       `json:"id"`
	Label      string     `json:"label"`
	State      string     `json:"state"` // active | expired
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// Сроки, которые можно выбрать при выдаче ключа. Ноль дней — бессрочный.
const (
	tokenMaxDays = 3650 // десять лет; больше — уже «бессрочный», и незачем
)

// createToken — POST /api/tokens {"label": "дайджест", "days": 30}
//
// Без days (или с days: 0) ключ бессрочный — такой удобно положить в скрипт,
// который ходит в облако каждый день.
func (s *Server) createToken(w http.ResponseWriter, r *http.Request, u *models.User) {
	in := struct {
		Label string `json:"label"`
		Days  int    `json:"days"`
	}{}
	if r.ContentLength != 0 {
		if !decodeJSON(w, r, &in, 4096) {
			return
		}
	}
	if in.Days < 0 {
		writeErr(w, http.StatusBadRequest, "срок не бывает отрицательным")
		return
	}
	if in.Days > tokenMaxDays {
		writeErr(w, http.StatusBadRequest, "слишком долгий срок — выберите бессрочный ключ")
		return
	}

	tok, hash, err := auth.NewToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог выписать ключ")
		return
	}
	key := models.APIToken{
		TokenHash: hash,
		UserID:    u.ID,
		Label:     strings.TrimSpace(in.Label),
	}
	if in.Days > 0 {
		exp := time.Now().Add(time.Duration(in.Days) * 24 * time.Hour)
		key.ExpiresAt = &exp
	}
	if err := s.db.Create(&key).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог выписать ключ")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         key.ID,
		"label":      key.Label,
		"expires_at": key.ExpiresAt,
		"token":      tok, // показывается один раз: в базе только хеш
	})
}

// listTokens — GET /api/tokens: свои ключи, свежие сверху.
func (s *Server) listTokens(w http.ResponseWriter, r *http.Request, u *models.User) {
	var rows []models.APIToken
	if err := s.db.Where("user_id = ?", u.ID).Order("id DESC").Limit(200).Find(&rows).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}
	now := time.Now()
	out := make([]tokenView, 0, len(rows))
	for i := range rows {
		state := "active"
		if rows[i].Expired(now) {
			state = "expired"
		}
		out = append(out, tokenView{
			ID:         rows[i].ID,
			Label:      rows[i].Label,
			State:      state,
			CreatedAt:  rows[i].CreatedAt,
			ExpiresAt:  rows[i].ExpiresAt,
			LastUsedAt: rows[i].LastUsedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

// deleteToken — DELETE /api/tokens/{id}: отозвать свой ключ.
//
// Чужой ключ по этому адресу не найти: выборка идёт по user_id, поэтому
// чужой id отвечает 404, а не 403 — существование чужих ключей не подтверждаем.
func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request, u *models.User) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "не понял, какой ключ отозвать")
		return
	}
	res := s.db.Where("id = ? AND user_id = ?", uint(id), u.ID).Delete(&models.APIToken{})
	if res.Error != nil {
		writeErr(w, http.StatusInternalServerError, "не смог отозвать ключ")
		return
	}
	if res.RowsAffected == 0 {
		writeErr(w, http.StatusNotFound, "такого ключа нет")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
