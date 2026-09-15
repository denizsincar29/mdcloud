package api_test

import (
	"net/http"
	"testing"
)

// sharesOf достаёт список получателей из ответа на отправку или из документа.
func sharesOf(t *testing.T, body map[string]any) []string {
	t.Helper()
	raw, ok := body["shared_with"].([]any)
	if !ok {
		t.Fatalf("в ответе нет shared_with: %v", body)
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		out = append(out, v.(string))
	}
	return out
}

// TestShareDocWithUser — документ можно отправить человеку по юзернейму:
// адресат читает и комментирует, но не правит, а публичным документ не
// становится.
func TestShareDocWithUser(t *testing.T) {
	srv := newTestServer(t)
	deniz := register(t, srv, "deniz", "parol1234")
	vasya := register(t, srv, "vasilisa", "parol1234")
	stranger := register(t, srv, "kostya", "parol1234")

	const docPath = "/api/docs/deniz/ДЗ/ИИ/задачи"
	if st, doc := req(t, srv, "PUT", docPath, deniz, map[string]any{"content": "# Задачи"}); st != http.StatusOK {
		t.Fatalf("создание документа: %d %v", st, doc)
	}

	// Пока не отправлен — чужому и анониму документа нет.
	if st, _ := req(t, srv, "GET", docPath, vasya, nil); st != http.StatusNotFound {
		t.Fatalf("до отправки чужой видит документ: %d", st)
	}

	st, res := req(t, srv, "POST", "/api/share", deniz, map[string]any{
		"owner": "deniz", "path": "ДЗ/ИИ/задачи", "username": "vasilisa",
	})
	if st != http.StatusOK {
		t.Fatalf("отправка: %d %v", st, res)
	}
	if got := sharesOf(t, res); len(got) != 1 || got[0] != "vasilisa" {
		t.Errorf("получатели после отправки: %v", got)
	}

	// Адресат читает, но править не может.
	st, doc := req(t, srv, "GET", docPath, vasya, nil)
	if st != http.StatusOK {
		t.Fatalf("адресат не открыл отправленный документ: %d", st)
	}
	if doc["content"] != "# Задачи" {
		t.Errorf("содержимое не вернулось: %v", doc["content"])
	}
	if doc["visibility"] != "private" {
		t.Errorf("отправка не должна делать документ публичным: %v", doc["visibility"])
	}
	if doc["can_edit"] != false {
		t.Errorf("адресату правка не положена: %v", doc)
	}
	if st, _ := req(t, srv, "PUT", docPath, vasya, map[string]any{"content": "вандализм"}); st != http.StatusForbidden {
		t.Errorf("адресат правит документ: %d, ждали 403", st)
	}

	// Аноним и посторонний по-прежнему не видят: отправка — это не публикация.
	if st, _ := req(t, srv, "GET", docPath, "", nil); st != http.StatusNotFound {
		t.Errorf("аноним видит отправленный документ: %d", st)
	}
	if st, _ := req(t, srv, "GET", docPath, stranger, nil); st != http.StatusNotFound {
		t.Errorf("посторонний видит отправленный документ: %d", st)
	}
	// И в публичном списке хозяина его нет.
	if st, body := req(t, srv, "GET", "/api/docs/deniz", stranger, nil); st != http.StatusOK {
		t.Errorf("чужой список: %d", st)
	} else if docs, _ := body["docs"].([]any); len(docs) != 0 {
		t.Errorf("приватный документ виден в чужом списке: %v", docs)
	}

	// Хозяин видит, кому отправлено: иначе повторная отправка выглядит как
	// потерянная.
	if _, doc = req(t, srv, "GET", docPath, deniz, nil); len(sharesOf(t, doc)) != 1 {
		t.Errorf("хозяин не видит получателей: %v", doc)
	}

	// Повторная отправка тому же человеку ничего не добавляет.
	if st, res = req(t, srv, "POST", "/api/share", deniz, map[string]any{
		"owner": "deniz", "path": "ДЗ/ИИ/задачи", "username": "vasilisa",
	}); st != http.StatusOK || len(sharesOf(t, res)) != 1 {
		t.Errorf("повторная отправка: %d %v", st, res)
	}

	// Отзыв закрывает доступ.
	st, res = req(t, srv, "DELETE", "/api/share?owner=deniz&path=ДЗ/ИИ/задачи&username=vasilisa", deniz, nil)
	if st != http.StatusOK {
		t.Fatalf("отзыв: %d %v", st, res)
	}
	if got := sharesOf(t, res); len(got) != 0 {
		t.Errorf("получатели после отзыва: %v", got)
	}
	if st, _ := req(t, srv, "GET", docPath, vasya, nil); st != http.StatusNotFound {
		t.Errorf("после отзыва адресат всё ещё видит документ: %d", st)
	}
}

// TestSharedWithMe — присланное лежит отдельным списком: «мои документы» —
// это про владение и правку, а тут чужая работа, которую дали почитать.
func TestSharedWithMe(t *testing.T) {
	srv := newTestServer(t)
	deniz := register(t, srv, "deniz", "parol1234")
	vasya := register(t, srv, "vasilisa", "parol1234")

	const docPath = "/api/docs/deniz/ДЗ/ИИ/задачи"
	if st, _ := req(t, srv, "PUT", docPath, deniz, map[string]any{"content": "# Задачи"}); st != http.StatusOK {
		t.Fatalf("создание документа: %d", st)
	}
	if st, _ := req(t, srv, "POST", "/api/share", deniz, map[string]any{
		"owner": "deniz", "path": "ДЗ/ИИ/задачи", "username": "vasilisa",
	}); st != http.StatusOK {
		t.Fatalf("отправка: %d", st)
	}

	st, body := req(t, srv, "GET", "/api/shared", vasya, nil)
	if st != http.StatusOK {
		t.Fatalf("список присланного: %d", st)
	}
	docs, _ := body["docs"].([]any)
	if len(docs) != 1 {
		t.Fatalf("присланного должно быть одно: %v", docs)
	}
	first := docs[0].(map[string]any)
	if first["owner"] != "deniz" {
		t.Errorf("в присланном не тот хозяин: %v", first["owner"])
	}
	if first["can_edit"] != false {
		t.Errorf("присланное не правится: %v", first)
	}
	if _, ok := first["content"]; ok {
		t.Errorf("список не должен тащить содержимое: %v", first)
	}

	// У того, кому ничего не присылали, список пуст.
	if _, body = req(t, srv, "GET", "/api/shared", deniz, nil); len(body["docs"].([]any)) != 0 {
		t.Errorf("у хозяина список присланного не пуст: %v", body)
	}
}

// TestShareGuards — отправлять может только хозяин, и только существующему
// человеку: чужой документ не подтверждаем, себя не отправляем.
func TestShareGuards(t *testing.T) {
	srv := newTestServer(t)
	deniz := register(t, srv, "deniz", "parol1234")
	vasya := register(t, srv, "vasilisa", "parol1234")

	const docPath = "/api/docs/deniz/ДЗ/ИИ/задачи"
	if st, _ := req(t, srv, "PUT", docPath, deniz, map[string]any{"content": "# Задачи"}); st != http.StatusOK {
		t.Fatalf("создание документа: %d", st)
	}

	// Незнакомому юзернейму — не отправляем.
	if st, _ := req(t, srv, "POST", "/api/share", deniz, map[string]any{
		"owner": "deniz", "path": "ДЗ/ИИ/задачи", "username": "nikogo-tut-net",
	}); st != http.StatusNotFound {
		t.Errorf("отправка несуществующему: %d, ждали 404", st)
	}
	// Себе — не отправляем.
	if st, _ := req(t, srv, "POST", "/api/share", deniz, map[string]any{
		"owner": "deniz", "path": "ДЗ/ИИ/задачи", "username": "deniz",
	}); st != http.StatusBadRequest {
		t.Errorf("отправка себе: %d, ждали 400", st)
	}
	// Чужой документ не отправляем — и существование его не подтверждаем.
	if st, _ := req(t, srv, "POST", "/api/share", vasya, map[string]any{
		"owner": "deniz", "path": "ДЗ/ИИ/задачи", "username": "vasilisa",
	}); st != http.StatusNotFound {
		t.Errorf("отправка чужого документа: %d, ждали 404", st)
	}
	// И отозвать чужое тоже нельзя.
	if st, _ := req(t, srv, "DELETE", "/api/share?owner=deniz&path=ДЗ/ИИ/задачи&username=vasilisa", vasya, nil); st != http.StatusNotFound {
		t.Errorf("отзыв чужого документа: %d, ждали 404", st)
	}
	// Аноним не отправляет ничего: нужен вход.
	if st, _ := req(t, srv, "POST", "/api/share", "", map[string]any{
		"owner": "deniz", "path": "ДЗ/ИИ/задачи", "username": "vasilisa",
	}); st != http.StatusUnauthorized {
		t.Errorf("отправка без входа: %d, ждали 401", st)
	}
}
