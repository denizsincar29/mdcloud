// mdcloud — предпросмотр облачных markdown-документов.
//
// Страница показывает документ и умеет позвать редактор: кнопка
// «Редактировать» открывает mathmd на этом документе. Сессия живёт в
// httpOnly-куке на общем домене, поэтому редактору не нужно её получать —
// он просто ходит в API, и браузер прикладывает куку сам.
//
// Разметка документа приходит из облака как обычный markdown, а не как
// готовый HTML: рендерер здесь один, и он пропускает результат через
// строгий список разрешённого. Скрипты из markdown не выполняются никогда.

const API = "";

const state = {
  user: null,
  doc: null,
  renderers: null,
  config: null, // что сервер рассказал про регистрацию
  invites: [],
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
  if (!state.renderers) {
    const [showdownMod, purifyMod] = await Promise.all([
      import("https://cdn.jsdelivr.net/npm/showdown@2.1.0/+esm"),
      import("https://cdn.jsdelivr.net/npm/dompurify@3/+esm"),
    ]);
    const showdown = new (showdownMod.default || showdownMod) .Converter({
      tables: true,
      tasklists: true,
      simplifiedAutoLink: true,
      strikethrough: true,
      headerLevelStart: 2,
      extensions: [chessExtension],
    });
    state.renderers = { showdown, DOMPurify: purifyMod.default || purifyMod };
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

// Список разрешённого. Всё, чего здесь нет, до страницы не доедет:
// ни <script>, ни <iframe>, ни обработчиков вида onclick.
const SANITIZE = {
  FORBID_TAGS: [
    "script", "style", "iframe", "object", "embed", "form", "input",
    "button", "textarea", "select", "link", "meta", "base", "audio", "video", "source",
  ],
  FORBID_ATTR: ["style", "srcset", "formaction", "ping"],
  ADD_TAGS: ["chessjax-board"],
  CUSTOM_ELEMENT_HANDLING: {
    tagNameCheck: /^chessjax-board$/,
    attributeNameCheck: () => true, // атрибуты доски: fen, pgn, id, lang, move, chess…
    allowCustomizedBuiltInElements: false,
  },
  ALLOW_DATA_ATTR: false,
};

async function renderMarkdown(markdown) {
  const { showdown, DOMPurify } = await renderers();
  const html = showdown.makeHtml(markdown || "");
  return DOMPurify.sanitize(html, SANITIZE);
}

// ------------------------------------------------------------------ шапка

function paintAuth() {
  const logged = Boolean(state.user);
  el("who").hidden = !logged;
  el("who").textContent = logged ? state.user.username : "";
  el("logout").hidden = !logged;
  el("login-toggle").hidden = logged;
  el("invites-toggle").hidden = !(logged && state.user.is_admin);
}

// ------------------------------------------------------------------ экраны

const SCREENS = ["login", "register", "index", "doc", "invites"];

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
  const byInvite = mode === "invite" || mode === "first";
  el("register-invite-row").hidden = !byInvite;
  el("register-toggle").hidden = mode === "closed";
  el("register-hint").textContent = {
    first: "Вы первый — регистрируйтесь, и облако станет вашим: приглашения выдаёте вы.",
    open: "Регистрация открыта для всех.",
    invite: "Регистрация по приглашению: вставьте код из ссылки, которую вам прислали.",
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

// openRegister показывает форму регистрации. Код можно принести ссылкой
// вида mdcloud.denizsincar.ru/#invite=КОД — фрагмент адреса на сервер не
// уходит, поэтому в логах он не осядет.
async function openRegister(invite) {
  show("register");
  status("");
  paintRegister();
  el("register-invite").value = invite || "";
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
  for (const doc of data.docs) {
    const li = document.createElement("li");
    const a = document.createElement("a");
    a.href = "/" + owner + "/" + doc.path.split("/").map(encodeURIComponent).join("/");
    a.textContent = doc.title || doc.path;
    li.append(a);
    if (doc.visibility !== "public") {
      const mark = document.createElement("span");
      mark.className = "meta";
      mark.textContent = " — закрытый";
      li.append(mark);
    }
    list.append(li);
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
  const doc = await api("/api/docs/" + encodeURIComponent(owner) + "/" +
    path.split("/").map(encodeURIComponent).join("/"));
  state.doc = doc;

  el("doc-title").textContent = doc.title || doc.path;
  document.title = (doc.title || doc.path) + " — mdcloud";
  el("doc-meta").textContent = [
    owner,
    doc.visibility === "public" ? "публичный" : "закрытый",
    "обновлён " + new Date(doc.updated_at).toLocaleString("ru-RU"),
  ].join(" · ");

  const body = el("doc-body");
  try {
    body.innerHTML = await renderMarkdown(doc.content);
  } catch (err) {
    body.replaceChildren();
    const pre = document.createElement("pre");
    pre.textContent = doc.content || "";
    body.append(pre);
    status("Не загрузился рендерер, показываю исходный текст. " + err.message);
  }

  el("edit").hidden = !doc.can_edit;
  el("toggle-vis").hidden = !doc.can_edit;
  if (doc.can_edit) {
    el("toggle-vis").textContent =
      doc.visibility === "public" ? "Сделать закрытым" : "Сделать публичным";
  }

  await loadComments(owner, doc);
  el("main").focus();
}

async function loadComments(owner, doc) {
  const base = "/api/comments/" + encodeURIComponent(owner) + "/" +
    doc.path.split("/").map(encodeURIComponent).join("/");
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
    body.textContent = c.body; // только текстом: разметке в комментариях не место
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
  el("comment-hint").textContent = data.require_auth
    ? "Здесь пишут только вошедшие."
    : data.viewer_authenticated
      ? "Вы вошли — подпись возьмётся сама."
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
el("invites-toggle").addEventListener("click", () => {
  location.hash = "#invites";
  render();
});
el("logout").addEventListener("click", async () => {
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
        invite: el("register-invite").value.trim(),
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
  // Путь чистим так же, как редактор при сохранении: пробелы и лишние слэши
  // сервер не примет, поэтому приводим адрес в порядок здесь, а не отказом
  // потом, когда человек уже написал документ.
  const path = el("new-doc-path").value.trim()
    .replace(/^\/+/, "")
    .replace(/\s+/g, "-")
    .split("/")
    .filter(Boolean)
    .join("/");
  if (!path) return;
  openInEditor(event.currentTarget.dataset.owner, path);
});

el("toggle-vis").addEventListener("click", async () => {
  const next = state.doc.visibility === "public" ? "private" : "public";
  try {
    const doc = await api(
      "/api/docs/" + encodeURIComponent(state.doc.owner) + "/" +
        state.doc.path.split("/").map(encodeURIComponent).join("/"),
      { method: "PUT", body: { visibility: next } }
    );
    state.doc = doc;
    status(next === "public" ? "Документ открыт для всех." : "Документ закрыт.");
    await openDoc(doc.owner, doc.path);
    status(next === "public" ? "Документ открыт для всех." : "Документ закрыт.");
  } catch (err) {
    fail(err);
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
