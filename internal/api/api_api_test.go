package api_test

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// Ключи аккаунта: выписать, увидеть в списке, сходить ими в API, отозвать.
// Ключ — это вход для скрипта: он живёт в заголовке Authorization и не должен
// давать ничего сверх того, что даёт вход браузером.
func TestAPITokens(t *testing.T) {
	srv := newTestServer(t)
	session := register(t, srv, "deniz", "parol1234")
	other := register(t, srv, "vasilisa", "parol1234")

	// Ключ со сроком и бессрочный: срок приходит в ответе, значение — один раз.
	st, body := req(t, srv, "POST", "/api/tokens", session,
		map[string]any{"label": "дайджест", "days": 30})
	if st != http.StatusCreated {
		t.Fatalf("выписать ключ: %d %v", st, body)
	}
	key, _ := body["token"].(string)
	if len(key) < 16 {
		t.Fatalf("ключ не выписан: %v", body)
	}
	if body["expires_at"] == nil || body["expires_at"] == "" {
		t.Errorf("у ключа на 30 дней нет срока: %v", body)
	}
	id := body["id"]

	st, body = req(t, srv, "POST", "/api/tokens", session, map[string]any{"label": "вечный"})
	if st != http.StatusCreated {
		t.Fatalf("выписать бессрочный ключ: %d %v", st, body)
	}
	forever, _ := body["token"].(string)
	if body["expires_at"] != nil {
		t.Errorf("бессрочный ключ получил срок: %v", body)
	}

	// Ключ работает как вход.
	st, me := req(t, srv, "GET", "/api/me", key, nil)
	if st != http.StatusOK || me["user"] == nil {
		t.Fatalf("ключ не пускает в API: %d %v", st, me)
	}
	if st, _ := req(t, srv, "GET", "/api/docs", forever, nil); st != http.StatusOK {
		t.Errorf("бессрочный ключ не пускает к документам: %d", st)
	}

	// В списке ключей самих ключей нет — только метки и сроки.
	st, list := req(t, srv, "GET", "/api/tokens", session, nil)
	if st != http.StatusOK {
		t.Fatalf("список ключей: %d %v", st, list)
	}
	tokens, _ := list["tokens"].([]any)
	if len(tokens) != 2 {
		t.Fatalf("в списке %d ключей, ждали 2: %v", len(tokens), list)
	}
	for _, item := range tokens {
		if row, ok := item.(map[string]any); ok && row["token"] != nil {
			t.Errorf("список отдал сам ключ: %v", row)
		}
	}

	// Сроки проверяем до отзыва: отрицательный и завышенный — ошибка запроса.
	if st, _ := req(t, srv, "POST", "/api/tokens", session, map[string]any{"days": -1}); st != http.StatusBadRequest {
		t.Errorf("отрицательный срок: %d, ждали 400", st)
	}
	if st, _ := req(t, srv, "POST", "/api/tokens", session, map[string]any{"days": 99999}); st != http.StatusBadRequest {
		t.Errorf("срок в 273 года: %d, ждали 400", st)
	}

	// Чужой ключ не отозвать: по чужому id отвечаем 404, а не 403 — existence
	// чужих ключей не подтверждаем.
	path := "/api/tokens/" + strconv.Itoa(int(id.(float64)))
	if st, _ := req(t, srv, "DELETE", path, other, nil); st != http.StatusNotFound {
		t.Errorf("отзыв чужого ключа: %d, ждали 404", st)
	}
	if st, _ := req(t, srv, "DELETE", path, session, nil); st != http.StatusOK {
		t.Errorf("отзыв своего ключа: %d, ждали 200", st)
	}
	// Отозванный ключ больше не пускает.
	if st, _ := req(t, srv, "GET", "/api/me", key, nil); st != http.StatusUnauthorized {
		t.Errorf("отозванный ключ всё ещё работает: %d", st)
	}
	// Без ключа и без куки — не пускает вовсе.
	if st, _ := req(t, srv, "GET", "/api/tokens", "", nil); st != http.StatusUnauthorized {
		t.Errorf("список ключей без входа: %d, ждали 401", st)
	}
}

// POST /api/docs — сохранить документ под своим аккаунтом, сразу решив,
// открыт он всем или закрыт. Тем же путём ходят ассистенты и пайплайны:
// владельца в теле нет, владелец — тот, чей ключ пришёл.
func TestPostDocSavesUnderAccount(t *testing.T) {
	srv := newTestServer(t)
	tok := register(t, srv, "deniz", "parol1234")

	// Владельца в теле не принимаем: его берём из ключа, а не из запроса.
	if st, body := req(t, srv, "POST", "/api/docs", tok,
		map[string]any{"path": "чужая/папка", "owner": "vasilisa"}); st != http.StatusBadRequest {
		t.Errorf("чужой владелец в теле: %d %v, ждали 400", st, body)
	}

	const docPath = "/api/docs/deniz/ДЗ/ИИ/задачи"
	st, doc := req(t, srv, "POST", "/api/docs", tok,
		map[string]any{"path": "ДЗ/ИИ/задачи", "content": "# Задачи\n", "public": true})
	if st != http.StatusCreated {
		t.Fatalf("создание документа: %d %v", st, doc)
	}
	if doc["visibility"] != "public" {
		t.Errorf("public: true не открыл документ: %v", doc)
	}
	if doc["owner"] != "deniz" || doc["path"] != "ДЗ/ИИ/задачи" {
		t.Errorf("документ лёг не туда: %v", doc)
	}
	if url, _ := doc["url"].(string); !strings.HasSuffix(url, "/deniz/ДЗ/ИИ/задачи") {
		t.Errorf("в ответе странный адрес: %v", doc["url"])
	}

	// Открытый документ виден анонимно, закрытый — нет.
	if st, _ := req(t, srv, "GET", "/api/docs/deniz/ДЗ/ИИ/задачи", "", nil); st != http.StatusOK {
		t.Errorf("аноним не видит публичный документ: %d", st)
	}
	st, closed := req(t, srv, "POST", "/api/docs", tok,
		map[string]any{"path": "ДЗ/черновик", "content": "тайное", "public": false})
	if st != http.StatusCreated {
		t.Fatalf("создание закрытого документа: %d %v", st, closed)
	}
	if closed["visibility"] != "private" {
		t.Errorf("public: false не закрыл документ: %v", closed)
	}
	if st, _ := req(t, srv, "GET", "/api/docs/deniz/ДЗ/черновик", "", nil); st != http.StatusNotFound {
		t.Errorf("аноним видит закрытый документ: %d", st)
	}
	// Владелец своим ключом его видит.
	if st, _ := req(t, srv, "GET", "/api/docs/deniz/ДЗ/черновик", tok, nil); st != http.StatusOK {
		t.Errorf("владелец не видит свой закрытый документ: %d", st)
	}

	// Повторный POST по тому же пути обновляет (200) и не меняет видимость,
	// если её не прислали.
	st, again := req(t, srv, "POST", "/api/docs", tok,
		map[string]any{"path": "ДЗ/черновик", "content": "тайное и новое"})
	if st != http.StatusOK {
		t.Fatalf("обновление документа: %d %v", st, again)
	}
	if again["visibility"] != "private" {
		t.Errorf("обновление сняло закрытость: %v", again)
	}
	if content, _ := again["content"].(string); content != "тайное и новое" {
		t.Errorf("содержимое не обновилось: %v", again["content"])
	}

	// Обе формы видимости — одно и то же, но противоречие в них — ошибка.
	st, doc = req(t, srv, "POST", "/api/docs", tok,
		map[string]any{"path": "ДЗ/ИИ/задачи", "visibility": "private"})
	if st != http.StatusOK || doc["visibility"] != "private" {
		t.Errorf("visibility: private не сработал: %d %v", st, doc)
	}
	if st, _ := req(t, srv, "POST", "/api/docs", tok,
		map[string]any{"path": "ДЗ/ИИ/задачи", "public": true, "visibility": "private"}); st != http.StatusBadRequest {
		t.Errorf("противоречие public и visibility: %d, ждали 400", st)
	}
	if st, _ := req(t, srv, "POST", "/api/docs", tok,
		map[string]any{"path": "ДЗ/ИИ/задачи", "visibility": "секретно"}); st != http.StatusBadRequest {
		t.Errorf("неизвестная видимость: %d, ждали 400", st)
	}

	// Путь обязателен: документу негде жить.
	if st, _ := req(t, srv, "POST", "/api/docs", tok, map[string]any{"content": "без пути"}); st != http.StatusBadRequest {
		t.Errorf("POST без пути: %d, ждали 400", st)
	}
	if st, _ := req(t, srv, "POST", "/api/docs", tok, map[string]any{"path": "../вверх"}); st != http.StatusBadRequest {
		t.Errorf("путь с выходом наверх: %d, ждали 400", st)
	}

	// Документ под своим аккаунтом, но правка чужого — по-прежнему под запретом.
	other := register(t, srv, "vasilisa", "parol1234")
	if st, _ := req(t, srv, "PUT", docPath, other, map[string]any{"content": "чужое"}); st != http.StatusForbidden {
		t.Errorf("правка чужого документа: %d, ждали 403", st)
	}
}

// Инструкция для ассистентов отдаётся без входа: тот, у кого ещё нет ключа,
// должен прочитать, как его выписать. Тело — markdown, поэтому читаем ответ
// сырым, а не через JSON-разбор.
func TestLLMGuide(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/api/llm.md")
	if err != nil {
		t.Fatalf("запрос инструкции: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("инструкция: %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("инструкция отдана как %q", ct)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("чтение инструкции: %v", err)
	}
	text := string(raw)
	// Инструкция бесполезна, если не называет ни способа входа, ни точки
	// сохранения — за это её и читают.
	for _, want := range []string{"Authorization: Bearer", "POST /api/docs", "/api/tokens"} {
		if !strings.Contains(text, want) {
			t.Errorf("в инструкции нет %q", want)
		}
	}
}
