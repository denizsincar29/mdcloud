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
  invites: [],
  tokens: [],
  invite: "", // код из ссылки-приглашения, с которой пришёл человек
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

// docHref — адрес документа на сайте: владелец и каждый сегмент пути
// экранируются по отдельности (в именах бывает кириллица и «/» в сегменте).
function docHref(owner, path) {
  return "/" + ownerPath("", owner, path);
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
    // Шахматный компонент — тот же, что в mathmd.
    import("https://cdn.jsdelivr.net/gh/denizsincar29/chessjax@v0.8.0/chessjax.js").catch(() => {});
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
// контейнер: сам график — это чужой SDK и iframe, и грузить его ради документа
// без графиков незачем (см. initDesmos). Тело блока едет в data-атрибуте
// целиком закодированным — строки выражений не должны разбираться как
// разметка. Кнопку входа (см. DESMOS_ENTER) ставит initDesmos: она должна
// стоять рядом с контейнером, а не внутри абзаца, которым showdown окружил бы
// строчную разметку.
const desmosExtension = {
  type: "lang",
  filter(text) {
    return text.replace(/```desmos[^\n]*\n([\s\S]*?)```/g, (_, body) =>
      `<div class="desmos" data-desmos-body="${encodeURIComponent(body)}"></div>`);
  },
};

// ------------------------------------------------------------------ Desmos

// График Desmos — тот же API и тот же ключ, что в редакторе mathmd: один
// документ должен выглядеть в облаке и в редакторе одинаково.
const DESMOS_SRC =
  "https://www.desmos.com/api/v1.10/calculator.js?apiKey=dcb31709b452b1cf9dc26972add0fda6";

let desmosLoading = null;

// SDK грузим лениво и один раз — как MathJax. Ключ в адресе публичный,
// демонстрационный: тот же, что стоит в редакторе.
function loadDesmos() {
  // SDK уже на странице (или подставлен тестом) — второй раз не грузим.
  if (window.Desmos && typeof window.Desmos.Calculator === "function") {
    return Promise.resolve(window.Desmos);
  }
  if (!desmosLoading) {
    desmosLoading = new Promise((resolve, reject) => {
      const script = document.createElement("script");
      script.src = DESMOS_SRC;
      script.async = true;
      script.onload = () => resolve(window.Desmos);
      script.onerror = () => reject(new Error("Desmos не загрузился"));
      document.head.append(script);
    });
  }
  return desmosLoading;
}

function desmosFallback(spot, why) {
  spot.replaceChildren();
  const span = document.createElement("span");
  span.className = "desmos-fallback";
  span.textContent = why;
  spot.append(span);
}

// Кнопка входа перед графиком. График — чужой iframe: Tab внутрь не доводит,
// стрелки его не трогают, и без такой кнопки незрячему в калькулятор не
// попасть. Кнопка невидимая, но стоит в потоке фокуса: скринридер читает её
// как «График Desmos — перейти к списку выражений, кнопка», а Enter (пробел)
// переводит фокус внутрь, сразу в список выражений. Пока графика нет, кнопки
// тоже нет — обещать вход в то, чего не построилось, незачем.
function desmosEnterButton(calc, spot) {
  const enter = document.createElement("button");
  enter.type = "button";
  enter.className = "desmos-enter";
  enter.style.cssText =
    "position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0 0 0 0);white-space:nowrap";
  enter.textContent = "График Desmos — перейти к списку выражений, кнопка";
  enter.addEventListener("click", () => enterDesmos(calc, spot));
  spot.before(enter);
  return enter;
}

// Вход в график: штатный метод Desmos ставит фокус в список выражений; если
// версия API его не знает, фокусируем сам iframe — тогда до списка дойдёт Tab.
function enterDesmos(calc, spot) {
  if (calc && typeof calc.focusFirstExpression === "function") {
    try {
      calc.focusFirstExpression();
      return;
    } catch (err) {
      console.warn("[mdcloud] вход в график Desmos:", err);
    }
  }
  const frame = spot.querySelector("iframe");
  if (frame) frame.focus();
}

// initDesmos наполняет контейнеры графиков, которые поставило расширение
// showdown. График не должен ронять показ документа: текст без графика
// полезнее пустого экрана, поэтому о неудаче говорим в статусе и живём дальше.
async function initDesmos(root) {
  const spots = [...root.querySelectorAll(".desmos[data-desmos-body]")];
  if (!spots.length) return;
  let Desmos;
  try {
    Desmos = await loadDesmos();
  } catch (err) {
    spots.forEach((spot) => desmosFallback(spot, "График Desmos не загрузился."));
    status("Графики Desmos не загрузились: " + err.message);
    return;
  }
  for (const spot of spots) {
    const body = decodeURIComponent(spot.dataset.desmosBody || "");
    if (!body.trim()) continue;
    try {
      const calc = Desmos.Calculator(spot, {
        expressions: true,
        settingsMenu: false,
        border: false,
        projectorMode: true,
      });
      body
        .split("\n")
        .map((s) => s.trim())
        .filter(Boolean)
        .forEach((expr, i) => {
          try {
            calc.setExpression({ id: "e" + i, latex: expr });
          } catch (err) {
            console.warn("[mdcloud] выражение Desmos не распознано:", expr, err);
          }
        });
      // График готов — открываем вход: кнопка встаёт перед контейнером.
      desmosEnterButton(calc, spot);
    } catch (err) {
      desmosFallback(spot, "График Desmos не построился.");
      console.error("[mdcloud] не удалось создать график Desmos:", err);
    }
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

function startPicking() {
  const blocks = docBlocks();
  if (!blocks.length) {
    status("Строк пока нет — документ ещё не отрисован.");
    return;
  }
  picker.on = true;
  picker.index = 0;
  blocks[0].focus();
  status("Выбираю строку: стрелки вверх и вниз — по строкам, Enter — вставить ссылку, Escape — отмена.");
}

function stopPicking() {
  picker.on = false;
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

// Учётная запись — меню, а не ряд кнопок: «Выйти» рядом с «Приглашениями»
// слишком легко нажать мимо. Кнопка в шапке раскрывает список, Escape и клик
// в стороне его закрывают, при раскрытии фокус сразу встаёт на первый пункт —
// чтец экрана читает «меню раскрыто, Приглашения, кнопка».
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
  el("invites-toggle").hidden = !(logged && state.user.is_admin);
  if (!logged) closeAccountMenu();
}

// ------------------------------------------------------------------ экраны

const SCREENS = ["login", "register", "index", "doc", "invites", "tokens"];

function show(id) {
  for (const name of SCREENS) el(name).hidden = name !== id;
}

// registration — что можно рассказать про регистрацию по ответу /api/config:
// первый (место хозяина свободно), открытая, по приглашению или закрыта.
function registration() {
  return (state.config && state.config.registration) || "closed";
}

function paintRegister() {
  const mode = registration();
  el("register-toggle").hidden = mode === "closed";
  el("register-hint").textContent = {
    first: "Вы первый — регистрируйтесь, и облако станет вашим: приглашения выдаёте вы.",
    open: "Регистрация открыта для всех.",
    invite: "Регистрация по приглашению: откройте ссылку, которую вам прислали, — форма откроется сама.",
    closed: "Регистрация закрыта. Попросите приглашение у хозяина облака.",
  }[mode];
}

async function openLogin(message) {
  show("login");
  status(message || "");
  paintRegister();
  // На первом запуске логиниться некому — сразу к регистрации.
  if (registration() === "first") {
    await openRegister("");
    return;
  }
  el("login-name").focus();
}

// openRegister показывает форму регистрации. Приглашение приходит ссылкой
// вида mdcloud.denizsincar.ru/#invite=КОД — фрагмент адреса на сервер не
// уходит, поэтому в логах он не осядет. Отдельного поля для кода нет: человеку
// нечего вставлять руками, он просто открывает ссылку.
async function openRegister(invite) {
  show("register");
  status("");
  paintRegister();
  state.invite = invite || "";
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
function docRow(owner, doc, folder) {
  const li = document.createElement("li");
  const a = document.createElement("a");
  a.href = docHref(owner, doc.path);
  const leaf = folder === "/" ? doc.path : doc.path.slice(folder.length + 1);
  a.textContent = doc.title || leaf;
  li.append(a);
  if (doc.visibility !== "public") {
    const mark = document.createElement("span");
    mark.className = "meta";
    mark.textContent = " — закрытый";
    li.append(mark);
  }
  li.addEventListener("click", (event) => {
    if (event.target.closest("a")) return; // по ссылке — обычный переход
    a.click();
  });
  return li;
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
  el("main").focus();
}

async function openDoc(owner, path) {
  show("doc");
  const doc = await api(apiPath(owner, path));
  state.doc = doc;

  el("doc-title").textContent = doc.title || doc.path;
  document.title = (doc.title || doc.path) + " — mdcloud";
  el("doc-meta").textContent = [
    "Адрес: " + owner + "/" + doc.path,
    doc.visibility === "public" ? "публичный" : "закрытый",
    "обновлён " + new Date(doc.updated_at).toLocaleString("ru-RU"),
  ].join(" · ");

  const body = el("doc-body");
  try {
    await paintDocument(doc.content, body);
    await typesetMath(body);
    // Графики достраиваются в фоне: SDK Desmos тяжелее документа, и ждать его
    // ради того, чтобы отдать страницу, незачем.
    initDesmos(body).catch((err) => status("Графики Desmos: " + err.message));
  } catch (err) {
    body.replaceChildren();
    const pre = document.createElement("pre");
    pre.textContent = doc.content || "";
    body.append(pre);
    status("Не загрузился рендерер, показываю исходный текст. " + err.message);
  }

  el("edit").hidden = !doc.can_edit;
  el("toggle-vis").hidden = !doc.can_edit;
  el("rename-toggle").hidden = !doc.can_edit;
  el("rename-form").hidden = true;
  if (doc.can_edit) {
    el("toggle-vis").textContent =
      doc.visibility === "public" ? "Сделать закрытым" : "Сделать публичным";
  }

  await loadComments(owner, doc);
  el("main").focus();
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

// ------------------------------------------------------------------ приглашения

async function openInvites() {
  show("invites");
  status("");
  const data = await api("/api/invites");
  state.invites = data.invites || [];
  const list = el("invites-list");
  list.replaceChildren();
  for (const inv of state.invites) {
    const li = document.createElement("li");
    const name = inv.note ? inv.note : "без пометки";
    const when = new Date(inv.expires_at).toLocaleDateString("ru-RU");
    const head = document.createElement("p");
    head.textContent = inv.state === "used"
      ? `${name} — использовано: ${inv.used_by || "кто-то"}, ${new Date(inv.used_at).toLocaleDateString("ru-RU")}`
      : inv.state === "expired"
        ? `${name} — просрочено ${when}`
        : `${name} — действует до ${when}`;
    li.append(head);
    if (inv.state !== "used") {
      const del = document.createElement("button");
      del.type = "button";
      del.textContent = `Отозвать приглашение ${name}`;
      del.addEventListener("click", async () => {
        try {
          await api("/api/invites/" + inv.id, { method: "DELETE" });
          await openInvites();
          status("Приглашение отозвано.");
        } catch (err) {
          fail(err);
        }
      });
      li.append(del);
    }
    list.append(li);
  }
  if (!state.invites.length) {
    const li = document.createElement("li");
    li.className = "meta";
    li.textContent = "Приглашений пока нет.";
    list.append(li);
  }
  el("main").focus();
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
    if (location.hash === "#invites" && state.user && state.user.is_admin) {
      await openInvites();
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
el("invites-toggle").addEventListener("click", () => {
  closeAccountMenu();
  location.hash = "#invites";
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

el("register-toggle").addEventListener("click", () => openRegister(""));
el("register-back").addEventListener("click", () => openLogin(""));

el("register-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  status("Создаю аккаунт…");
  try {
    const out = await api("/api/auth/register", {
      method: "POST",
      body: {
        username: el("register-name").value,
        email: el("register-mail").value,
        password: el("register-pass").value,
        invite: state.invite || "",
      },
    });
    state.user = out.user;
    paintAuth();
    el("register-pass").value = "";
    // Код во фрагменте больше не нужен — убираем из адреса.
    if (location.hash.startsWith("#invite=")) history.replaceState(null, "", "/");
    status("Здравствуйте, " + out.user.username + ".");
    await render();
  } catch (err) {
    fail(err);
  }
});

el("invite-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  status("Выписываю приглашение…");
  try {
    const out = await api("/api/invites", {
      method: "POST",
      body: { note: el("invite-note").value, days: Number(el("invite-days").value) || 0 },
    });
    el("invite-note").value = "";
    await openInvites();
    // Ссылка показывается один раз: в базе лежит только хеш кода.
    el("invite-fresh").hidden = false;
    el("invite-link").value = out.url;
    status("Ссылка готова — скопируйте её сейчас: потом показать не получится.");
    el("invite-link").focus();
    el("invite-link").select();
  } catch (err) {
    fail(err);
  }
});

el("invites-back").addEventListener("click", () => {
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
    history.replaceState(null, "", docHref(owner, doc.path));
    await openDoc(owner, doc.path);
    status("Документ перенесён: " + doc.path);
  } catch (err) {
    fail(err);
  }
});

el("toggle-vis").addEventListener("click", async () => {
  const next = state.doc.visibility === "public" ? "private" : "public";
  try {
    const doc = await api(apiPath(state.doc.owner, state.doc.path),
      { method: "PUT", body: { visibility: next } });
    state.doc = doc;
    status(next === "public" ? "Документ открыт для всех." : "Документ закрыт.");
    await openDoc(doc.owner, doc.path);
    status(next === "public" ? "Документ открыт для всех." : "Документ закрыт.");
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

el("comment-line").addEventListener("click", startPicking);

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
      stopPicking();
      commentAnchor().focus();
      status("Выбор строки отменил.");
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

  // Ссылка-приглашение: код едет во фрагменте, поэтому сразу открываем
  // регистрацию с уже подставленным кодом.
  const hash = location.hash.slice(1);
  if (location.pathname === "/" && hash.startsWith("invite=")) {
    await openRegister(decodeURIComponent(hash.slice("invite=".length)));
    return;
  }
  await render();
})();
