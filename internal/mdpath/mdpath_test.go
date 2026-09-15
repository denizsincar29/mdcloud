package mdpath

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		in   string
		want string
		bad  bool
	}{
		{in: "ДЗ по ИИ", bad: true}, // пробел в имени — %20 в адресе
		{in: "ДЗ-по-ИИ/задачи", want: "ДЗ-по-ИИ/задачи"},
		{in: "  /ДЗ/ИИ/задачи.md  ", want: "ДЗ/ИИ/задачи.md"},
		{in: "a//b", want: "a/b"},    // пустые сегменты выбрасываем
		{in: "./a/./b", want: "a/b"}, // точки-заполнители тоже
		{in: "../../etc/passwd", bad: true},
		{in: "a/../b", bad: true},
		{in: "", bad: true},
		{in: "/", bad: true},
		{in: "a/b/c/d/e/f", bad: true}, // глубже пяти папок не пускаем
		{in: "a/b/c/d/e", want: "a/b/c/d/e"},
		{in: ".hidden", bad: true},
		{in: "имя+файл(1)", want: "имя+файл(1)"},
	}
	for _, c := range cases {
		got, err := Normalize(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("Normalize(%q) = %q, ожидалась ошибка", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Normalize(%q): неожиданная ошибка %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Normalize(%q) = %q, хотели %q", c.in, got, c.want)
		}
	}
}

func TestValidUsername(t *testing.T) {
	for _, ok := range []string{"deniz", "va_sya-1", "abc"} {
		if !ValidUsername(ok) {
			t.Errorf("%q должен быть допустим", ok)
		}
	}
	for _, bad := range []string{"", "ab", "Дениз", "Deniz", "de niz", "deniz!", "оченьдлинноеимякоторогонебываеттакого"} {
		if ValidUsername(bad) {
			t.Errorf("%q должен быть отвергнут", bad)
		}
	}
}
