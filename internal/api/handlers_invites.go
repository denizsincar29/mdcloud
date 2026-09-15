package api

// Приглашения в облако.
//
// Регистрация закрыта для всех, кроме хозяина: аккаунт заводится по коду,
// который он выписал. Кода нет в базе — только его хеш, поэтому показать
// приглашение повторно нельзя; список хранит, кому и когда выдали и кто
// по нему пришёл.

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/denizsincar29/mdcloud/internal/auth"
	"github.com/denizsincar29/mdcloud/internal/models"
)

// inviteView — приглашение наружу. Кода здесь нет и быть не может.
type inviteView struct {
	ID        uint       `json:"id"`
	Note      string     `json:"note"`
	State     string     `json:"state"` // active | used | expired
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
	UsedBy    string     `json:"used_by,omitempty"` // имя того, кто пришёл
}

func inviteState(i *models.Invite, now time.Time) string {
	switch {
	case i.Spent():
		return "used"
	case i.Expired(now):
		return "expired"
	default:
		return "active"
	}
}

// createInvite — POST /api/invites {"note": "Маше", "days": 14}
//
// Тело можно не присылать вовсе: тогда срок берётся из настроек.
func (s *Server) createInvite(w http.ResponseWriter, r *http.Request, u *models.User) {
	in := struct {
		Note string `json:"note"`
		Days int    `json:"days"`
	}{}
	if r.ContentLength != 0 {
		if !decodeJSON(w, r, &in, 4096) {
			return
		}
	}
	ttl := s.cfg.InviteTTL
	if in.Days > 0 {
		if in.Days > 365 {
			in.Days = 365
		}
		ttl = time.Duration(in.Days) * 24 * time.Hour
	}

	code, hash, err := auth.NewCode()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог выписать приглашение")
		return
	}
	inv := models.Invite{
		CodeHash:  hash,
		Note:      strings.TrimSpace(in.Note),
		CreatedBy: u.ID,
		ExpiresAt: time.Now().Add(ttl),
	}
	if err := s.db.Create(&inv).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог выписать приглашение")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         inv.ID,
		"note":       inv.Note,
		"code":       code, // показывается один раз: в базе только хеш
		"expires_at": inv.ExpiresAt,
		// Код едет во фрагменте адреса: на сервер он не попадёт и в логах
		// Caddy не осядет.
		"url": s.cfg.BaseURL + "/#invite=" + code,
	})
}

// listInvites — GET /api/invites: и живые, и потраченные, свежие сверху.
func (s *Server) listInvites(w http.ResponseWriter, r *http.Request, u *models.User) {
	var rows []models.Invite
	if err := s.db.Order("id DESC").Limit(200).Find(&rows).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}

	// Имена пришедших — одним запросом, а не по строке на приглашение.
	ids := make([]uint, 0, len(rows))
	for i := range rows {
		if rows[i].UsedBy != nil {
			ids = append(ids, *rows[i].UsedBy)
		}
	}
	names := map[uint]string{}
	if len(ids) > 0 {
		var users []models.User
		s.db.Where("id IN ?", ids).Find(&users)
		for i := range users {
			names[users[i].ID] = users[i].Name()
		}
	}

	now := time.Now()
	out := make([]inviteView, 0, len(rows))
	for i := range rows {
		v := inviteView{
			ID:        rows[i].ID,
			Note:      rows[i].Note,
			State:     inviteState(&rows[i], now),
			CreatedAt: rows[i].CreatedAt,
			ExpiresAt: rows[i].ExpiresAt,
			UsedAt:    rows[i].UsedAt,
		}
		if rows[i].UsedBy != nil {
			v.UsedBy = names[*rows[i].UsedBy]
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"invites": out})
}

// deleteInvite — DELETE /api/invites/{id}: отозвать неиспользованное.
func (s *Server) deleteInvite(w http.ResponseWriter, r *http.Request, u *models.User) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "не понял, какое приглашение отозвать")
		return
	}
	var inv models.Invite
	if err := s.db.First(&inv, uint(id)).Error; err != nil {
		writeErr(w, http.StatusNotFound, "такого приглашения нет")
		return
	}
	// Потраченное не трогаем: это уже история — кто-то по нему пришёл,
	// и стирать след только потому, что кнопка рядом, не надо.
	if inv.Spent() {
		writeErr(w, http.StatusConflict, "по этому приглашению уже зарегистрировались")
		return
	}
	if err := s.db.Delete(&models.Invite{}, inv.ID).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог отозвать приглашение")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
