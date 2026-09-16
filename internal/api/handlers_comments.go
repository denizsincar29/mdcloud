package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/denizsincar29/mdcloud/internal/auth"
	"github.com/denizsincar29/mdcloud/internal/models"
)

const maxCommentRunes = 4000

type commentView struct {
	ID          uint      `json:"id"`
	AuthorName  string    `json:"author_name"`
	AuthorID    *uint     `json:"author_id,omitempty"`
	AuthorLogin string    `json:"author_login,omitempty"`
	Body        string    `json:"body"`
	CreatedAt   time.Time `json:"created_at"`
	Mine        bool      `json:"mine"`      // можно удалить (автор или хозяин документа)
	Anonymous   bool      `json:"anonymous"` // комментарий без входа
}

func commentJSON(c *models.Comment, viewer *models.User, docOwnerID uint) commentView {
	v := commentView{
		ID:        c.ID,
		AuthorID:  c.AuthorID,
		Body:      c.Body,
		CreatedAt: c.CreatedAt,
		Anonymous: c.AuthorID == nil,
	}
	switch {
	case c.AuthorID == nil:
		v.AuthorName = c.Name
	case c.Author != nil:
		v.AuthorName = c.Author.Name()
		v.AuthorLogin = c.Author.Username
	default:
		v.AuthorName = c.Name
	}
	if viewer != nil {
		v.Mine = viewer.ID == docOwnerID || (c.AuthorID != nil && *c.AuthorID == viewer.ID)
	}
	return v
}

func (s *Server) listComments(w http.ResponseWriter, r *http.Request) {
	viewer := s.authenticate(r)
	doc, owner, ok := s.docForRequest(w, r, viewer)
	if !ok {
		return
	}
	var comments []models.Comment
	err := s.db.Preload("Author").Where("doc_id = ?", doc.ID).Order("created_at").Find(&comments).Error
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}
	out := make([]commentView, 0, len(comments))
	for i := range comments {
		out = append(out, commentJSON(&comments[i], viewer, owner.ID))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"comments":             out,
		"comments_on":          doc.CommentsOn,
		"require_auth":         doc.CommentsRequireAuth,
		"can_comment":          doc.CommentsOn && (!doc.CommentsRequireAuth || viewer != nil),
		"viewer_authenticated": viewer != nil,
	})
}

func (s *Server) postComment(w http.ResponseWriter, r *http.Request) {
	viewer := s.authenticate(r)
	doc, owner, ok := s.docForRequest(w, r, viewer)
	if !ok {
		return
	}
	if !doc.CommentsOn {
		writeErr(w, http.StatusForbidden, "комментарии к этому документу выключены")
		return
	}
	if doc.CommentsRequireAuth && viewer == nil {
		writeErr(w, http.StatusUnauthorized, "здесь можно комментировать только после входа")
		return
	}

	var in struct {
		Body string `json:"body"`
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &in, 16<<10) {
		return
	}
	body := strings.TrimSpace(in.Body)
	if body == "" {
		writeErr(w, http.StatusBadRequest, "пустой комментарий")
		return
	}
	if len([]rune(body)) > maxCommentRunes {
		writeErr(w, http.StatusRequestEntityTooLarge, "комментарий слишком длинный")
		return
	}

	ipHash := auth.HashIP(s.cfg.IPSalt, clientIP(r))
	if !s.lim.allow("comment:"+ipHash, s.cfg.CommentLimit, s.cfg.CommentWindow) {
		retryAfter(w, s.cfg.CommentWindow)
		writeErr(w, http.StatusTooManyRequests, "слишком часто — подождите немного")
		return
	}

	c := models.Comment{DocID: doc.ID, Body: body, IPHash: ipHash}
	if viewer != nil {
		id := viewer.ID
		c.AuthorID = &id
		c.Name = viewer.Name()
	} else {
		name := strings.TrimSpace(in.Name)
		if name == "" {
			writeErr(w, http.StatusBadRequest, "представьтесь, пожалуйста")
			return
		}
		if len([]rune(name)) > 80 {
			writeErr(w, http.StatusBadRequest, "имя длиннее 80 символов")
			return
		}
		c.Name = name
	}
	if err := s.db.Create(&c).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог сохранить комментарий")
		return
	}
	c.Author = viewer
	writeJSON(w, http.StatusCreated, commentJSON(&c, viewer, owner.ID))
}

func (s *Server) deleteComment(w http.ResponseWriter, r *http.Request, u *models.User) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "непонятный номер комментария")
		return
	}
	var c models.Comment
	if err := s.db.First(&c, uint(id)).Error; err != nil {
		writeErr(w, http.StatusNotFound, "нет такого комментария")
		return
	}
	var doc models.Doc
	if err := s.db.First(&doc, c.DocID).Error; err != nil {
		writeErr(w, http.StatusNotFound, "документ комментария уже удалён")
		return
	}
	// Удалять может автор комментария и хозяин документа — и только они.
	isAuthor := c.AuthorID != nil && *c.AuthorID == u.ID
	if !isAuthor && doc.OwnerID != u.ID {
		writeErr(w, http.StatusForbidden, "это не ваш комментарий")
		return
	}
	if err := s.db.Delete(&c).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог удалить комментарий")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
