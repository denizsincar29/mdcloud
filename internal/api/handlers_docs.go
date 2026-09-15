package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/denizsincar29/mdcloud/internal/mdpath"
	"github.com/denizsincar29/mdcloud/internal/models"
)

// docView — документ в том виде, в каком его видит браузер.
// Content отдаётся только там, где он реально нужен (просмотр и правка):
// список документов не должен весить мегабайты.
type docView struct {
	Owner               string    `json:"owner"`
	Path                string    `json:"path"`
	Title               string    `json:"title"`
	Content             string    `json:"content,omitempty"`
	Visibility          string    `json:"visibility"`
	CommentsOn          bool      `json:"comments_on"`
	CommentsRequireAuth bool      `json:"comments_require_auth"`
	CanEdit             bool      `json:"can_edit"`
	URL                 string    `json:"url"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

func (s *Server) viewDoc(d *models.Doc, ownerName string, viewer *models.User, withContent bool) docView {
	v := docView{
		Owner:               ownerName,
		Path:                d.Path,
		Title:               d.Title,
		Visibility:          d.Visibility,
		CommentsOn:          d.CommentsOn,
		CommentsRequireAuth: d.CommentsRequireAuth,
		CanEdit:             viewer != nil && viewer.ID == d.OwnerID,
		URL:                 s.cfg.BaseURL + "/" + ownerName + "/" + d.Path,
		CreatedAt:           d.CreatedAt,
		UpdatedAt:           d.UpdatedAt,
	}
	if withContent {
		v.Content = d.Content
	}
	return v
}

func (s *Server) findUser(username string) (*models.User, bool) {
	var u models.User
	err := s.db.Where("username = ?", mdpath.CanonicalUsername(username)).First(&u).Error
	return &u, err == nil
}

// docForRequest достаёт документ по адресу из URL и проверяет право на
// просмотр. Отдельно возвращает владельца: он нужен и для подписи, и для
// проверки прав. Приватный документ отвечает 404 — существование чужих
// закрытых записей не подтверждаем.
func (s *Server) docForRequest(w http.ResponseWriter, r *http.Request, viewer *models.User) (*models.Doc, *models.User, bool) {
	owner, ok := s.findUser(r.PathValue("owner"))
	if !ok {
		writeErr(w, http.StatusNotFound, "нет такого пользователя")
		return nil, nil, false
	}
	path, err := mdpath.Normalize(r.PathValue("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return nil, nil, false
	}
	var doc models.Doc
	err = s.db.Where("owner_id = ? AND path = ?", owner.ID, path).First(&doc).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return nil, nil, false
	}
	if errors.Is(err, gorm.ErrRecordNotFound) ||
		(!doc.IsPublic() && (viewer == nil || viewer.ID != owner.ID)) {
		writeErr(w, http.StatusNotFound, "нет такого документа")
		return nil, nil, false
	}
	return &doc, owner, true
}

// ---------------------------------------------------------------- чтение

// listMine — все свои документы, включая закрытые.
func (s *Server) listMine(w http.ResponseWriter, r *http.Request, u *models.User) {
	s.respondDocList(w, u, u)
}

// listByOwner — публичный список документов пользователя.
func (s *Server) listByOwner(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.findUser(r.PathValue("owner"))
	if !ok {
		writeErr(w, http.StatusNotFound, "нет такого пользователя")
		return
	}
	s.respondDocList(w, owner, s.authenticate(r))
}

func (s *Server) respondDocList(w http.ResponseWriter, owner, viewer *models.User) {
	q := s.db.Where("owner_id = ?", owner.ID)
	if viewer == nil || viewer.ID != owner.ID {
		q = q.Where("visibility = ?", models.VisPublic)
	}
	var docs []models.Doc
	if err := q.Order("path").Find(&docs).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}
	out := make([]docView, 0, len(docs))
	for i := range docs {
		out = append(out, s.viewDoc(&docs[i], owner.Username, viewer, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"owner": owner.Username, "docs": out})
}

func (s *Server) getDoc(w http.ResponseWriter, r *http.Request) {
	viewer := s.authenticate(r)
	doc, owner, ok := s.docForRequest(w, r, viewer)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.viewDoc(doc, owner.Username, viewer, true))
}

// ---------------------------------------------------------------- запись

// docInput — что можно приложить к документу при записи. Всё указателями:
// «не прислали» и «прислали пустое» — разные вещи, а PUT обновляет только то,
// что пришло в теле.
type docInput struct {
	Path                *string `json:"path"`
	Title               *string `json:"title"`
	Content             *string `json:"content"`
	Visibility          *string `json:"visibility"`
	Public              *bool   `json:"public"` // короткая форма visibility
	CommentsOn          *bool   `json:"comments_on"`
	CommentsRequireAuth *bool   `json:"comments_require_auth"`
}

// visibility разрешает обе формы: «visibility»: «public» и «public»: true.
// Если пришли обе и они противоречат друг другу — это ошибка в запросе, а не
// повод выбрать одну наугад.
func (in *docInput) visibility() (string, bool) {
	byName := ""
	if in.Visibility != nil {
		byName = strings.TrimSpace(*in.Visibility)
		switch byName {
		case "", models.VisPublic, models.VisPrivate:
		default:
			return "", false
		}
	}
	byFlag := ""
	if in.Public != nil {
		if *in.Public {
			byFlag = models.VisPublic
		} else {
			byFlag = models.VisPrivate
		}
	}
	if byName != "" && byFlag != "" && byName != byFlag {
		return "", false
	}
	if byName != "" {
		return byName, true
	}
	return byFlag, true // пусто — «как было» либо значение по умолчанию
}

// postDoc — POST /api/docs: сохранить документ в свой аккаунт.
//
// Путь приходит в теле, а не в адресе: такому запросу не нужно знать имя
// владельца — оно и есть тот, чей ключ пришёл. Существующий документ
// обновляется (200), новый заводится (201) — для скрипта разницы нет, но
// ответ говорит, что именно произошло.
func (s *Server) postDoc(w http.ResponseWriter, r *http.Request, u *models.User) {
	var in docInput
	if !decodeJSON(w, r, &in, int64(s.cfg.MaxDocBytes)+8192) {
		return
	}
	if in.Path == nil || strings.TrimSpace(*in.Path) == "" {
		writeErr(w, http.StatusBadRequest, "не указан путь документа")
		return
	}
	path, err := mdpath.Normalize(*in.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.saveDoc(w, u, u, path, &in)
}

func (s *Server) putDoc(w http.ResponseWriter, r *http.Request, u *models.User) {
	owner, ok := s.findUser(r.PathValue("owner"))
	if !ok {
		writeErr(w, http.StatusNotFound, "нет такого пользователя")
		return
	}
	path, err := mdpath.Normalize(r.PathValue("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var in docInput
	if !decodeJSON(w, r, &in, int64(s.cfg.MaxDocBytes)+8192) {
		return
	}
	s.saveDoc(w, u, owner, path, &in)
}

// saveDoc — общий путь записи: проверки, создание или обновление, ответ. Им
// пользуются и PUT по адресу, и POST с путём в теле.
func (s *Server) saveDoc(w http.ResponseWriter, u, owner *models.User, path string, in *docInput) {
	if owner.ID != u.ID {
		writeErr(w, http.StatusForbidden, "править можно только свои документы")
		return
	}
	if in.Content != nil && len(*in.Content) > s.cfg.MaxDocBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "документ больше допустимого размера")
		return
	}
	visibility, ok := in.visibility()
	if !ok {
		writeErr(w, http.StatusBadRequest, "видимость бывает только public или private")
		return
	}
	if in.Title != nil && len([]rune(*in.Title)) > 255 {
		writeErr(w, http.StatusBadRequest, "заголовок длиннее 255 символов")
		return
	}

	var doc models.Doc
	err := s.db.Where("owner_id = ? AND path = ?", owner.ID, path).First(&doc).Error
	created := false
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		created = true
		doc = models.Doc{
			OwnerID:    owner.ID,
			Path:       path,
			Title:      defaultTitle(path),
			Visibility: models.VisPrivate, // по умолчанию всё закрыто
			CommentsOn: true,
		}
		if in.Content != nil {
			doc.Content = *in.Content
		}
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	default:
		if in.Content != nil {
			doc.Content = *in.Content
		}
	}

	if in.Title != nil {
		doc.Title = strings.TrimSpace(*in.Title)
	}
	if visibility != "" {
		doc.Visibility = visibility
	}
	if in.CommentsOn != nil {
		doc.CommentsOn = *in.CommentsOn
	}
	if in.CommentsRequireAuth != nil {
		doc.CommentsRequireAuth = *in.CommentsRequireAuth
	}

	if err := s.db.Save(&doc).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог сохранить документ")
		return
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	writeJSON(w, code, s.viewDoc(&doc, owner.Username, u, true))
}

func (s *Server) deleteDoc(w http.ResponseWriter, r *http.Request, u *models.User) {
	owner, ok := s.findUser(r.PathValue("owner"))
	if !ok {
		writeErr(w, http.StatusNotFound, "нет такого пользователя")
		return
	}
	if owner.ID != u.ID {
		writeErr(w, http.StatusForbidden, "удалять можно только свои документы")
		return
	}
	path, err := mdpath.Normalize(r.PathValue("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res := s.db.Where("owner_id = ? AND path = ?", owner.ID, path).Delete(&models.Doc{})
	if res.Error != nil {
		writeErr(w, http.StatusInternalServerError, "не смог удалить документ")
		return
	}
	if res.RowsAffected == 0 {
		writeErr(w, http.StatusNotFound, "нет такого документа")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": path})
}

// defaultTitle — заголовок из имени файла, если своего не дали.
func defaultTitle(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		path = path[i+1:]
	}
	return strings.TrimSuffix(path, ".md")
}
