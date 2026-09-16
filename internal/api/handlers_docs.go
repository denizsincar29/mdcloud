package api

import (
	"errors"
	"fmt"
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
	Owner               string     `json:"owner"`
	Path                string     `json:"path"`
	Slug                string     `json:"slug"`
	Title               string     `json:"title"`
	Content             string     `json:"content,omitempty"`
	Visibility          string     `json:"visibility"`
	CommentsOn          bool       `json:"comments_on"`
	CommentsRequireAuth bool       `json:"comments_require_auth"`
	ExpiresAt           *time.Time `json:"expires_at,omitempty"`
	CanEdit             bool       `json:"can_edit"`
	// SharedWith — кому документ отправлен по юзернейму. Заполняется только
	// хозяину: получателю список остальных получателей не нужен.
	SharedWith []string  `json:"shared_with,omitempty"`
	URL        string    `json:"url"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (s *Server) viewDoc(d *models.Doc, ownerName string, viewer *models.User, withContent bool) docView {
	// Slug — то, как адрес выглядит в ссылке. Пустым он бывает только у
	// документа, до которого ещё не дошла уборка при старте, поэтому считаем
	// на месте: ссылка не должна зависеть от того, успела ли она пройти.
	slug := d.Slug
	if slug == "" {
		slug = mdpath.Slug(d.Path)
	}
	v := docView{
		Owner:               ownerName,
		Path:                d.Path,
		Slug:                slug,
		Title:               d.Title,
		Visibility:          d.Visibility,
		CommentsOn:          d.CommentsOn,
		CommentsRequireAuth: d.CommentsRequireAuth,
		ExpiresAt:           d.ExpiresAt,
		CanEdit:             viewer != nil && viewer.ID == d.OwnerID,
		URL:                 s.cfg.BaseURL + "/" + ownerName + "/" + slug,
		CreatedAt:           d.CreatedAt,
		UpdatedAt:           d.UpdatedAt,
	}
	if withContent {
		v.Content = d.Content
	}
	return v
}

// findDoc ищет документ по адресу из ссылки. Адрес приходит в двух видах:
// канонический путь, каким его набрал человек («ДЗ/ИИ»), и слаг — то, как он
// выглядит в ссылке («dz/ii»). Оба ведут к одному документу, поэтому старая
// кириллическая ссылка не ломается, когда появляется латинская.
func (s *Server) findDoc(ownerID uint, addr string) (*models.Doc, error) {
	var doc models.Doc
	err := s.db.Where("owner_id = ? AND (path = ? OR slug = ?)", ownerID, addr, mdpath.Slug(addr)).
		First(&doc).Error
	return &doc, err
}

// slugTaken отвечает, занят ли слаг другим документом того же владельца.
// Адрес в ссылке должен указывать ровно на один документ: если два разных
// пути дают одну ссылку («ДЗ» и «dz»), второй заводить нельзя.
func (s *Server) slugTaken(ownerID uint, slug, exceptPath string) (string, error) {
	var other models.Doc
	err := s.db.Where("owner_id = ? AND slug = ? AND path <> ?", ownerID, slug, exceptPath).
		First(&other).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return "", nil
	case err != nil:
		return "", err
	}
	return other.Path, nil
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
	doc, err := s.findDoc(owner.ID, path)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return nil, nil, false
	}
	// Приватный документ открыт хозяину и тому, кому его отправили; для
	// остальных его не существует. Публичный и «по ссылке» читает всякий, кто
	// знает адрес, — разница между ними в списке, а не здесь: сюда и тот, и
	// другой попадает по прямой ссылке.
	if errors.Is(err, gorm.ErrRecordNotFound) ||
		(!doc.IsOpen() && (viewer == nil ||
			(viewer.ID != owner.ID && !s.sharedWith(doc.ID, viewer.ID)))) {
		writeErr(w, http.StatusNotFound, "нет такого документа")
		return nil, nil, false
	}
	// Срок вышел — документа больше нет, даже для хозяина: он сам его на
	// срок и заводил. Подметатель сотрёт строку в ближайшие минуты.
	if doc.Expired(time.Now()) {
		writeErr(w, http.StatusNotFound, "срок документа вышел")
		return nil, nil, false
	}
	return doc, owner, true
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
		// Чужому в списке — только публичное. Документ «по ссылке» из списка
		// выпадает намеренно: его и заводят затем, чтобы он не попадался на
		// глаза, а открывался тому, кому адрес назвали.
		q = q.Where("visibility = ?", models.VisPublic)
	}
	// Документы с вышедшим сроком не показываем и хозяину: подметатель
	// доберётся до них в четверть часа, а до тех пор их не должно быть видно.
	q = q.Where("expires_at IS NULL OR expires_at > ?", time.Now())
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
	view := s.viewDoc(doc, owner.Username, viewer, true)
	// Хозяину виднее: он должен видеть, кому документ уже отправлен, иначе
	// отправка второй раз тому же человеку выглядит как потерянная.
	if viewer != nil && viewer.ID == owner.ID {
		names, err := s.shareNames(doc.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "база недоступна")
			return
		}
		view.SharedWith = names
	}
	writeJSON(w, http.StatusOK, view)
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
	// ExpiresInDays — документ на срок, в днях от сегодняшнего дня. 0 снимает
	// срок (документ остаётся насовсем), отрицательное число — ошибка.
	ExpiresInDays *int `json:"expires_in_days"`
}

// maxExpiryDays — предел срока. Дольше десяти лет «на время» уже не бывает,
// а опечатка в лишний ноль превратила бы вечность в вечность незаметно.
const maxExpiryDays = 3650

// visibility разрешает обе формы: «visibility»: «public» и «public»: true.
// Если пришли обе и они противоречат друг другу — это ошибка в запросе, а не
// повод выбрать одну наугад.
//
// Именованная форма знает три значения (public, link, private), флаг — только
// два: он остался от тех времён, когда режимов было два, и трогать его незачем
// — по нему пишут скрипты. Третий режим просят именем.
func (in *docInput) visibility() (string, bool) {
	byName := ""
	if in.Visibility != nil {
		byName = strings.TrimSpace(*in.Visibility)
		switch byName {
		case "", models.VisPublic, models.VisLink, models.VisPrivate:
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
	s.saveDoc(w, u, u, path, &in, http.StatusCreated)
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
	s.saveDoc(w, u, owner, path, &in, http.StatusOK)
}

// moveDoc переносит документ на другой адрес: PATCH с новым путём в теле.
//
// Переезд — это один UPDATE поля path, а не перезапись содержимого: строка
// документа остаётся той же, поэтому комментарии (они ссылаются на DocID)
// едут вместе с ним, и ничего не теряется по дороге. Отдельный метод, а не
// POST по новому адресу, ещё и потому, что POST по новому адресу затёр бы
// документ, который там уже лежит.
func (s *Server) moveDoc(w http.ResponseWriter, r *http.Request, u *models.User) {
	owner, ok := s.findUser(r.PathValue("owner"))
	if !ok {
		writeErr(w, http.StatusNotFound, "нет такого пользователя")
		return
	}
	from, err := mdpath.Normalize(r.PathValue("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var in struct {
		Path *string `json:"path"`
	}
	if !decodeJSON(w, r, &in, 8192) {
		return
	}
	if in.Path == nil || strings.TrimSpace(*in.Path) == "" {
		writeErr(w, http.StatusBadRequest, "не назван новый путь")
		return
	}
	to, err := mdpath.Normalize(*in.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	doc, err := s.findDoc(owner.ID, from)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		writeErr(w, http.StatusNotFound, "нет такого документа")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}
	if owner.ID != u.ID {
		writeErr(w, http.StatusForbidden, "переносить можно только свои документы")
		return
	}
	if to == doc.Path {
		writeJSON(w, http.StatusOK, s.viewDoc(doc, owner.Username, u, true))
		return
	}

	var taken int64
	if err := s.db.Model(&models.Doc{}).
		Where("owner_id = ? AND path = ?", owner.ID, to).Count(&taken).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}
	if taken > 0 {
		writeErr(w, http.StatusConflict, "по адресу "+to+" уже есть документ")
		return
	}
	if busy, err := s.slugTaken(owner.ID, mdpath.Slug(to), to); err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	} else if busy != "" {
		writeErr(w, http.StatusConflict,
			"новый адрес в ссылке выглядит как «"+mdpath.Slug(to)+"» — так уже называется "+busy)
		return
	}

	// Заголовок, оставшийся от старого имени (то есть не свой), едет с
	// документом: иначе после переименования файла в списке висело бы
	// прежнее имя, и завести своё человеку пришлось бы отдельно.
	if doc.Title == defaultTitle(doc.Path) {
		doc.Title = defaultTitle(to)
	}
	doc.Path = to
	doc.Slug = mdpath.Slug(to)
	if err := s.db.Save(doc).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог перенести документ")
		return
	}
	writeJSON(w, http.StatusOK, s.viewDoc(doc, owner.Username, u, true))
}

// saveDoc — общий путь записи: проверки, создание или обновление, ответ. Им
// пользуются и PUT по адресу, и POST с путём в теле.
//
// createdCode — что ответить, когда документа ещё не было. PUT всегда отвечает
// 200: он «положи сюда», адрес уже назван, и менять его ответ незачем — на нём
// висит редактор. POST отвечает 201: по коду видно, завёл клиент документ или
// переписал существующий.
func (s *Server) saveDoc(w http.ResponseWriter, u, owner *models.User, path string, in *docInput, createdCode int) {
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
		writeErr(w, http.StatusBadRequest, "видимость бывает public, link или private")
		return
	}
	if in.Title != nil && len([]rune(*in.Title)) > 255 {
		writeErr(w, http.StatusBadRequest, "заголовок длиннее 255 символов")
		return
	}
	if in.ExpiresInDays != nil {
		if *in.ExpiresInDays < 0 || *in.ExpiresInDays > maxExpiryDays {
			writeErr(w, http.StatusBadRequest,
				fmt.Sprintf("срок задаётся числом дней от 0 до %d", maxExpiryDays))
			return
		}
	}

	// Пишем по ссылке, а не по адресу: адрес мог прийти латиницей из ссылки
	// («dz/ii»), и по нему документ надо найти, а не завести второй.
	doc, err := s.findDoc(owner.ID, path)
	created := false
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		created = true
		doc = &models.Doc{
			OwnerID:    owner.ID,
			Path:       path,
			Slug:       mdpath.Slug(path),
			Title:      defaultTitle(path),
			Visibility: models.VisPrivate, // по умолчанию всё закрыто
			CommentsOn: true,
		}
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}

	// Свой слаг занят быть не может, чужой — может: тогда у двух документов
	// была бы одна ссылка, и вторая вела бы не туда.
	if busy, err := s.slugTaken(owner.ID, doc.Slug, doc.Path); err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	} else if busy != "" {
		writeErr(w, http.StatusConflict,
			"адрес "+doc.Path+" в ссылке выглядит как «"+doc.Slug+"» — так уже называется "+busy)
		return
	}

	if in.Content != nil {
		doc.Content = *in.Content
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
	// Срок отсчитывается от сегодняшнего дня: «удалить через 7 дней» — это то,
	// что человек и имеет в виду, а не «в 14:37 седьмого дня».
	if in.ExpiresInDays != nil {
		if *in.ExpiresInDays == 0 {
			doc.ExpiresAt = nil
		} else {
			until := time.Now().AddDate(0, 0, *in.ExpiresInDays)
			doc.ExpiresAt = &until
		}
	}

	if err := s.db.Save(doc).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог сохранить документ")
		return
	}
	code := http.StatusOK
	if created {
		code = createdCode
	}
	writeJSON(w, code, s.viewDoc(doc, owner.Username, u, true))
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
	// Удаляем по ссылке: адрес мог прийти и латиницей, и кириллицей.
	doc, err := s.findDoc(owner.ID, path)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeErr(w, http.StatusNotFound, "нет такого документа")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}
	if err := s.db.Delete(doc).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог удалить документ")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": doc.Path})
}

// defaultTitle — заголовок из имени файла, если своего не дали.
func defaultTitle(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		path = path[i+1:]
	}
	return strings.TrimSuffix(path, ".md")
}
