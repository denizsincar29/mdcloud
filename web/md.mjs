// md.mjs — подготовка markdown к показу: то, что нужно сделать до showdown и
// после него. Отдельным модулем, чтобы проверялось тестом без браузера
// (web/test/md.test.mjs): в app.js всё завязано на живую страницу.
//
// Показ облачного документа повторяет предпросмотр редактора mathmd — те же
// маркеры, те же правила, — чтобы облако и редактор показывали одну и ту же
// мдшку одинаково.

// AsciiMath пишется обратными кавычками (`sqrt(2)`), а showdown превратил бы
// их в <code>, и MathJax формулы не увидел бы. Меняем кавычки на маркеры до
// конвертации и возвращаем их уже в готовом HTML. Маркеры — как в mathmd.
export const ASM_OPEN = "⁣¶ASMOPEN¶⁣";
export const ASM_CLOSE = "⁣¶ASMCLOSE¶⁣";

// stripFrontmatter убирает блок настроек документа в начале файла:
//   ---
//   title: Морфи
//   lang: ru
//   ---
// В облаке он не настройки, а мусор: заголовок и язык учтены при сохранении,
// а читателю незачем видеть «title:» первой строкой. Незакрытый блок — не
// frontmatter, а обычный текст: тогда документ остаётся как есть.
export function stripFrontmatter(md) {
  const text = md || "";
  if (!/^---\r?\n/.test(text)) return text;
  const lines = text.split(/\r?\n/);
  for (let i = 1; i < lines.length; i++) {
    if (/^\s*---\s*$/.test(lines[i])) return lines.slice(i + 1).join("\n");
  }
  return text;
}

// protectAsciiMath прячет содержимое одиночных кавычек. Соседние кавычки не
// трогаем нарочно: ограждение блока кода и двойные кавычки markdown — не
// формула, и гадать, где внутри них кончается код, хуже, чем оставить строку
// как есть.
export function protectAsciiMath(md) {
  return (md || "").replace(/(^|[^`])`([^`\n]+)`([^`]|$)/g,
    (all, before, expr, after) => before + ASM_OPEN + expr + ASM_CLOSE + after);
}

// restoreAsciiMath возвращает кавычки в готовом HTML — MathJax понимает их как
// делимитер AsciiMath.
export function restoreAsciiMath(html) {
  return (html || "").split(ASM_OPEN).join("`").split(ASM_CLOSE).join("`");
}

// markdownToHtml — весь путь мдшки: снять frontmatter, уберечь кавычки,
// отдать showdown, вернуть кавычки. Конвертер приходит снаружи: showdown
// грузится с CDN в браузере и в тесте подменяется заглушкой.
export function markdownToHtml(markdown, converter) {
  return restoreAsciiMath(converter.makeHtml(protectAsciiMath(stripFrontmatter(markdown))));
}
