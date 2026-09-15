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
  return splitFrontmatter(md).body;
}

// splitFrontmatter отдаёт и тело, и число снятых строк: номера строк в
// комментариях считаются по исходнику документа, вместе с настройками, — те же
// числа, что видит человек в редакторе.
export function splitFrontmatter(md) {
  const text = md || "";
  if (!/^---\r?\n/.test(text)) return { body: text, skipped: 0 };
  const lines = text.split(/\r?\n/);
  for (let i = 1; i < lines.length; i++) {
    if (/^\s*---\s*$/.test(lines[i])) return { body: lines.slice(i + 1).join("\n"), skipped: i + 1 };
  }
  return { body: text, skipped: 0 };
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

// --- разбиение на блоки ------------------------------------------------------
// Блоки нужны, чтобы на строку документа можно было встать и сослаться на неё
// из комментария. Разбиваем так же, как предпросмотр редактора mathmd: блок —
// абзац, список, цитата или ограждение целиком, с номером первой строки.

function lineClass(line) {
  if (/^\s*```/.test(line)) return "fence";
  if (/^\s*[-+*]\s+/.test(line)) return "bullet";
  if (/^\s*\d+[.)]\s+/.test(line)) return "ordered";
  if (/^\s*>\s?/.test(line)) return "quote";
  if (/^\s*#{1,6}\s+/.test(line)) return "heading";
  if (/^\s*(---+|\*\*\*+|___+)\s*$/.test(line)) return "hr";
  if (/^\s*\|.*\|\s*$/.test(line)) return "table";
  return "text";
}

function canContinue(seg, line) {
  if (line === "fence") return seg === "text";
  switch (seg) {
    case "heading":
    case "hr":
      return false;
    case "bullet":
      return line === "bullet" || line === "ordered" || line === "text";
    case "ordered":
      return line === "ordered" || line === "text";
    case "quote":
      return line !== "hr" && line !== "fence";
    default:
      return line === "text" || line === "table";
  }
}

// segmentMarkdown режет markdown на блоки: {line — номер первой строки, text}.
// Пробельные строки разделяют блоки, но ограждение не разрывают и список не
// рвут посреди пунктов.
export function segmentMarkdown(md) {
  const lines = (md || "").split("\n");
  const segments = [];
  let i = 0;
  while (i < lines.length) {
    while (i < lines.length && lines[i].trim() === "") i++;
    if (i >= lines.length) break;
    const start = i;
    const cls = lineClass(lines[i]);
    const buf = [lines[i]];
    i++;
    let inFence = cls === "fence";
    while (i < lines.length) {
      const line = lines[i];
      if (inFence) {
        buf.push(line);
        if (/^\s*```/.test(line)) inFence = false;
        i++;
        continue;
      }
      if (line.trim() === "") break;
      const lc = lineClass(line);
      if (canContinue(cls, lc)) {
        buf.push(line);
        if (lc === "fence") inFence = true;
        i++;
      } else {
        break;
      }
    }
    segments.push({ line: start + 1, text: buf.join("\n") });
  }
  return segments;
}

// markdownBlocks — весь путь мдшки по блокам: снять frontmatter, уберечь
// кавычки, разбить, отдать showdown, вернуть кавычки. Конвертер приходит
// снаружи: showdown грузится с CDN в браузере, в тесте — заглушкой.
export function markdownBlocks(markdown, converter) {
  const { body, skipped } = splitFrontmatter(markdown);
  const prepared = protectAsciiMath(body);
  return segmentMarkdown(prepared).map((seg) => ({
    line: skipped + seg.line,
    html: restoreAsciiMath(converter.makeHtml(seg.text)),
  }));
}

// markdownToHtml — тот же путь, но одной строкой HTML: удобно там, где номеров
// строк не нужно (экспорт, проверки).
export function markdownToHtml(markdown, converter) {
  return markdownBlocks(markdown, converter).map((block) => block.html).join("\n");
}
