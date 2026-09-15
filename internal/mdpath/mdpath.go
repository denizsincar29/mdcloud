// Package mdpath — правила адресов в облаке.
//
// Адрес документа — это <username>/<sub>/<folder>/<name>, и он же ключ в БД.
// Поэтому нормализация строгая: один и тот же документ не должен уметь
// притвориться двумя разными строками.
package mdpath

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxSegments — сколько сегментов допускаем после имени владельца.
const MaxSegments = 5

// MaxLen — предел длины адреса целиком.
const MaxLen = 200

// Ошибки валидации — на русском: их видит человек в интерфейсе.
var (
	ErrEmpty     = errors.New("пустой адрес документа")
	ErrTooDeep   = fmt.Errorf("слишком глубоко: не больше %d вложенных папок", MaxSegments)
	ErrTooLong   = fmt.Errorf("адрес длиннее %d символов", MaxLen)
	ErrTraversal = errors.New("сегмент «..» запрещён")
	ErrBadSeg    = errors.New("в имени папки или файла допустимы буквы, цифры, точка, дефис и подчёркивание")
)

// Normalize приводит адрес к каноническому виду.
//
//	"  /ДЗ/ИИ/Задачи.md " -> "ДЗ/ИИ/Задачи.md"
//
// Возвращает ошибку, если адрес невалиден: пустые сегменты выбрасываются,
// «..» и точки-заполнители отбрасываются с ошибкой, пробелы в сегменте
// запрещены (пробел в URL — это %20, такое имя потом не набрать руками).
func Normalize(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrEmpty
	}
	var segs []string
	for _, part := range strings.Split(raw, "/") {
		part = strings.TrimSpace(part)
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return "", ErrTraversal
		}
		if !validSegment(part) {
			return "", fmt.Errorf("%w: «%s»", ErrBadSeg, part)
		}
		segs = append(segs, part)
	}
	if len(segs) == 0 {
		return "", ErrEmpty
	}
	if len(segs) > MaxSegments {
		return "", ErrTooDeep
	}
	out := strings.Join(segs, "/")
	if len([]rune(out)) > MaxLen {
		return "", ErrTooLong
	}
	return out, nil
}

// validSegment проверяет один сегмент адреса.
func validSegment(s string) bool {
	if s == "" || len([]rune(s)) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
		case r == '-', r == '_', r == '.', r == '+', r == '(', r == ')':
		default:
			return false
		}
	}
	// Сегмент не может начинаться с точки: «.» и «..» — служебные имена,
	// а скрытые файлы в облаке ни к чему.
	return !strings.HasPrefix(s, ".")
}

// ValidUsername проверяет имя владельца: 3–32 символа, латиница в нижнем
// регистре, цифры, дефис и подчёркивание. Имя попадает в URL, поэтому
// кириллицу здесь не пускаем — она уходит в путь документа.
func ValidUsername(s string) bool {
	if len(s) < 3 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// CanonicalUsername приводит имя к нижнему регистру.
func CanonicalUsername(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// translitTable — русские буквы латиницей. Таблица намеренно простая и
// читаемая глазом, а не по ГОСТу: «ё» и «э» дают «e», «ь» и «ъ» пропадают,
// потому что адрес нужен для ссылки в переписке, а не для паспорта.
var translitTable = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "h", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
	// украинские и белорусские — на случай документа от гостя
	'і': "i", 'ї': "i", 'є': "e", 'ґ': "g", 'ў': "u",
}

// Slug — адрес документа в том виде, в каком он попадает в ссылку.
//
//	"ДЗ/ИИ/задачи" -> "dz/ii/zadachi"
//
// Ссылку диктуют голосом, вставляют в письмо и читают с экрана, поэтому
// кириллица в ней превращается в «%D0%94%D0%97» и перестаёт быть адресом.
// Латиница тут — представление, а не хранилище: сам путь в базе остаётся
// таким, каким его написал человек, и открывается по-прежнему.
//
// Регистр теряется: «ДЗ» и «дз» — это одна и та же ссылка, и завести два
// документа, различимых только регистром, не выйдет.
func Slug(path string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if t := translitSegment(s); t != "" {
			segs[i] = t
		}
	}
	return strings.Join(segs, "/")
}

// translitSegment переводит один сегмент адреса. Пустая строка в ответе —
// «здесь транслит не вышел»: либо сегмент состоял из одних «ь» и «ъ», либо
// в нём буква, которой нет в таблице. Тогда сегмент остаётся как есть, и
// адрес перестаёт быть красивым, но не перестаёт работать.
func translitSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < utf8.RuneSelf {
			b.WriteRune(unicode.ToLower(r)) // цифры, дефисы, латиница — как есть
			continue
		}
		t, ok := translitTable[unicode.ToLower(r)]
		if !ok {
			return ""
		}
		b.WriteString(t)
	}
	return b.String()
}
