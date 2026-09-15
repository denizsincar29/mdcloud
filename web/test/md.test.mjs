// Проверка правок мдшки до и после showdown (web/md.mjs) — без браузера и без
// CDN: showdown подменяем заглушкой, потому что проверяем не его, а то, что мы
// делаем с текстом до него и с его выводом после.
//
// Запуск: node web/test/md.test.mjs

import { createRequire } from "node:module";
import { execSync } from "node:child_process";
import path from "node:path";
import {
  ASM_OPEN, ASM_CLOSE, stripFrontmatter, protectAsciiMath, restoreAsciiMath,
  markdownBlocks, markdownToHtml, segmentMarkdown, splitFrontmatter,
} from "../md.mjs";

let failed = 0;
function ok(name, cond, extra) {
  console.log((cond ? "ok   " : "FAIL ") + name + (cond || extra === undefined ? "" : " — " + JSON.stringify(extra)));
  if (!cond) failed++;
}
const eq = (name, got, want) => ok(name, got === want, { got, want });
const eqDeep = (name, got, want) =>
  ok(name, JSON.stringify(got) === JSON.stringify(want), { got, want });

// --- frontmatter ------------------------------------------------------------
eq("frontmatter снят",
  stripFrontmatter("---\ntitle: Морфи\nlang: ru\n---\n\n# Заголовок"),
  "\n# Заголовок");

eq("без frontmatter текст не тронут",
  stripFrontmatter("# Заголовок\n\n---\n\nтекст"),
  "# Заголовок\n\n---\n\nтекст");

eq("незакрытый frontmatter — обычный текст",
  stripFrontmatter("---\ntitle: Морфи\n\nтекст"),
  "---\ntitle: Морфи\n\nтекст");

eq("--- не в первой строке — не frontmatter",
  stripFrontmatter("текст\n---\ntitle: Морфи\n---\n"),
  "текст\n---\ntitle: Морфи\n---\n");

eq("CRLF тоже понимаем",
  stripFrontmatter("---\r\ntitle: Морфи\r\n---\r\nтекст"),
  "текст");

eq("пустой документ не роняет",
  stripFrontmatter(""),
  "");

// --- AsciiMath --------------------------------------------------------------
eq("одиночные кавычки спрятаны",
  protectAsciiMath("Корень `sqrt(2)` тут"),
  "Корень " + ASM_OPEN + "sqrt(2)" + ASM_CLOSE + " тут");

eq("ограждение блока кода не тронуто",
  protectAsciiMath("```js\nlet a = 1;\n```\n"),
  "```js\nlet a = 1;\n```\n");

eq("двойные кавычки — не делимитер AsciiMath",
  protectAsciiMath("``код с ` внутри``"),
  "``код с ` внутри``");

eq("кавычки возвращаются в HTML",
  restoreAsciiMath("<p>" + ASM_OPEN + "x^2" + ASM_CLOSE + "</p>"),
  "<p>`x^2`</p>");

// --- весь путь через showdown ----------------------------------------------
// Заглушка вместо showdown: нам важно, что до неё доехало и что вернулось.
const echo = { makeHtml: (md) => "<p>" + md + "</p>" };

eq("формула AsciiMath переживает конвертер кавычками",
  markdownToHtml("Корень `sqrt(2)` и всё", echo),
  "<p>Корень `sqrt(2)` и всё</p>");

eq("LaTeX конвертер не трогает — до него дойдёт как есть",
  markdownToHtml("$x^2$ и $$\\int_0^1 x\\,dx$$", echo),
  "<p>$x^2$ и $$\\int_0^1 x\\,dx$$</p>");

eq("frontmatter до конвертера не доезжает",
  markdownToHtml("---\ntitle: Морфи\n---\nТело", echo),
  "<p>Тело</p>");

// --- разбиение на блоки -----------------------------------------------------
eqDeep("абзацы становятся отдельными блоками с номерами строк",
  segmentMarkdown("первый абзац\n\nвторой абзац"),
  [{ line: 1, text: "первый абзац" }, { line: 3, text: "второй абзац" }]);

eq("список не рвётся на пунктах",
  segmentMarkdown("- раз\n- два\n- три").length,
  1);

eq("пустая строка внутри ограждения блок не рвёт",
  segmentMarkdown("```js\nlet a = 1;\n\nlet b = 2;\n```").length,
  1);

eq("пустая строка внутри ограждения блок не рвёт (проверка текста)",
  segmentMarkdown("```js\nlet a = 1;\n\nlet b = 2;\n```")[0].text,
  "```js\nlet a = 1;\n\nlet b = 2;\n```");

eq("frontmatter сдвигает номера строк, а не сбивает их",
  segmentMarkdown(splitFrontmatter("---\ntitle: Морфи\n---\n\n# Партия").body)[0].line,
  2);

eq("splitFrontmatter говорит, сколько строк снял",
  splitFrontmatter("---\ntitle: Морфи\n---\nтело").skipped,
  3);

// --- то же самое, но с настоящим showdown -----------------------------------
// Заглушка выше проверяет наш код, а не стык с конвертером: важно убедиться,
// что в его выводе формулы действительно остались формулами. showdown лежит
// глобально (npm install -g showdown), рядом с jsdom для теста страницы.
function loadShowdown() {
  const require = createRequire(import.meta.url);
  try {
    return require("showdown");
  } catch {
    try {
      const root = execSync("npm root -g").toString().trim();
      return require(path.join(root, "showdown"));
    } catch {
      return null;
    }
  }
}

const showdownMod = loadShowdown();
if (!showdownMod) {
  console.log("skip настоящий showdown — npm install -g showdown");
} else {
  const converter = new showdownMod.Converter({
    tables: true, tasklists: true, simplifiedAutoLink: true, strikethrough: true, headerLevelStart: 2,
  });
  const html = (md) => markdownToHtml(md, converter);

  ok("кавычки AsciiMath дожили до HTML кавычками",
    /<p>Корень `sqrt\(2\)` тут<\/p>/.test(html("Корень `sqrt(2)` тут")),
    html("Корень `sqrt(2)` тут"));

  ok("кавычки не превратились в <code>",
    !html("Корень `sqrt(2)` тут").includes("<code>"),
    html("Корень `sqrt(2)` тут"));

  ok("LaTeX доехал до HTML без изменений",
    html("$x^2$ и $$\\int_0^1 x\\,dx$$").includes("$x^2$"),
    html("$x^2$ и $$\\int_0^1 x\\,dx$$"));

  // Кириллический id showdown оставляет пустым — нам важен уровень заголовка
  // (в облаке он как в редакторе: h2) и то, что настроек документа на экране нет.
  const head = html("---\ntitle: Морфи\n---\n\n# Партия").trim();
  ok("заголовок стал h2, frontmatter на экран не попал",
    /^<h2[^>]*>Партия<\/h2>$/.test(head), head);

  ok("формула внутри таблицы не ломает таблицу",
    html("| ход | формула |\n| --- | --- |\n| 1 | $e^2$ |").includes("$e^2$"),
    html("| ход | формула |\n| --- | --- |\n| 1 | $e^2$ |"));
}

export { failed };
console.log(failed ? "\nПРОВАЛОВ: " + failed : "\nвсё чисто");
process.exitCode = failed ? 1 : 0;
