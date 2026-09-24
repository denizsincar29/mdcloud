// mdcloud — предпросмотр облачных markdown-документов.
//
// Страница показывает документ и умеет позвать редактор: кнопка
// «Редактировать» открывает mathmd на этом документе. Сессия живёт в
// httpOnly-куке на общем домене, поэтому редактору не нужно её получать —
// он просто ходит в API, и браузер прикладывает куку сам.
//
// Разметка документа приходит из облака как обычный markdown, и рендерится
// она так же, как в редакторе mathmd: одна мдшка — один вид. За то, чтобы
// написанное в документе не выполнилось, отвечает не чистка HTML, а CSP
// страницы (см. deploy/Caddyfile.snippet): inline-скриптов и обработчиков на
// странице нет, поэтому вставший из документа тег исполнить нечего.

// Правки самой мдшки (frontmatter, кавычки AsciiMath) живут в md.mjs рядом —
// модуль подгружаем по требованию, см. mdTools() ниже.

const API = "";

const state = {
  user: null,
  doc: null,
  renderers: null,
  config: null, // что сервер рассказал про регистрацию
  accounts: [], // учётные записи — их видит хозяин облака
  tokens: [],
};

const el = (id) => document.getElementById(id);

// editorUrl собирает адрес mathmd на документе owner/path. Путь едет во
// фрагменте (браузер не отправляет его на сервер), а сегменты экранируются
// по отдельности — иначе кириллица и «/» в имени сломают разбор в редакторе.
function editorUrl(base, owner, path) {
  return base + "/#cloud=" +
    (owner + "/" + path).split("/").map(encodeURIComponent).join("/");
}

// Наружу — нарочно: страница живёт одним файлом без модулей, и это
// единственный способ проверить сборку адреса тестом (web/test/ui.test.cjs).
window.mdcloudEditorUrl = editorUrl;

// cleanPath приводит путь к тому виду, который примет сервер: пробелы и
// лишние слэши он отвергает, поэтому чистим здесь, а не отказом потом.
function cleanPath(raw) {
  return (raw || "").trim()
    .replace(/^\/+/, "")
    .replace(/\s+/g, "-")
    .split("/")
    .filter(Boolean)
    .join("/");
}

// docHref — адрес документа на сайте. В ссылке адрес идёт латиницей (slug):
// русский путь в ней превратился бы в «%D0%94%D0%97…», и такую ссылку нельзя
// ни продиктовать голосом, ни прочитать с экрана. Старый адрес сервер тоже
// понимает, поэтому розданные раньше кириллические ссылки не ломаются.
// Владелец и каждый сегмент экранируются по отдельности («/» бывает и внутри
// сегмента).
function docHref(owner, path, slug) {
  return "/" + ownerPath("", owner, slug || path);
}

// docAddress — адрес документа так, как он выглядит в ссылке: то же самое, но
// без ведущего слэша, — чтобы показать человеку и дать продиктовать.
function docAddress(doc) {
  return doc.owner + "/" + (doc.slug || doc.path);
}

// visLabel — как режим доступа называется человеку. Слово одно на весь
// интерфейс: им подписана строка состояния, метка в списке и вариант выбора.
// Значение приходит с сервера строкой, поэтому незнакомое читаем как закрытое
// — «приватный» — а не как «что-то непонятное».
function visLabel(visibility) {
  if (visibility === "public") return "публичный";
  if (visibility === "link") return "по ссылке";
  return "приватный";
}

// untilText — дата, до которой документ живёт. Считаем по календарю, а не по
// остатку часов: «удалить через неделю» — это число, которое человек назвал.
function untilText(when) {
  return new Date(when).toLocaleDateString("ru-RU");
}

// ownerPath — «<префикс><владелец>/<путь>» с экранированием по сегментам:
// один и тот же адрес собирается и для страницы, и для API, и для комментов.
function ownerPath(prefix, owner, path) {
  return prefix + encodeURIComponent(owner) + "/" +
    path.split("/").map(encodeURIComponent).join("/");
}

// apiPath — адрес документа в API.
function apiPath(owner, path) {
  return ownerPath("/api/docs/", owner, path);
}

function status(text) {
  el("status").textContent = text || "";
}

function fail(err) {
  status(err && err.message ? err.message : String(err));
}

// api ходит с credentials: сессия — кука, и без неё сервер не узнает, кто
// пришёл. Токен в ответе остаётся для скриптов, страница им не пользуется.
async function api(path, opts = {}) {
  const headers = {};
  const init = { method: opts.method || "GET", headers, credentials: "include" };
  if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(opts.body);
  }

  const resp = await fetch(API + path, init);
  const text = await resp.text();
  let data = {};
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      throw new Error("сервер ответил не по-нашему: " + text.slice(0, 120));
    }
  }
  if (!resp.ok) {
    const err = new Error(data.error || "ошибка " + resp.status);
    err.status = resp.status;
    throw err;
  }
  return data;
}

// ------------------------------------------------------------------ рендерер

// Библиотеки тянем по требованию: без CDN страница всё равно должна
// открываться и логинить, просто без отрендеренного текста.
async function renderers() {
  // window.mdcloudRenderers — только для теста: в jsdom нет ни import(), ни
  // сети, и страница проверяется с подставленным конвертером.
  if (window.mdcloudRenderers) return (state.renderers = window.mdcloudRenderers);
  if (!state.renderers) {
    const showdownMod = await import("https://cdn.jsdelivr.net/npm/showdown@2.1.0/+esm");
    const showdown = new (showdownMod.default || showdownMod).Converter({
      tables: true,
      tasklists: true,
      simplifiedAutoLink: true,
      strikethrough: true,
      headerLevelStart: 2,
      extensions: [chessExtension, desmosExtension],
    });
    state.renderers = { showdown };
    // Шахматный компонент — тот же, что в mathmd. Доску, фигуры, клавиши и
    // анализ он приносит с собой: страница даёт только тег с fen или pgn.
    import("https://cdn.jsdelivr.net/gh/denizsincar29/chessjax@v0.8.7/chessjax.js").catch(() => {});
  }
  return state.renderers;
}

// ```chess ... ``` → <chessjax-board ...>
const chessExtension = {
  type: "lang",
  filter(text) {
    return text.replace(/```chess[^\n]*\n([\s\S]*?)```/g, (_, body) => {
      const attrs = body
        .split("\n")
        .map((s) => s.trim())
        .filter(Boolean)
        .join(" ");
      return `<chessjax-board ${attrs}></chessjax-board>`;
    });
  },
};

// ```desmos ... ``` → пустое место под график. Расширение ставит только
// контейнер, а рамку с графиком в него вставляет initDesmos. Тело блока едет в
// data-атрибуте целиком закодированным — строки выражений не должны
// разбираться как разметка.
const desmosExtension = {
  type: "lang",
  filter(text) {
    return text.replace(/```desmos[^\n]*\n([\s\S]*?)```/g, (_, body) =>
      `<div class="desmos" data-desmos-body="${encodeURIComponent(body)}"></div>`);
  },
};

// ------------------------------------------------------------------ Desmos

// График Desmos живёт отдельным документом в рамке (/embed/desmos), а не на
// этой странице. Причина не в лени: SDK Desmos исполняет строки как код и без
// 'unsafe-eval' в script-src просто не поднимается, а эта страница рисует
// чужой markdown — её политика скриптов держится ровно на том, что такого
// разрешения в ней нет (см. deploy/Caddyfile.snippet). Графику отдан
// отдельный документ со своей узкой политикой, сюда он приходит рамкой.
const DESMOS_EMBED = "/embed/desmos";

// initDesmos ставит рамки на место контейнеров, которые расставило расширение
// showdown: одна рамка — один график. Выражения едут во фрагменте адреса, на
// сервер он не уходит, а страница рамки отдаёт их калькулятору строками.
function initDesmos(root) {
  for (const spot of root.querySelectorAll(".desmos[data-desmos-body]")) {
    const body = decodeURIComponent(spot.dataset.desmosBody || "");
    if (!body.trim()) continue;
    const frame = document.createElement("iframe");
    frame.className = "desmos-frame";
    frame.title = "График Desmos";
    frame.src = DESMOS_EMBED + "#" + encodeURIComponent(body);
    spot.append(frame);
  }
}

// Наружу — нарочно: страница живёт одним файлом без модулей, и это
// единственный способ проверить разбор блока тестом (web/test/ui.test.cjs).
window.mdcloudDesmosExtension = desmosExtension;

// md.mjs — модуль, и грузим мы его сами: страница живёт одним скриптом, а
// тест прогоняет её в jsdom как обычный скрипт, где import не работает (см.
// web/test/ui.test.cjs). Один лишний запрос, дальше модуль в кеше браузера.
let mdModule = null;
function mdTools() {
  // window.mdcloudMd — только для теста: в jsdom динамический import не
  // работает, и страница проверяется с подставленным модулем.
  if (!mdModule) mdModule = window.mdcloudMd ? Promise.resolve(window.mdcloudMd) : import("./md.mjs");
  return mdModule;
}

// paintDocument раскладывает документ по блокам: строка-абзац становится
// отдельным div с номером первой строки, на неё можно встать (Tab, клик) и
// сослаться из комментария. Номера считаются по исходнику документа, включая
// frontmatter, — те же числа, что человек видит в редакторе.
//
// Готовый HTML ничем не чистим: страницу защищает CSP, а не список разрешённых
// тегов (см. deploy/Caddyfile.snippet).
async function paintDocument(markdown, into) {
  const [{ showdown }, { markdownBlocks }] = await Promise.all([renderers(), mdTools()]);
  into.replaceChildren();
  for (const block of markdownBlocks(markdown, showdown)) {
    const div = document.createElement("div");
    div.className = "doc-block";
    div.dataset.line = String(block.line);
    div.id = "line-" + block.line;
    div.tabIndex = 0;
    div.innerHTML = block.html;
    into.append(div);
  }
  return into;
}

// ------------------------------------------------------------------ формулы

// MathJax — тот же и настроен так же, как в редакторе mathmd: скрытый MathML
// вместо встроенной англоязычной речи (NVDA читает его на своём языке), меню
// выключено, авторасстановка при загрузке тоже — формулы расставляем сами,
// после того как документ отрисован.
const MATHJAX_SRC = "https://cdn.jsdelivr.net/npm/mathjax@4/tex-chtml.js";

let mathjaxLoading = null;

// Скрипт грузим лениво и один раз. Конфиг обязан стоять в window.MathJax до
// загрузки скрипта, поэтому он здесь, а не в разметке: инлайновый <script> на
// странице запрещён её же CSP.
function loadMathJax() {
  // MathJax уже на странице (или подставлен тестом) — свой конфиг не навязываем
  // и второй раз не грузим.
  if (window.MathJax && window.MathJax.typesetPromise) return Promise.resolve(window.MathJax);
  if (!mathjaxLoading) {
    window.MathJax = {
      loader: { load: ["input/tex", "input/asciimath", "output/chtml"] },
      tex: {
        inlineMath: [["$", "$"], ["\\(", "\\)"]],
        displayMath: [["$$", "$$"], ["\\[", "\\]"]],
        packages: { "[+]": ["ams"] },
      },
      asciimath: { delimiters: [["`", "`"]] },
      options: {
        menuOptions: { settings: { enrich: true, assistiveMml: true, speech: false, braille: false } },
        a11y: { speech: false, assistiveMml: true },
        enableMenu: false,
        renderActions: {},
      },
    };
    mathjaxLoading = new Promise((resolve, reject) => {
      const script = document.createElement("script");
      script.src = MATHJAX_SRC;
      script.async = true;
      script.onload = () => resolve(window.MathJax);
      script.onerror = () => reject(new Error("MathJax не загрузился"));
      document.head.append(script);
    });
  }
  return mathjaxLoading;
}

// typesetMath не роняет показ документа: текст без формул полезнее пустого
// экрана, поэтому о неудаче говорим в статусе и живём дальше.
async function typesetMath(root) {
  try {
    const mj = await loadMathJax();
    await (mj.startup && mj.startup.promise);
    await mj.typesetPromise([root]);
  } catch (err) {
    status("Формулы не отрисовались: " + err.message);
  }
}

// ------------------------------------------------------------------ строки

// Указание на строку документа из комментария: человек пишет «вот здесь»,
// нажимает «Указать на строку документа», ходит по блокам стрелками, а Enter
// вставляет в текст ссылку {line 5}. Читатель комментария переходит по ней на
// ту же строку — поэтому блоки предпросмотра пронумерованы по исходнику.
const picker = { on: false, index: 0 };

function docBlocks() {
  return [...el("doc-body").querySelectorAll(".doc-block")];
}

// blockByLine — блок, с которого начинается строка line (или ближайший
// предыдущий): ссылка может указывать на строку внутри абзаца.
function blockByLine(line) {
  const blocks = docBlocks();
  let found = null;
  for (const block of blocks) {
    if (Number(block.dataset.line) <= line) found = block;
    else break;
  }
  return found || blocks[0] || null;
}

// Куда возвращает Alt+B: последний якорь, с которого человек ушёл в документ, —
// кнопка формы комментария или ссылка «строка N» в самом комментарии. Ничего
// не нажимали — ведём в поле комментария.
let lastCommentAnchor = null;

function commentAnchor() {
  return lastCommentAnchor && document.body.contains(lastCommentAnchor)
    ? lastCommentAnchor
    : el("comment-body");
}

// Где стоит курсор и что выделено в комментарии. Пока фокус в поле, берём
// selectionStart/End; после ухода в документ — последнее, что запомнили: туда
// встанет ссылка, а выделенный текст станет её подписью.
let region = { start: 0, end: 0 };

function commentRegion() {
  const field = el("comment-body");
  if (document.activeElement === field) {
    region = {
      start: field.selectionStart ?? field.value.length,
      end: field.selectionEnd ?? field.value.length,
    };
  }
  const len = field.value.length;
  return { start: Math.min(region.start, len), end: Math.min(region.end, len) };
}

// Кнопка выбора — та же, что включает и выключает: она теперь висит на краю
// экрана, и нажатие на неё во время ходьбы по строкам читается как «хватит».
function setPickingButton(on) {
  const button = el("comment-line");
  button.textContent = on ? "Отменить выбор строки" : "Указать на строку документа";
  button.setAttribute("aria-pressed", on ? "true" : "false");
}

function startPicking() {
  const blocks = docBlocks();
  if (!blocks.length) {
    status("Строк пока нет — документ ещё не отрисован.");
    return;
  }
  picker.on = true;
  picker.index = 0;
  setPickingButton(true);
  blocks[0].focus();
  status("Выбираю строку: стрелки вверх и вниз — по строкам, Enter — вставить ссылку, Escape — отмена.");
}

function stopPicking() {
  picker.on = false;
  setPickingButton(false);
}

// cancelPicking — тот же выход, что по Escape: выбор снят, человек снова в
// комментарии, где писал.
function cancelPicking() {
  stopPicking();
  commentAnchor().focus();
  status("Выбор строки отменил.");
}

function movePick(step) {
  const blocks = docBlocks();
  if (!blocks.length) return;
  picker.index = Math.min(blocks.length - 1, Math.max(0, picker.index + step));
  blocks[picker.index].focus();
}

// insertLineRef вставляет ссылку на строку туда, где человек писал, и
// возвращает фокус в комментарий — «вставилось, пиши дальше». Выделенное слово
// становится подписью ссылки: «[здесь]{line 5}» — как гиперссылка в markdown,
// только ведёт она на строку документа. Без выделения вставляется «{line 5}».
function insertLineRef(line) {
  const field = el("comment-body");
  const { start, end } = commentRegion();
  const picked = end > start;
  const ref = picked
    ? "[" + field.value.slice(start, end) + "]{line " + line + "}"
    : "{line " + line + "}";
  field.value = field.value.slice(0, start) + ref + field.value.slice(end);
  region = { start: start + ref.length, end: start + ref.length };
  field.focus();
  field.setSelectionRange(region.start, region.start);
  status(picked
    ? "Строка " + line + " встала ссылкой на выделенное слово."
    : "Строка " + line + " вставлена.");
}

// focusLine — переход по ссылке {line N}: ставим фокус на строку документа,
// её прочитает чтец экрана.
//
// Одного focus() чтецам мало: курсор чтения при этом остаётся там, где был, и
// человек слышит документ с начала. Поэтому строку ещё и называем вслух через
// живой область статуса — «Строка 5: …» доходит всегда, чем бы ни закончился
// перевод фокуса. Сам переход делает браузер по якорю #line-N (см. lineRefLink).
function focusLine(line) {
  const block = blockByLine(line);
  if (!block) {
    status("Такой строки в документе нет.");
    return;
  }
  // scrollIntoView есть не везде (в тесте страницы его нет) — на переход это
  // не влияет: фокус браузер и сам подтянет к видимой части.
  if (block.scrollIntoView) block.scrollIntoView({ block: "center" });
  block.focus();
  const text = (block.textContent || "").replace(/\s+/g, " ").trim();
  status("Строка " + block.dataset.line + (text ? ": " + text.slice(0, 80) : ""));
}

// paintCommentBody показывает текст комментария, превращая ссылку на строку в
// настоящую ссылку. Две формы: «{line 5}» — подпись «строка 5», и
// «[здесь]{line 5}» — подпись своя, как у гиперссылки в markdown. Собираем
// через текстовые узлы: остальной разметке в комментариях места нет.
const LINE_REF = /\[([^\]\n]+)\]\{line\s*(\d+)\}|\{line\s*(\d+)\}/g;

function lineRefLink(label, line) {
  const link = document.createElement("a");
  link.href = "#line-" + line;
  link.className = "line-ref";
  link.textContent = label;
  // Переход по якорю не отменяем: браузер сам переносит и экран, и курсор
  // чтения чтеца экрана на строку — это работает надёжнее, чем focus()
  // вручную (см. focusLine). Наш обработчик только дописывает фокус и подпись.
  link.addEventListener("click", () => {
    // Запоминаем саму ссылку — Alt+B вернёт сюда же.
    lastCommentAnchor = link;
    focusLine(line);
  });
  return link;
}

function paintCommentBody(into, text) {
  into.replaceChildren();
  let last = 0;
  for (const match of (text || "").matchAll(LINE_REF)) {
    if (match.index > last) into.append(document.createTextNode(text.slice(last, match.index)));
    const label = (match[1] || "").trim();
    const line = Number(match[2] ?? match[3]);
    into.append(lineRefLink(label || "строка " + line, line));
    last = match.index + match[0].length;
  }
  if (last < (text || "").length) into.append(document.createTextNode(text.slice(last)));
}

// ------------------------------------------------------------------ шапка

// Учётная запись — меню, а не ряд кнопок: «Выйти» рядом с «Учётными записями»
// слишком легко нажать мимо. Кнопка в шапке раскрывает список, Escape и клик
// в стороне его закрывают, при раскрытии фокус сразу встаёт на первый пункт —
// чтец экрана читает «меню раскрыто, Учётные записи, кнопка».
function accountMenu() {
  return el("account-menu");
}

function accountMenuOpen() {
  return !accountMenu().hidden;
}

function openAccountMenu() {
  accountMenu().hidden = false;
  el("account-toggle").setAttribute("aria-expanded", "true");
  const first = accountMenu().querySelector("button:not([hidden])");
  if (first) first.focus();
}

function closeAccountMenu({ focus = false } = {}) {
  accountMenu().hidden = true;
  el("account-toggle").setAttribute("aria-expanded", "false");
  if (focus) el("account-toggle").focus();
}

function toggleAccountMenu() {
  if (accountMenuOpen()) closeAccountMenu({ focus: true });
  else openAccountMenu();
}

function paintAuth() {
  const logged = Boolean(state.user);
  const toggle = el("account-toggle");
  toggle.hidden = !logged;
  toggle.textContent = logged ? "Учётная запись: " + state.user.username : "Учётная запись";
  el("login-toggle").hidden = logged;
  el("tokens-toggle").hidden = !logged;
  el("admin-toggle").hidden = !(logged && state.user.is_admin);
  if (!logged) closeAccountMenu();
}

// ------------------------------------------------------------------ экраны

const SCREENS = ["login", "register", "index", "doc", "admin", "tokens"];

function show(id) {
  for (const name of SCREENS) el(name).hidden = name !== id;
}

// registration — что можно рассказать про регистрацию по ответу /api/config.
// Открыта она всегда, разница одна: свободно ли место хозяина. Если настройки
// не доехали, считаем открытой — прятать форму из-за молчания сервера глупо.
function registration() {
  return (state.config && state.config.registration) || "open";
}

function paintRegister() {
  el("register-hint").textContent = registration() === "first"
    ? "Вы первый — регистрируйтесь, и облако станет вашим: учётные записи ведёте вы."
    : "Регистрация открыта — заводите аккаунт.";
}

async function openLogin(message) {
  show("login");
  status(message || "");
  paintRegister();
  // На первом запуске логиниться некому — сразу к регистрации.
  if (registration() === "first") {
    await openRegister();
    return;
  }
  el("login-name").focus();
}

// openRegister показывает форму регистрации. Пропусков в облако нет: форма
// открыта всякому, кто до неё дошёл.
async function openRegister() {
  show("register");
  status("");
  paintRegister();
  if (!state.config) {
    try {
      state.config = await api("/api/config");
      paintRegister();
    } catch {
      // без настроек просто покажем форму как есть
    }
  }
  el("register-name").focus();
}

// docRow собирает строку списка: имя файла без папки — сама папка стоит
// заголовком над ним. Ссылка остаётся ссылкой, но нажать можно и мимо её
// текста: так до пункта доходит чтец экрана, который в режиме обзора
// попадает на пункт списка, а не на ссылку внутри него.
//
// У каждой строки есть кнопка «Действия» и меню на ней (см. rowMenu ниже):
// и то и другое — только вошедшему. Не вошедшему команд предлагать нечего,
// и перехватывать у него меню браузера незачем — пусть будет родное.
function docRow(owner, doc, folder) {
  const li = document.createElement("li");
  li.tabIndex = -1; // куда вернётся фокус после закрытия меню действий
  const a = document.createElement("a");
  a.href = docHref(owner, doc.path, doc.slug);
  const leaf = folder === "/" ? doc.path : doc.path.slice(folder.length + 1);
  a.textContent = doc.title || leaf;
  li.append(a);
  // В списке помечаем всё, что не открыто всем: хозяин должен отличать
  // приватные документы от «по ссылке» — иначе непонятно, какой адрес можно
  // называть, а какой нет. Метка живёт отдельным узлом: меню действий меняет
  // режим на месте, не перерисовывая список, и ему есть что поправить.
  const mark = document.createElement("span");
  mark.className = "meta doc-mark";
  mark.hidden = doc.visibility === "public";
  mark.textContent = " — " + visLabel(doc.visibility);
  li.append(mark);
  // Документ на срок помечаем в списке: иначе о нём не вспомнить, а он возьмёт
  // и пропадёт — вместе с тем, что в нём было написано.
  if (doc.expires_at) {
    const until = document.createElement("span");
    until.className = "meta";
    until.textContent = " — до " + untilText(doc.expires_at);
    li.append(until);
  }
  if (state.user) {
    li.append(rowMenuButton(owner, doc, li));
    rowContextMenu(owner, doc, li);
  }
  li.addEventListener("click", (event) => {
    if (event.target.closest("a") || event.target.closest("button")) return;
    a.click();
  });
  return li;
}

// ------------------------------------------------------ меню действий (APG)

// Меню действий над документом сделано по паттерну WAI-ARIA Menu, а не по
// disclosure. Разница не в разметке, а в обещании: пункты меню — команды
// (переименовать, дублировать, удалить), и от такого списка чтец экрана ждёт
// стрелок, одной остановки Tab и Escape с возвратом фокуса. Disclosure с
// кнопками обещает обратное — обычный Tab по списку, — и подмена одного
// другим читается как «кнопки, кнопки, кнопки» без единой подсказки, что тут
// вообще-то меню.
//
// Экзотики вроде role="toolbar" поверх меню не нужно: на роли menu и menuitem
// NVDA сама включает режим форм — это подтверждает разработчик NVDA в их
// трекере (nvaccess/nvda#11143: «if NVDA is switching to focus mode, that
// means there is a role (menu, menuitem…) that needs to be treated as an
// interactive control which implements its own keyboard navigation»), поэтому
// стрелки доходят до страницы штатно.
//
// Меню одно на страницу: открытое живёт в rowMenu, закрытое снимается с
// документа целиком, а не прячется атрибутом.

let rowMenu = null; // { root, trigger, returnTo, buttons, items }

function rowMenuOpen() {
  return Boolean(rowMenu);
}

// closeRowMenu снимает меню. Фокус возвращается туда, откуда меню позвали:
// кнопке «Действия» или самой строке, если меню открыли правым кликом по ней.
function closeRowMenu({ focus = false } = {}) {
  if (!rowMenu) return;
  const menu = rowMenu;
  rowMenu = null;
  menu.root.remove();
  if (menu.trigger) menu.trigger.setAttribute("aria-expanded", "false");
  const back = menu.returnTo || menu.trigger;
  if (focus && back && back.focus) back.focus();
}

// menuTrigger — кнопка «Действия» в строке. Она же точка входа с клавиатуры:
// до неё доходит Tab, а Applications и Shift+F10 на ней открывают то же меню.
// Подпись включает, чья это строка: иначе чтец экрана читает двадцать
// одинаковых «Действия» и не понимает, к какому документу они относятся.
// Кнопкой дело не ограничивается — то же меню открывают правым кликом и
// Applications на самой строке (см. rowContextMenu).
function menuTrigger(label, onOpen) {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "menu-trigger";
  btn.textContent = "Действия";
  btn.setAttribute("aria-haspopup", "menu");
  btn.setAttribute("aria-expanded", "false");
  btn.setAttribute("aria-label", label);
  btn.addEventListener("click", () => onOpen({ trigger: btn, returnTo: btn }));
  btn.addEventListener("contextmenu", (event) => {
    event.preventDefault();
    event.stopPropagation();
    onOpen({ trigger: btn, returnTo: btn });
  });
  return btn;
}

function rowMenuButton(owner, doc, row) {
  return menuTrigger("Действия: " + (doc.title || doc.path), (ctx) => {
    openRowMenu(owner, doc, { ...ctx, row });
  });
}

// rowContextMenu — Applications, Shift+F10 и правый клик по строке открывают
// наше меню вместо браузерного. Пункты, которых человек этим лишается,
// внутри есть: «Открыть в новом окне» и «Скопировать адрес».
function rowContextMenu(owner, doc, row) {
  row.addEventListener("contextmenu", (event) => {
    event.preventDefault();
    openRowMenu(owner, doc, {
      trigger: row.querySelector(".menu-trigger"),
      returnTo: row,
      row,
      point: { x: event.clientX, y: event.clientY },
    });
  });
}

// rowMenuItems — что вообще можно сделать с этой строкой. Чужой документ
// (из «Со мной поделились» и из чужого публичного списка) — только читать;
// над своим — всё.
function rowMenuItems(owner, doc, ctx) {
  const href = docHref(owner, doc.path, doc.slug);
  const mine = Boolean(state.user && state.user.username === owner);
  const items = [
    { label: "Открыть", run: () => { location.href = href; } },
  ];
  if (mine) {
    items.push({ label: "Редактировать", run: () => openInEditor(owner, doc.path) });
  }
  items.push(
    { label: "Открыть в новом окне", run: () => window.open(href, "_blank", "noopener") },
    { label: "Скопировать адрес", run: () => copyAddress(location.origin + href) },
  );
  if (!mine) return items;

  items.push(
    { separator: true },
    {
      role: "menuitemradio",
      label: "Приватный — читаете вы и те, кому отправили",
      checked: doc.visibility !== "link" && doc.visibility !== "public",
      run: () => applyVisibility(owner, doc, ctx, "private"),
    },
    {
      role: "menuitemradio",
      label: "По ссылке — кто знает адрес",
      checked: doc.visibility === "link",
      run: () => applyVisibility(owner, doc, ctx, "link"),
    },
    {
      role: "menuitemradio",
      label: "Публичный — виден всем в списке документов",
      checked: doc.visibility === "public",
      run: () => applyVisibility(owner, doc, ctx, "public"),
    },
    { separator: true },
    { label: "Переименовать", run: () => openRowAction(owner, doc, ctx, "rename-toggle") },
    { label: "Дублировать", run: () => duplicateDoc(owner, doc) },
    { label: "Срок хранения", run: () => openRowAction(owner, doc, ctx, "expiry-toggle") },
    { label: "Отправить человеку", run: () => openRowAction(owner, doc, ctx, "share-toggle") },
    { separator: true },
    { label: "Удалить", run: () => askDelete(owner, doc, ctx) },
  );
  return items;
}

function openRowMenu(owner, doc, ctx) {
  openMenu(ctx.items || rowMenuItems(owner, doc, ctx), {
    ...ctx,
    ariaLabel: "Действия с документом " + (doc.title || doc.path),
  });
}

// openMenu — построить и раскрыть меню: пункты, клавиши, фокус. Одно на весь
// сайт: меню документа и меню учётной записи отличаются только списком пунктов,
// и расходиться в поведении им незачем. at — либо кнопка-источник, либо точка,
// где щёлкнули; фокус сразу встаёт на первый пункт, иначе чтец экрана не узнает,
// что меню вообще раскрылось.
function openMenu(items, ctx) {
  closeRowMenu();
  const root = document.createElement("div");
  root.id = "row-menu";
  root.setAttribute("role", "menu");
  root.setAttribute("aria-label", ctx.ariaLabel || "Действия");
  const buttons = [];
  for (const item of items) {
    if (item.separator) {
      const sep = document.createElement("div");
      sep.setAttribute("role", "separator");
      root.append(sep);
      continue;
    }
    const btn = document.createElement("button");
    btn.type = "button";
    btn.setAttribute("role", item.role || "menuitem");
    btn.textContent = item.label;
    if (item.checked) btn.setAttribute("aria-checked", "true");
    if (item.disabled) btn.setAttribute("aria-disabled", "true");
    btn.tabIndex = -1;
    btn.addEventListener("click", () => {
      if (item.disabled) return;
      closeRowMenu({ focus: true });
      item.run();
    });
    buttons.push(btn);
    root.append(btn);
  }

  // Клавиши меню: стрелки по кругу, Home и End по краям, Escape закрывает и
  // возвращает фокус, Tab просто уводит дальше — меню при этом закрывается.
  // Буква переводит к пункту на неё: пунктов много, стрелками ходить долго.
  let typed = "";
  let typedAt = 0;
  root.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      event.preventDefault();
      closeRowMenu({ focus: true });
      return;
    }
    if (event.key === "Tab") {
      closeRowMenu({ focus: true }); // дальше браузер сам уводит фокус по Tab
      return;
    }
    const step = { ArrowDown: 1, ArrowUp: -1 }[event.key];
    if (step) {
      event.preventDefault();
      menuFocus(buttons, menuIndex(buttons) + step);
      return;
    }
    if (event.key === "Home" || event.key === "End") {
      event.preventDefault();
      menuFocus(buttons, event.key === "Home" ? 0 : buttons.length - 1);
      return;
    }
    if (event.key.length === 1 && !event.ctrlKey && !event.altKey && !event.metaKey) {
      const now = Date.now();
      typed = now - typedAt > 800 ? event.key : typed + event.key;
      typedAt = now;
      const hit = buttons.find((b) => b.textContent.toLowerCase().startsWith(typed.toLowerCase()));
      if (hit) menuFocus(buttons, buttons.indexOf(hit));
    }
  });

  // Меню закрывается, когда фокус ушёл из него, и когда щёлкнули мимо.
  root.addEventListener("focusout", () => {
    setTimeout(() => {
      if (rowMenu && rowMenu.root === root && !root.contains(document.activeElement)) {
        closeRowMenu();
      }
    }, 0);
  });
  root.addEventListener("click", (event) => event.stopPropagation());

  rowMenu = { root, trigger: ctx.trigger, returnTo: ctx.returnTo, buttons, items };
  // Ставим меню, пока оно невидимо: у только что вставленного узла есть
  // размер, но ещё нет места на экране, и мерить его надо до показа, иначе
  // оно мигнёт в левом верхнем углу.
  root.style.visibility = "hidden";
  document.body.append(root);
  placeRowMenu(root, ctx);
  root.style.visibility = "";
  if (ctx.trigger) ctx.trigger.setAttribute("aria-expanded", "true");
  menuFocus(buttons, 0);
}

// placeRowMenu ставит меню у кнопки или у курсора и не даёт ему уехать за
// край окна: пункт, до которого не добраться, — тот же отсутствующий пункт.
function placeRowMenu(root, ctx) {
  const box = root.getBoundingClientRect();
  let x;
  let y;
  if (ctx.point) {
    x = ctx.point.x;
    y = ctx.point.y;
  } else {
    const rect = ctx.trigger ? ctx.trigger.getBoundingClientRect() : { left: 8, top: 8, bottom: 8 };
    x = rect.left;
    y = rect.bottom + 4;
    // Под кнопкой не поместилось — раскрываем вверх, а не за нижний край.
    if (y + box.height > window.innerHeight - 8 && rect.top - box.height - 4 > 8) {
      y = rect.top - box.height - 4;
    }
  }
  x = Math.max(8, Math.min(x, window.innerWidth - box.width - 8));
  y = Math.max(8, Math.min(y, window.innerHeight - box.height - 8));
  root.style.left = x + "px";
  root.style.top = y + "px";
}

function menuIndex(buttons) {
  return buttons.indexOf(document.activeElement);
}

// menuFocus ставит фокус на пункт по кругу и ведёт за ним tabindex: в меню
// одна остановка Tab, а не столько, сколько пунктов.
function menuFocus(buttons, index) {
  const live = buttons.filter((b) => b.getAttribute("aria-disabled") !== "true");
  if (!live.length) return;
  const target = live[((index % live.length) + live.length) % live.length];
  for (const b of buttons) b.tabIndex = b === target ? 0 : -1;
  target.focus();
}

// copyAddress кладёт адрес в буфер. В буфер не пустили — говорим адрес вслух:
// он всё равно длинный и диктовать его придётся, но потерять его молча нельзя.
async function copyAddress(url) {
  try {
    await navigator.clipboard.writeText(url);
    status("Адрес скопирован: " + url);
  } catch {
    status("Скопировать не вышло — вот адрес: " + url);
  }
}

// applyVisibility меняет режим доступа прямо из списка: документ тут же
// помечается новым словом, а страницу не перерисовываем — иначе фокус уехал
// бы в начало списка, и человек потерял бы место, с которого начал.
async function applyVisibility(owner, doc, ctx, next) {
  if (doc.visibility === next) {
    status("Доступ не менялся — " + visLabel(next) + ".");
    return;
  }
  try {
    const saved = await api(apiPath(owner, doc.path), { method: "PUT", body: { visibility: next } });
    doc.visibility = saved.visibility;
    const mark = ctx.row && ctx.row.querySelector(".doc-mark");
    if (mark) {
      mark.textContent = " — " + visLabel(saved.visibility);
      mark.hidden = saved.visibility === "public";
    }
    status(visWords[saved.visibility] || "Доступ сохранён.");
  } catch (err) {
    fail(err);
  }
}

// openRowAction уводит на страницу документа и раскрывает на ней нужную форму:
// переименование, срок и отправка живут там, и второй их копии в списке не
// нужно. Адрес страницы меняется вместе с переходом — иначе F5 вернул бы
// человеку список, с которого он пришёл.
async function openRowAction(owner, doc, ctx, toggleId) {
  const href = docHref(owner, doc.path, doc.slug);
  history.pushState(null, "", href);
  await openDoc(owner, doc.path);
  const toggle = el(toggleId);
  if (!toggle.hidden) toggle.click();
}

// duplicateDoc делает копию рядом с оригиналом. Имя не спрашиваем: копия
// получает адрес с «-копия» на конце, а переименовать её можно тут же, из её
// же меню. Спросить имя значило бы показать поле посреди списка — и потерять
// место, где человек стоял.
async function duplicateDoc(owner, doc) {
  status("Делаю копию…");
  try {
    const full = await api(apiPath(owner, doc.path));
    const body = {
      title: (doc.title || doc.path) + " (копия)",
      content: full.content,
      visibility: doc.visibility,
      comments_on: doc.comments_on,
      comments_require_auth: doc.comments_require_auth,
    };
    let copy = null;
    for (let n = 1; n <= 20 && !copy; n++) {
      const path = doc.path + (n === 1 ? "-копия" : "-копия-" + n);
      try {
        copy = await api("/api/docs", { method: "POST", body: { ...body, path } });
      } catch (err) {
        if (err.status !== 409) throw err; // 409 — адрес занят, берём следующий
      }
    }
    if (!copy) {
      status("Не нашёл свободного адреса для копии — переименуйте оригинал и повторите.");
      return;
    }
    await openIndex(owner);
    status("Копия готова: " + docAddress(copy));
  } catch (err) {
    fail(err);
  }
}

// askDelete спрашивает подтверждение тем же меню, а не окном браузера: окно
// чтец экрана читает через силу, а тут подтверждение — такой же пункт меню,
// как всё остальное, и «Отмена» рядом.
function askDelete(owner, doc, ctx) {
  const title = doc.title || doc.path;
  openRowMenu(owner, doc, {
    ...ctx,
    items: [
      { label: "Да, удалить «" + title + "»", run: () => deleteDoc(owner, doc) },
      { label: "Отмена", run: () => status("Документ на месте.") },
    ],
  });
}

async function deleteDoc(owner, doc) {
  try {
    await api(apiPath(owner, doc.path), { method: "DELETE" });
    if (state.doc && state.doc.path === doc.path && state.doc.owner === owner) {
      state.doc = null;
      history.replaceState(null, "", "/" + encodeURIComponent(owner));
    }
    if (state.user && state.user.username === owner) await openIndex(owner);
    status("Документ удалён: " + owner + "/" + doc.path);
  } catch (err) {
    fail(err);
  }
}

async function openIndex(owner) {
  show("index");
  // Завести документ можно только у себя: у чужого списка формы нет.
  const mine = Boolean(state.user && state.user.username === owner);
  el("new-doc-form").hidden = !mine;
  el("new-doc-form").dataset.owner = owner;
  const data = await api("/api/docs/" + encodeURIComponent(owner));
  el("index-h").textContent = "Документы: " + owner;
  document.title = "Документы: " + owner + " — mdcloud";
  const list = el("index-list");
  list.replaceChildren();

  // Документы лежат по папкам, и в списке это видно: папка — заголовок,
  // под ним имена файлов. Путь целиком в каждой строке читать тяжело
  // (все строки начинаются одинаково), а по заголовкам папок чтец экрана
  // ходит с клавиатуры одним нажатием. «/» — документы в корне облака.
  const folders = new Map();
  for (const doc of data.docs) {
    const cut = doc.path.lastIndexOf("/");
    const folder = cut < 0 ? "/" : doc.path.slice(0, cut);
    if (!folders.has(folder)) folders.set(folder, []);
    folders.get(folder).push(doc);
  }
  const order = [...folders.keys()].sort((a, b) => {
    if (a === "/") return b === "/" ? 0 : -1; // корень — первым
    if (b === "/") return 1;
    return a.localeCompare(b, "ru");
  });
  for (const folder of order) {
    const group = document.createElement("li");
    group.className = "folder";
    const head = document.createElement("h2");
    head.textContent = folder;
    const inner = document.createElement("ul");
    inner.className = "doclist";
    for (const doc of folders.get(folder)) inner.append(docRow(owner, doc, folder));
    group.append(head, inner);
    list.append(group);
  }
  if (!data.docs.length) {
    const li = document.createElement("li");
    li.className = "meta";
    li.textContent = "Пусто.";
    list.append(li);
  }
  // Присланное показываем только у себя: это чужой список, и ходить в него
  // со страницы другого человека незачем.
  await paintShared(mine);
  el("main").focus();
}

// paintShared — раздел «Со мной поделились»: документы, которые отправили мне.
// Отдельно от «моих документов»: там владение и правка, а тут чужая работа,
// которую дали почитать.
async function paintShared(mine) {
  const block = el("shared-block");
  const list = el("shared-list");
  block.hidden = !mine;
  list.replaceChildren();
  if (!mine) return;
  let data;
  try {
    data = await api("/api/shared");
  } catch (err) {
    block.hidden = true;
    return;
  }
  for (const doc of data.docs) {
    const li = document.createElement("li");
    li.tabIndex = -1;
    const a = document.createElement("a");
    a.href = docHref(doc.owner, doc.path, doc.slug);
    a.textContent = (doc.title || doc.path) + " — от " + doc.owner;
    li.append(a);
    if (doc.expires_at) {
      const until = document.createElement("span");
      until.className = "meta";
      until.textContent = " — до " + untilText(doc.expires_at);
      li.append(until);
    }
    // Меню и здесь: чужой документ команд не даёт, но «открыть в новом окне»
    // и «скопировать адрес» нужны ровно так же, а правый клик по строке всё
    // равно отбирает у браузера его меню.
    if (state.user) {
      li.append(rowMenuButton(doc.owner, doc, li));
      rowContextMenu(doc.owner, doc, li);
    }
    li.addEventListener("click", (event) => {
      if (event.target.closest("a") || event.target.closest("button")) return;
      a.click();
    });
    list.append(li);
  }
  if (!data.docs.length) {
    const li = document.createElement("li");
    li.className = "meta";
    li.textContent = "Пока никто ничего не присылал.";
    list.append(li);
  }
}

// Документ, открытый по своему адресу, показываем в том состоянии, в котором
// его оставили: раскрытые формы — это место, где человек стоял, и после
// возвращения из редактора ему нужно то же место, а не список с начала.
// Считанные правки остаются открытыми прежним поведением, перенос адреса —
// тоже (см. перенос ниже); обновление из-за края страницы сбрасывает всё.
let justMoved = false;

// Обновление открытого документа. Возврат из редактора приходит без события:
// вкладку облака не перезагружают, а показывают снова (и по кнопке «открыть
// документ в облаке» mathmd делает ровно это). Поэтому спрашиваем сами, но не
// показом, а обновлением — приход со стороны не должен перебрасывать читателя
// наверх, туда, где он читает прямо сейчас. `updated_at` не даёт беспокоиться
// зря: то же число — та же мдшка, и трогать экран нечего.
//
// Кому обновляться нельзя, решает состояние экрана: идёт запись, открыта форма
// или диалог — молчим и ждём следующего раза. Открытая форма не потери: в ней
// лежат введённые слова, и перерисовать её значит их стереть.
let refreshTimer = 0;

function docEditorOpen() {
  return document.visibilityState !== "visible" ||
    Boolean(document.activeElement && document.activeElement.closest("dialog[open]"));
}

function formOpen() {
  for (const form of document.querySelectorAll("#doc form")) {
    if (!form.hidden) return true;
  }
  return false;
}

function stopRefresh() {
  clearInterval(refreshTimer);
  refreshTimer = 0;
}

function startRefresh(owner, doc) {
  stopRefresh();
  if (!doc.can_edit) return; // чужой документ: показом его не обновим, а Ctrl+R всегда под рукой
  const at = { owner: owner, path: doc.path, updatedAt: doc.updated_at };
  docRefreshAt = at;
  refreshTimer = setInterval(refreshTick, 5000);
}

let docRefreshAt = null;
let refreshing = false;

async function refreshTick() {
  const at = docRefreshAt;
  if (!at) {
    stopRefresh();
    return;
  }
  // Адрес мог уехать: человек ушёл в список или в другой документ.
  if (!state.doc || state.doc.path !== at.path || state.doc.owner !== at.owner) {
    stopRefresh();
    return;
  }
  if (refreshing || docEditorOpen() || formOpen()) return;
  refreshing = true;
  try {
    const doc = await api(apiPath(at.owner, at.path));
    if (doc.updated_at !== at.updatedAt) {
      at.updatedAt = doc.updated_at;
      // Документ удалён, пока он был открыт: показывать его дальше нечего.
      // Спрашиваем тем же меню, что и удаление, — окно браузера чтец экрана
      // читает через силу.
      const gone = !doc.content;
      await openDoc(at.owner, at.path, { keepPlace: true, silent: true });
      status(gone
        ? "Документ изменился или удалён — на экране сейчас то, что лежит в облаке."
        : "Документ обновлён: его правил кто-то ещё.");
    }
  } catch (err) {
    if (err.status === 404) {
      status("Документ удалён — обновляю.");
      stopRefresh();
      await render();
    }
    // остальные беды (связь, сервер) — не повод сыпать сообщениями каждые
    // пять секунд: попробуем в следующий раз
  } finally {
    refreshing = false;
  }
}

async function openDoc(owner, path, opts = {}) {
  const keepPlace = Boolean(opts.keepPlace);
  show("doc");
  const doc = await api(apiPath(owner, path));
  state.doc = doc;

  el("doc-title").textContent = doc.title || doc.path;
  document.title = (doc.title || doc.path) + " — mdcloud";
  // Свой документ — «приватный», присланный — «прислан вам»: иначе читатель
  // гадает, почему чужая работа закрыта для остальных, а открыта ему. Открытые
  // режимы называются одинаково для всех: «присланным» документ, доступный
  // всякому по адресу, назвать нельзя — читатель решил бы, что он один такой.
  const own = !state.user || state.user.username === doc.owner;
  const access = doc.visibility === "private" && !own
    ? "прислан вам"
    : visLabel(doc.visibility);
  el("doc-meta").textContent = [
    "Адрес: " + docAddress(doc),
    access,
    "обновлён " + new Date(doc.updated_at).toLocaleString("ru-RU"),
  ].join(" · ");

  // Про срок говорим всем, кто открыл документ, — и хозяину, и читателю:
  // «домашка до пятницы» перестаёт открываться в срок, и тот, кому её отдали,
  // должен это видеть заранее, а не гадать, куда она делась.
  const line = el("doc-expiry");
  line.hidden = !doc.expires_at;
  if (doc.expires_at) {
    line.textContent = "Документ удалится " + untilText(doc.expires_at) +
      " — после этого ссылка перестанет открываться.";
  }

  const body = el("doc-body");
  try {
    await paintDocument(doc.content, body);
    await typesetMath(body);
    // Рамки графиков — последними: сам график грузится уже внутри рамки и
    // показ документа не задерживает.
    initDesmos(body);
  } catch (err) {
    body.replaceChildren();
    const pre = document.createElement("pre");
    pre.textContent = doc.content || "";
    body.append(pre);
    status("Не загрузился рендерер, показываю исходный текст. " + err.message);
  }

  el("edit").hidden = !doc.can_edit;
  el("vis-form").hidden = !doc.can_edit;
  el("rename-toggle").hidden = !doc.can_edit;
  el("expiry-toggle").hidden = !doc.can_edit;
  el("share-toggle").hidden = !doc.can_edit;
  // Формы раскрытыми не оставляем: документ открывается с чистого листа.
  // Исключение — тихое обновление на месте: человек читает, и закрывать под
  // ним форму нельзя, но после переноса адреса форма имени не нужна никому.
  if (!keepPlace || justMoved) {
    el("rename-form").hidden = true;
    el("expiry-form").hidden = true;
    el("share-form").hidden = true;
  }
  justMoved = false;
  // Кому документ уже отправлен — видно сразу, без раскрытия формы: иначе
  // отправка второй раз тому же человеку выглядит как потерянная.
  paintShareList(doc.shared_with || []);
  if (doc.can_edit) {
    // В выборе стоит текущий режим: человек должен видеть, что у документа
    // сейчас, а не догадываться по надписи на кнопке.
    el("vis-select").value = doc.visibility;
    if (!el("vis-select").value) el("vis-select").value = "private";
  }

  await loadComments(owner, doc);
  if (!opts.silent) el("main").focus();
  // Обновление открытого документа — на время, пока он открыт: вкладка живёт
  // месяцами, а показанный документ устаревает молча.
  startRefresh(owner, doc);
}

async function loadComments(owner, doc) {
  const base = ownerPath("/api/comments/", owner, doc.path);
  const data = await api(base);
  const list = el("comments");
  list.replaceChildren();
  for (const c of data.comments) {
    const li = document.createElement("li");
    const head = document.createElement("p");
    head.className = "meta";
    head.textContent = c.anonymous
      ? c.author_name + " (без входа) · " + new Date(c.created_at).toLocaleString("ru-RU")
      : c.author_name + " · " + new Date(c.created_at).toLocaleString("ru-RU");
    const body = document.createElement("p");
    // Только текстом: разметке в комментариях не место. Исключение — ссылки на
    // строки {line N}: они собираются узлами, а не разбором HTML.
    paintCommentBody(body, c.body);
    li.append(head, body);
    if (c.mine) {
      const del = document.createElement("button");
      del.type = "button";
      del.textContent = "Удалить комментарий " + c.author_name;
      del.addEventListener("click", async () => {
        try {
          await api("/api/comments/" + c.id, { method: "DELETE" });
          await loadComments(owner, state.doc);
          status("Комментарий удалён.");
        } catch (err) {
          fail(err);
        }
      });
      li.append(del);
    }
    list.append(li);
  }
  el("comments-empty").hidden = data.comments.length > 0;

  const form = el("comment-form");
  form.hidden = !data.can_comment;
  // Кнопка выбора строки ездит по краю экрана, пока комментарии открыты:
  // человек читает документ внизу страницы, и без этого до кнопки пришлось бы
  // листать обратно, потеряв строку, на которую он хотел сослаться.
  el("comment-line").classList.toggle("pinned", !form.hidden);
  el("comment-closed").hidden = data.comments_on;
  // Вошедшему подсказывать нечего: подпись берётся из аккаунта, а поле имени
  // ему не показывают. Пояснения — только тем, кто пишет без входа.
  el("comment-hint").textContent = data.viewer_authenticated
    ? ""
    : data.require_auth
      ? "Здесь пишут только вошедшие."
      : "Можно писать без входа, но представьтесь.";
  el("comment-name-row").hidden = data.viewer_authenticated;
  form.dataset.base = base;
}

// ------------------------------------------------------- учётные записи

// Учётные записи ведёт хозяин облака. Регистрация открыта всем, поэтому этот
// список — единственное место, где видно, кто пришёл; отсюда же учётку убирают.
// Строки устроены как в списке документов и меню на них то же самое: один
// способ делать дела на весь сайт, а не два разных.
async function openAdmin() {
  show("admin");
  status("");
  await paintAccounts();
  el("main").focus();
}

async function paintAccounts() {
  const data = await api("/api/admin/users");
  state.accounts = data.users || [];
  const list = el("accounts-list");
  list.replaceChildren();
  for (const acc of state.accounts) list.append(accountRow(acc));
  if (!state.accounts.length) {
    const li = document.createElement("li");
    li.className = "meta";
    li.textContent = "Аккаунтов пока нет.";
    list.append(li);
  }
}

// plural — русские числительные: «1 документ», «2 документа», «5 документов».
// Список читают голосом, и «2 документов» на слух звучит как ошибка.
function plural(n, one, few, many) {
  const ten = n % 10;
  const hundred = n % 100;
  if (ten === 1 && hundred !== 11) return one;
  if (ten >= 2 && ten <= 4 && (hundred < 12 || hundred > 14)) return few;
  return many;
}

// accountLine — строка учётной записи одной фразой: чтец экрана читает её
// целиком, и по ней должно быть понятно всё, что решает судьбу аккаунта, —
// кто это, хозяин ли, сколько написал и заходил ли вообще.
function accountLine(acc) {
  const since = new Date(acc.created_at).toLocaleDateString("ru-RU");
  const docs = acc.docs
    ? acc.docs + " " + plural(acc.docs, "документ", "документа", "документов")
    : "документов нет";
  const seen = acc.last_login
    ? "заходил " + new Date(acc.last_login).toLocaleString("ru-RU")
    : "ни разу не заходил";
  const role = acc.is_admin ? "хозяин облака" : "аккаунт";
  return (acc.me ? acc.username + " — это вы, " : acc.username + " — ") +
    role + ", заведён " + since + ", " + docs + ", " + seen;
}

function accountRow(acc) {
  const li = document.createElement("li");
  li.tabIndex = -1; // куда вернётся фокус после закрытия меню действий
  const head = document.createElement("p");
  head.textContent = accountLine(acc);
  li.append(head);
  li.append(menuTrigger("Действия с учётной записью " + acc.username, (ctx) => {
    openAccountRowMenu(acc, { ...ctx, row: li });
  }));
  li.addEventListener("contextmenu", (event) => {
    event.preventDefault();
    openAccountRowMenu(acc, {
      trigger: li.querySelector(".menu-trigger"),
      returnTo: li,
      row: li,
      point: { x: event.clientX, y: event.clientY },
    });
  });
  return li;
}

// accountMenuItems — что можно сделать с учётной записью. Себя не удаляют и не
// разжаловывают: хозяин в облаке должен остаться, и держится это ровно на этом
// отказе. Пункты про себя не прячем, а показываем неактивными — иначе человек
// ищет их и не понимает, почему их нет.
function accountMenuItems(acc) {
  const items = [];
  if (acc.is_admin) {
    items.push(acc.me
      ? { label: "Себя разжаловать нельзя", disabled: true }
      : { label: "Разжаловать — перестанет вести учётные записи", run: () => setAccountAdmin(acc, false) });
  } else {
    items.push({ label: "Сделать хозяином — сможет вести учётные записи", run: () => setAccountAdmin(acc, true) });
  }
  items.push(
    { label: "Выгнать — закрыть все входы и отозвать ключи", run: () => kickAccount(acc) },
    { separator: true },
    acc.me
      ? { label: "Свою учётную запись удалить нельзя", disabled: true }
      : { label: "Удалить учётную запись — со всем, что человек написал", run: () => askDeleteAccount(acc) },
  );
  return items;
}

function openAccountRowMenu(acc, ctx) {
  openMenu(ctx.items || accountMenuItems(acc), {
    ...ctx,
    ariaLabel: "Действия с учётной записью " + acc.username,
  });
}

async function setAccountAdmin(acc, admin) {
  try {
    await api("/api/admin/users/" + acc.id + "/admin", { method: "POST", body: { admin } });
    await paintAccounts();
    status(admin
      ? acc.username + " теперь хозяин облака."
      : acc.username + " больше не хозяин.");
  } catch (err) {
    fail(err);
  }
}

async function kickAccount(acc) {
  try {
    const out = await api("/api/admin/users/" + acc.id + "/logout", { method: "POST" });
    status(acc.username + ": закрыто входов — " + out.sessions + ", отозвано ключей — " + out.tokens + ".");
  } catch (err) {
    fail(err);
  }
}

// askDeleteAccount спрашивает подтверждение тем же меню, а не окном браузера:
// удаление уносит всё написанное и обратной дороги нет, а окно чтец экрана
// читает через силу. «Отмена» стоит рядом и никуда не девается.
function askDeleteAccount(acc, ctx) {
  openAccountRowMenu(acc, {
    ...ctx,
    items: [
      { label: "Да, удалить " + acc.username + " — вместе с документами", run: () => deleteAccount(acc) },
      { label: "Отмена", run: () => status("Учётная запись на месте.") },
    ],
  });
}

async function deleteAccount(acc) {
  try {
    await api("/api/admin/users/" + acc.id, { method: "DELETE" });
    await paintAccounts();
    // Список перерисован — от прежних строк не осталось и следа, поэтому фокус
    // ставим на первую: так человек снова в списке, а не в неизвестности.
    const first = el("accounts-list").querySelector("li");
    if (first && first.focus) first.focus();
    status("Учётная запись удалена: " + acc.username + ".");
  } catch (err) {
    fail(err);
  }
}

// ------------------------------------------------------------------ ключи

// Ключи аккаунта: выписать, посмотреть, отозвать. Срок выбирают при выдаче —
// бессрочный ключ удобно положить в скрипт, который ходит в облако каждый день.
// Само значение ключа страница видит один раз: в облаке лежит только отпечаток.
async function openTokens() {
  show("tokens");
  status("");
  await paintTokens();
  el("main").focus();
}

function paintTokens() {
  return api("/api/tokens").then((data) => {
    state.tokens = data.tokens || [];
    const list = el("tokens-list");
    list.replaceChildren();
    for (const tok of state.tokens) list.append(tokenRow(tok));
    if (!state.tokens.length) {
      const li = document.createElement("li");
      li.className = "meta";
      li.textContent = "Ключей пока нет.";
      list.append(li);
    }
  });
}

function tokenRow(tok) {
  const li = document.createElement("li");
  const name = tok.label ? tok.label : "без пометки";
  const until = tok.expires_at
    ? (tok.state === "expired" ? "просрочен " : "действует до ") +
      new Date(tok.expires_at).toLocaleDateString("ru-RU")
    : "бессрочный";
  const used = tok.last_used_at
    ? "им пользовались " + new Date(tok.last_used_at).toLocaleString("ru-RU")
    : "им ещё не пользовались";
  const head = document.createElement("p");
  head.textContent = `${name} — ${until}, ${used}`;
  li.append(head);

  const del = document.createElement("button");
  del.type = "button";
  del.textContent = `Отозвать ключ ${name}`;
  del.addEventListener("click", async () => {
    try {
      await api("/api/tokens/" + tok.id, { method: "DELETE" });
      await paintTokens();
      status("Ключ отозван — скрипт им больше не войдёт.");
    } catch (err) {
      fail(err);
    }
  });
  li.append(del);
  return li;
}

// ------------------------------------------------------------------ маршрут

function route() {
  const parts = decodeURIComponent(location.pathname).split("/").filter(Boolean);
  if (parts.length === 0) return { kind: "home" };
  if (parts.length === 1) return { kind: "user", owner: parts[0] };
  return { kind: "doc", owner: parts[0], path: parts.slice(1).join("/") };
}

async function render() {
  status("");
  const r = route();
  try {
    if (location.hash === "#admin" && state.user && state.user.is_admin) {
      await openAdmin();
      return;
    }
    if (location.hash === "#tokens" && state.user) {
      await openTokens();
      return;
    }
    if (r.kind === "home") {
      if (state.user) {
        await openIndex(state.user.username);
      } else {
        // Не вошедшему на главной показывать нечего, поэтому не прячем экраны,
        // а сразу показываем форму: на новом облаке она же ведёт к регистрации.
        // Чужой документ и так открывается по прямому адресу /имя/папка/файл.
        await openLogin("");
      }
      return;
    }
    if (r.kind === "user") {
      await openIndex(r.owner);
      return;
    }
    await openDoc(r.owner, r.path);
  } catch (err) {
    fail(err);
    if (err.status === 401) await openLogin(err.message);
  }
}

// ------------------------------------------------------------------ события

el("login-toggle").addEventListener("click", () => openLogin(""));
el("account-toggle").addEventListener("click", toggleAccountMenu);
// Клик в стороне закрывает меню — но не клик по самой кнопке: её обрабатывает
// toggleAccountMenu, иначе меню закрывалось бы сразу после раскрытия.
document.addEventListener("click", (event) => {
  if (!accountMenuOpen()) return;
  if (event.target.closest && (event.target.closest("#account-menu") || event.target.closest("#account-toggle"))) return;
  closeAccountMenu();
});
el("tokens-toggle").addEventListener("click", () => {
  closeAccountMenu();
  location.hash = "#tokens";
  render();
});
el("admin-toggle").addEventListener("click", () => {
  closeAccountMenu();
  location.hash = "#admin";
  render();
});
el("logout").addEventListener("click", async () => {
  closeAccountMenu();
  try {
    await api("/api/auth/logout", { method: "POST" });
  } catch {
    // выйти должно получиться всегда, даже если сервер не ответил
  }
  state.user = null;
  paintAuth();
  location.href = "/";
});

el("login-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  status("Вхожу…");
  try {
    const out = await api("/api/auth/login", {
      method: "POST",
      body: { login: el("login-name").value, password: el("login-pass").value },
    });
    state.user = out.user;
    paintAuth();
    el("login-pass").value = "";
    // Токен из ответа показывает, что выдача сработала, но страница живёт
    // кукой — в разметке и в памяти его держать незачем.
    await render();
  } catch (err) {
    fail(err);
  }
});

el("register-toggle").addEventListener("click", () => openRegister());
el("register-back").addEventListener("click", () => openLogin(""));

el("register-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  // Согласие — правовое основание обработки (152-ФЗ). Сервер без него
  // аккаунт не создаст; здесь ловим раньше, чтобы человек не гадал.
  if (!el("register-consent").checked) {
    fail({ message: "Поставьте галочку согласия с политикой обработки персональных данных." });
    el("register-consent").focus();
    return;
  }
  status("Создаю аккаунт…");
  try {
    const out = await api("/api/auth/register", {
      method: "POST",
      body: {
        username: el("register-name").value,
        password: el("register-pass").value,
        consent: true,
      },
    });
    state.user = out.user;
    paintAuth();
    el("register-pass").value = "";
    status("Здравствуйте, " + out.user.username + ".");
    await render();
  } catch (err) {
    fail(err);
  }
});

el("admin-refresh").addEventListener("click", () => {
  paintAccounts().then(() => status("Список обновлён.")).catch(fail);
});

el("admin-back").addEventListener("click", () => {
  location.hash = "";
  render();
});

el("token-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  status("Выписываю ключ…");
  try {
    const out = await api("/api/tokens", {
      method: "POST",
      body: { label: el("token-label").value, days: Number(el("token-days").value) || 0 },
    });
    el("token-label").value = "";
    await paintTokens();
    // Ключ показывается один раз: в базе лежит только его отпечаток.
    el("token-fresh").hidden = false;
    el("token-value").value = out.token;
    status("Ключ готов — скопируйте его сейчас: потом посмотреть не получится.");
    el("token-value").focus();
    el("token-value").select();
  } catch (err) {
    fail(err);
  }
});

el("tokens-back").addEventListener("click", () => {
  location.hash = "";
  render();
});

// openInEditor уводит в mathmd на документе owner/path. Путь едет во фрагменте
// адреса: редактор читает его и сам идёт в API — с той же кукой, что уже
// есть у браузера. Если документа ещё нет, редактор откроет пустой лист с
// этим адресом, и Ctrl+S его заведёт.
function openInEditor(owner, path) {
  const base = (state.config && state.config.editor) || "";
  if (!base) {
    status("Не знаю адрес редактора — обновите страницу.");
    return;
  }
  location.href = window.mdcloudEditorUrl(base, owner, path);
}

el("edit").addEventListener("click", () => {
  openInEditor(state.doc.owner, state.doc.path);
});

el("new-doc-form").addEventListener("submit", (event) => {
  event.preventDefault();
  const path = cleanPath(el("new-doc-path").value);
  if (!path) return;
  openInEditor(event.currentTarget.dataset.owner, path);
});

// Переименование — это перенос по адресу: содержимое и комментарии остаются
// на месте, меняется только путь. Поле показываем по кнопке, чтобы оно не
// стояло поперёк документа, и заполняем текущим адресом — так его видно, и
// править можно кусок, а не набирать путь заново.
el("rename-toggle").addEventListener("click", () => {
  const form = el("rename-form");
  form.hidden = !form.hidden;
  if (form.hidden) return;
  el("rename-path").value = state.doc.path;
  el("rename-path").focus();
  el("rename-path").select();
  status("Новый адрес документа. Enter — перенести, Отмена — оставить как было.");
});

el("rename-cancel").addEventListener("click", () => {
  el("rename-form").hidden = true;
  el("rename-toggle").focus();
  status("Адрес не менялся.");
});

el("rename-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const owner = state.doc.owner;
  const from = state.doc.path;
  const to = cleanPath(el("rename-path").value);
  if (!to) {
    status("Напишите новый адрес.");
    return;
  }
  if (to === from) {
    el("rename-form").hidden = true;
    status("Адрес тот же — переносить нечего.");
    return;
  }
  try {
    const doc = await api(apiPath(owner, from), { method: "PATCH", body: { path: to } });
    // Адрес страницы тоже переезжает: иначе F5 вернул бы старую ссылку, и
    // «скопировать адрес» из браузера отдал бы документ по старому пути.
    history.replaceState(null, "", docHref(owner, doc.path, doc.slug));
    // Документ открывается по адресу в том состоянии, в котором его оставили
    // (см. openDoc), а переименование — правка: человек нажал «перенести» и
    // ждёт новое имя на экране, а не форму, раскрытую заново.
    justMoved = true;
    await openDoc(owner, doc.path);
    status("Документ перенесён: " + docAddress(doc));
  } catch (err) {
    fail(err);
  }
});

// Срок хранения — та же правка документа, что и видимость: PUT с одним полем,
// остальное не трогаем. Срок считается от сегодняшнего дня, поэтому в форме
// выбирают «неделю» или «месяц», а не дату в календаре.
//
// Поле подставляем по текущему сроку: у документа на месяц в форме стоит
// «месяц», а не первая строка списка — иначе «сохранить» без правок переселило
// бы срок на неделю.
el("expiry-toggle").addEventListener("click", () => {
  const form = el("expiry-form");
  form.hidden = !form.hidden;
  if (form.hidden) return;
  el("expiry-days").value = String(expiryDays(state.doc));
  el("expiry-days").focus();
  status("Сколько документ живёт. Enter — сохранить, Отмена — оставить как было.");
});

// expiryDays — ближайший к сроку документа пункт списка. Своих чисел в форме
// нет и не надо: «через 37 дней» человеку не нужно, а показать не то, что
// стоит, — хуже, чем показать приблизительно.
function expiryDays(doc) {
  if (!doc.expires_at) return 0;
  const left = Math.round((new Date(doc.expires_at).getTime() - Date.now()) / 86400000);
  if (left <= 0) return 0;
  const values = [...el("expiry-days").options]
    .map((o) => Number(o.value))
    .filter((v) => v > 0);
  let best = values[0];
  for (const v of values) if (Math.abs(v - left) < Math.abs(best - left)) best = v;
  return best;
}

el("expiry-cancel").addEventListener("click", () => {
  el("expiry-form").hidden = true;
  el("expiry-toggle").focus();
  status("Срок не менялся.");
});

el("expiry-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const days = Number(el("expiry-days").value) || 0;
  try {
    const doc = await api(apiPath(state.doc.owner, state.doc.path),
      { method: "PUT", body: { expires_in_days: days } });
    await openDoc(doc.owner, doc.path);
    status(days === 0
      ? "Документ остаётся насовсем."
      : "Документ удалится " + untilText(doc.expires_at) + ".");
  } catch (err) {
    fail(err);
  }
});

// Отправка документа человеку: юзернейм, и документ появляется у него в
// разделе «Со мной поделились». Публичным он от этого не становится —
// отправка адресная, а не публикация.
el("share-toggle").addEventListener("click", () => {
  const form = el("share-form");
  form.hidden = !form.hidden;
  if (form.hidden) return;
  el("share-username").value = "";
  el("share-username").focus();
  status("Кому отправить документ. Напишите юзернейм и нажмите Enter.");
});

// Подсказка к полю «кому отправить»: облако знает, кто в нём заведён, и
// подставляет совпавшие юзернеймы. Поле остаётся текстовым — набрать по памяти
// можно всегда, — а подсказка лишь помогает не ошибиться; список ведёт браузер
// (datalist), потому что это единственный вариант, который чтец экрана читает
// как «поле с подсказкой», не заставляя человека изучать новую навигацию.
// Спрашиваем не на каждую букву, а с задержкой: пока человек набирает, ответы
// всё равно устаревают.
let shareLookupTimer = 0;
let shareLookupSeq = 0;
el("share-username").addEventListener("input", () => {
  const q = el("share-username").value.trim();
  const list = el("people");
  const seq = ++shareLookupSeq;
  clearTimeout(shareLookupTimer);
  if (!q) {
    list.replaceChildren();
    return;
  }
  shareLookupTimer = setTimeout(async () => {
    let data;
    try {
      data = await api("/api/users?q=" + encodeURIComponent(q));
    } catch (err) {
      return; // подсказка не сработала — не повод ругаться: отправить можно и так
    }
    // Пока ходили в облако, человек набрал дальше: его подсказка уже в пути,
    // а эта опоздала.
    if (seq !== shareLookupSeq) return;
    list.replaceChildren();
    for (const name of data.users || []) {
      const option = document.createElement("option");
      option.value = name;
      list.append(option);
    }
  }, 250);
});

el("share-cancel").addEventListener("click", () => {
  el("share-form").hidden = true;
  el("share-toggle").focus();
  status("Никому не отправлено.");
});

el("share-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const username = el("share-username").value.trim();
  if (!username) {
    status("Напишите юзернейм.");
    return;
  }
  try {
    const res = await api("/api/share", {
      method: "POST",
      body: { owner: state.doc.owner, path: state.doc.path, username },
    });
    await openDoc(state.doc.owner, state.doc.path);
    status(res.message || "Документ отправлен.");
  } catch (err) {
    fail(err);
  }
});

// paintShareList — кому документ отправлен, с возможностью забрать обратно.
// Список стоит рядом с кнопкой и без раскрытия формы: хозяин должен видеть,
// у кого документ уже есть.
function paintShareList(names) {
  const list = el("share-list");
  list.replaceChildren();
  const block = el("share-block");
  block.hidden = names.length === 0;
  if (!names.length) return;
  el("share-summary").textContent = names.length === 1
    ? "Документ отправлен одному человеку:"
    : "Документ отправлен:";
  for (const name of names) {
    const li = document.createElement("li");
    const who = document.createElement("span");
    who.textContent = name;
    const take = document.createElement("button");
    take.type = "button";
    take.textContent = "Забрать";
    take.addEventListener("click", async () => {
      try {
        const res = await api(
          "/api/share?owner=" + encodeURIComponent(state.doc.owner) +
          "&path=" + encodeURIComponent(state.doc.path) +
          "&username=" + encodeURIComponent(name),
          { method: "DELETE" });
        await openDoc(state.doc.owner, state.doc.path);
        status(res.message || "Документ забран.");
      } catch (err) {
        fail(err);
      }
    });
    li.append(who, " ", take);
    list.append(li);
  }
}

// Режим доступа — выбор из трёх, а не переключатель: «по ссылке» посередине
// между «никто» и «все», и кнопкой-тумблером его не показать. Сохранение
// отдельной кнопкой: выбор стрелками в списке не должен менять документ на
// каждом нажатии — иначе, перебирая варианты, человек открывает его всему
// интернету и не замечает этого.
const visWords = {
  private: "Документ приватный: читаете вы и те, кому отправили.",
  link: "Документ открыт по ссылке: кто знает адрес, тот и читает. В чужих списках его нет.",
  public: "Документ публичный: открыт всем и стоит в вашем списке.",
};

el("vis-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const next = el("vis-select").value;
  if (next === state.doc.visibility) {
    status("Доступ не менялся — " + visLabel(next) + ".");
    return;
  }
  try {
    const doc = await api(apiPath(state.doc.owner, state.doc.path),
      { method: "PUT", body: { visibility: next } });
    state.doc = doc;
    await openDoc(doc.owner, doc.path);
    status(visWords[next] || "Доступ сохранён.");
  } catch (err) {
    fail(err);
  }
});

// Последний якорь в комментариях — кнопка формы или ссылка на строку: к нему
// ведёт Alt+B, откуда бы человек ни вернулся.
el("comments-block").addEventListener("focusin", (event) => {
  const anchor = event.target.closest && event.target.closest("button, a.line-ref");
  if (anchor) lastCommentAnchor = anchor;
});
// Позицию и выделение запоминаем, пока фокус ещё в поле: после ухода в документ
// поле их уже не отдаст.
for (const type of ["keyup", "click", "select", "input", "focus"]) {
  el("comment-body").addEventListener(type, commentRegion);
}

el("comment-line").addEventListener("click", () => {
  if (picker.on) cancelPicking();
  else startPicking();
});

// Enter или клик по блоку вставляет ссылку; вне выбора клик по документу
// ничего не делает — читать его можно спокойно.
el("doc-body").addEventListener("click", (event) => {
  const block = event.target.closest(".doc-block");
  if (!block || !picker.on) return;
  insertLineRef(Number(block.dataset.line));
  stopPicking();
});

el("doc-body").addEventListener("focusin", (event) => {
  const block = event.target.closest(".doc-block");
  if (!block) return;
  const index = docBlocks().indexOf(block);
  if (index >= 0) picker.index = index;
  if (picker.on) status("Строка " + block.dataset.line + ".");
});

document.addEventListener("keydown", (event) => {
  // Escape закрывает меню учётной записи, где бы внутри него ни стоял фокус,
  // и возвращает его на кнопку меню.
  if (event.key === "Escape" && accountMenuOpen()) {
    event.preventDefault();
    closeAccountMenu({ focus: true });
    return;
  }
  // Alt+B — назад к комментарию, где бы человек ни был: и после выбора строки,
  // и просто заглянув в документ. Код клавиши, а не буква: раскладка разная.
  if (event.altKey && event.code === "KeyB") {
    event.preventDefault();
    commentAnchor().focus();
    status("Вернулся к комментарию.");
    return;
  }
  // Alt+C — процитировать строку, на которой стоишь, не уходя за кнопкой:
  // человек читает документ, стоит на нужном блоке, нажимает — и ссылка встаёт
  // в комментарий туда, где он писал, а сам он оказывается в поле. Если фокус
  // не на строке документа, начинаем выбор стрелками — как кнопкой.
  // Код клавиши, а не буква: раскладка разная.
  if (event.altKey && event.code === "KeyC") {
    const block = document.activeElement && document.activeElement.closest
      ? document.activeElement.closest(".doc-block")
      : null;
    if (block) {
      event.preventDefault();
      insertLineRef(Number(block.dataset.line));
      return;
    }
    if (!el("comment-form").hidden) {
      event.preventDefault();
      startPicking();
      return;
    }
  }
  if (!picker.on) return;
  const blocks = docBlocks();
  switch (event.key) {
    case "ArrowDown": event.preventDefault(); movePick(1); break;
    case "ArrowUp": event.preventDefault(); movePick(-1); break;
    case "PageDown": event.preventDefault(); movePick(10); break;
    case "PageUp": event.preventDefault(); movePick(-10); break;
    case "Home": event.preventDefault(); picker.index = 0; blocks[0]?.focus(); break;
    case "End": event.preventDefault(); picker.index = blocks.length - 1; blocks.at(-1)?.focus(); break;
    case "Enter": {
      event.preventDefault();
      const block = blocks[picker.index];
      if (block) {
        // После вставки фокус остаётся в комментарии, у самой ссылки: человек
        // дописывает дальше с того же места (insertLineRef ставит туда курсор).
        insertLineRef(Number(block.dataset.line));
        stopPicking();
      }
      break;
    }
    case "Escape":
      event.preventDefault();
      cancelPicking();
      break;
    default:
      break;
  }
});

el("comment-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const base = event.currentTarget.dataset.base;
  const body = {
    body: el("comment-body").value,
    name: el("comment-name").value,
  };
  try {
    await api(base, { method: "POST", body });
    el("comment-body").value = "";
    status("Комментарий отправлен.");
    await loadComments(state.doc.owner, state.doc);
  } catch (err) {
    fail(err);
  }
});

// ------------------------------------------------------------------ старт

(async function start() {
  // Кто пришёл — знает только сервер: сессия в куке, из JS её не видно.
  try {
    const me = await api("/api/me");
    state.user = me.user;
  } catch {
    state.user = null;
  }
  try {
    state.config = await api("/api/config");
  } catch {
    // без настроек страница всё равно работает: покажем то, что есть
  }
  paintAuth();

  await render();
})();
