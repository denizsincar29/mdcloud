// mdcloud — предпросмотр облачных markdown-документов.
//
// Страница только читает. Вся правка живёт в редакторе mathmd: кнопка
// «Редактировать» берёт одноразовый код и уводит туда браузер. Куки между
// сайтами не делятся ни в одну сторону — только токен в заголовке.
//
// Разметка документа приходит из облака как обычный markdown, а не как
// готовый HTML: рендерер здесь один, и он пропускает результат через
// строгий список разрешённого. Скрипты из markdown не выполняются никогда.

const API = "";

const state = {
  token: localStorage.getItem("mdcloud_token") || "",
  user: null,
  doc: null,
  renderers: null,
};

const el = (id) => document.getElementById(id);

function status(text) {
  el("status").textContent = text || "";
}

function fail(err) {
  status(err && err.message ? err.message : String(err));
}

async function api(path, opts = {}) {
  const headers = {};
  const init = { method: opts.method || "GET", headers };
  if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(opts.body);
  }
  if (state.token) headers["Authorization"] = "Bearer " + state.token;

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
    if (resp.status === 401 && state.token) setToken("");
    throw new Error(data.error || "ошибка " + resp.status);
  }
  return data;
}

function setToken(token) {
  state.token = token || "";
  if (token) localStorage.setItem("mdcloud_token", token);
  else localStorage.removeItem("mdcloud_token");
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
}

// ------------------------------------------------------------------ экраны

function show(id) {
  for (const name of ["login", "index", "doc"]) el(name).hidden = name !== id;
}

// На главной без входа показывать нечего — прячем все экраны.
function showNothing() {
  show(null);
}

async function openLogin(message) {
  show("login");
  el("status").textContent = message || "";
  el("login-name").focus();
}

async function openIndex(owner) {
  show("index");
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
    if (r.kind === "home") {
      if (state.token) {
        const me = await api("/api/me");
        state.user = me.user;
        paintAuth();
        await openIndex(me.user.username);
      } else {
        showNothing();
        status("Укажите адрес документа: /имя/папка/файл. Или войдите в шапке страницы.");
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
    if (err.message && err.message.includes("нужен вход")) await openLogin(err.message);
  }
}

// ------------------------------------------------------------------ события

el("login-toggle").addEventListener("click", () => openLogin(""));
el("logout").addEventListener("click", async () => {
  try {
    await api("/api/auth/logout", { method: "POST" });
  } catch {
    // токен всё равно стираем — выйти должно получиться всегда
  }
  setToken("");
  state.user = null;
  paintAuth();
  status("Вы вышли.");
  el("login").hidden = true;
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
    setToken(out.token);
    state.user = out.user;
    paintAuth();
    el("login-pass").value = "";
    status("Здравствуйте, " + out.user.username + ".");
    await render();
  } catch (err) {
    fail(err);
  }
});

el("edit").addEventListener("click", async () => {
  status("Готовлю переход в редактор…");
  try {
    const out = await api("/api/handoff", { method: "POST", body: { path: state.doc.path } });
    // Код едет во фрагменте: на сервер он не попадёт.
    location.href = out.url;
  } catch (err) {
    fail(err);
  }
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
  if (state.token) {
    try {
      const me = await api("/api/me");
      state.user = me.user;
    } catch {
      setToken("");
    }
  }
  paintAuth();
  await render();
})();
