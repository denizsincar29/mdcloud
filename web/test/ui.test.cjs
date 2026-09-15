// Проверка страницы облака без браузера: jsdom + подставной fetch.
// Смотрим то, что руками не проверить: что кнопки действительно ходят в API
// и что экраны переключаются — регистрация, приглашения, вход по ссылке.
//
// Запуск: npm install -g jsdom && node web/test/ui.test.cjs

const fs = require("node:fs");
const path = require("node:path");
// jsdom ставится глобально (npm install -g jsdom) и потому не находится
// обычным require — ищем его там, где живёт глобальный префикс npm.
function loadJsdom() {
  try {
    return require("jsdom");
  } catch (err) {
    const { execSync } = require("node:child_process");
    const root = execSync("npm root -g").toString().trim();
    return require(path.join(root, "jsdom"));
  }
}
const { JSDOM, VirtualConsole } = loadJsdom();

// jsdom не умеет переходы на другую страницу и ругается на них в консоль.
// Нам переходы и не нужны: адрес редактора проверяем через
// window.mdcloudEditorUrl, а сам «уход» глушим.
function quietConsole() {
  const vc = new VirtualConsole();
  vc.on("jsdomError", (err) => {
    if (!/Not implemented: navigation/.test(err.message)) console.error(err.message);
  });
  return vc;
}

const WEB = path.join(__dirname, "..");
const html = fs.readFileSync(path.join(WEB, "index.html"), "utf8");
const app = fs.readFileSync(path.join(WEB, "app.js"), "utf8");

let failed = 0;
function ok(name, cond, extra) {
  console.log((cond ? "ok   " : "FAIL ") + name + (cond || !extra ? "" : " — " + extra));
  if (!cond) failed++;
}

// --- подставной сервер ------------------------------------------------------
function makeApi(overrides = {}) {
  const calls = [];
  const routes = {
    "GET /api/config": () => ({ status: 200, body: { registration: "first", cloud: "https://mdcloud.denizsincar.ru", editor: "https://mathmd.denizsincar.ru" } }),
    "GET /api/me": () => ({ status: 401, body: { error: "нужен вход" } }),
    "POST /api/auth/register": () => ({ status: 201, body: { user: { username: "deniz", is_admin: true } } }),
    "POST /api/auth/login": () => ({ status: 200, body: { user: { username: "deniz", is_admin: true } } }),
    "POST /api/auth/logout": () => ({ status: 200, body: { ok: true } }),
    "GET /api/docs/deniz": () => ({ status: 200, body: { docs: [] } }),
    "POST /api/invites": () => ({ status: 201, body: { id: 7, note: "Маше", code: "KOD", url: "https://mdcloud.denizsincar.ru/#invite=KOD" } }),
    "GET /api/invites": () => ({ status: 200, body: { invites: [{ id: 7, note: "Маше", state: "active", created_at: "2026-09-15T10:00:00Z", expires_at: "2026-09-29T10:00:00Z" }] } }),
    "DELETE /api/invites/7": () => ({ status: 200, body: { ok: true } }),
    ...overrides,
  };
  return {
    calls,
    fetch: async (url, init) => {
      const method = (init && init.method) || "GET";
      // Страница экранирует путь посегментно, а маршруты в тесте записаны
      // как есть: сводим их вместе разбором процентов, иначе кириллический
      // адрес не находил бы свою заглушку.
      const key = method + " " + decodeURIComponent(url);
      calls.push({ key, init });
      const route = routes[key];
      if (!route) return { ok: false, status: 404, text: async () => '{"error":"нет такого маршрута: ' + key + '"}' };
      const r = route();
      return {
        ok: r.status >= 200 && r.status < 300,
        status: r.status,
        text: async () => JSON.stringify(r.body),
      };
    },
  };
}

// boot поднимает страницу: index.html + app.js в одном окне jsdom.
// hooks подставляет то, чего в jsdom нет: модуль md.mjs, showdown и MathJax
// грузятся страницей по сети, а тест даёт их заглушками через window.mdcloud*.
async function boot({ path = "/", hash = "", api, hooks } = {}) {
  const dom = new JSDOM(html, {
    url: "https://mdcloud.denizsincar.ru" + path + hash,
    runScripts: "outside-only",
    pretendToBeVisual: true,
    virtualConsole: quietConsole(),
  });
  const w = dom.window;
  Object.assign(w, hooks || {});
  const stub = api || makeApi();
  w.fetch = stub.fetch;
  w.eval(app);
  const tick = () => new Promise((r) => setTimeout(r, 30));
  await tick(); // start(): /api/me, /api/config, первый render
  return { dom, w, stub, tick, $: (id) => w.document.getElementById(id) };
}

const submit = (w, form) =>
  form.dispatchEvent(new w.Event("submit", { bubbles: true, cancelable: true }));
const click = (w, el) => el.dispatchEvent(new w.Event("click", { bubbles: true }));
const key = (w, keyName, init = {}) =>
  w.document.dispatchEvent(new w.KeyboardEvent("keydown", { key: keyName, bubbles: true, cancelable: true, ...init }));

async function main() {
  // --- 1. Свежее облако: сразу форма регистрации ------------------------------
  {
    const { w, $, stub, tick } = await boot();
    ok("на новом облаке открыта регистрация, а не вход", !$("register").hidden && $("login").hidden);
    ok("в подсказке сказано, что это первый аккаунт", /первый/i.test($("register-hint").textContent));

    $("register-name").value = "deniz";
    $("register-pass").value = "parol1234";
    submit(w, $("register-form"));
    await tick();
    const call = stub.calls.find((c) => c.key === "POST /api/auth/register");
    ok("форма регистрации ушла в API", Boolean(call), JSON.stringify(stub.calls.map((c) => c.key)));
    ok(
      "в теле регистрации — логин и пароль, без кода",
      call && call.init.body.includes("deniz") && call.init.body.includes("parol1234") && !call.init.body.includes("invite\":\"K"),
      call && call.init.body
    );
    ok("после регистрации видно документы", !$("index").hidden && $("register").hidden);
    ok("кнопка учётной записи появилась", $("account-toggle").hidden === false);
    ok("имя владельца на кнопке", /deniz/.test($("account-toggle").textContent), $("account-toggle").textContent);
    ok("меню учётной записи закрыто", $("account-menu").hidden === true);
    ok("пункт приглашений есть (хозяин)", $("invites-toggle").hidden === false);
  }

  // --- 2. Ссылка-приглашение подставляет код ---------------------------------
  {
    const api = makeApi();
    const { $, w, tick, stub } = await boot({ hash: "#invite=KOD42", api });
    ok("по ссылке открылась регистрация", !$("register").hidden);
    // Кода в форме нет: приглашение приезжает ссылкой и едет в теле запроса
    // само — вставлять руками нечего.
    ok("поля для кода в форме нет", !w.document.getElementById("register-invite"));
    $("register-name").value = "vasilisa";
    $("register-pass").value = "parol1234";
    submit(w, $("register-form"));
    await tick();
    const call = stub.calls.find((c) => c.key === "POST /api/auth/register");
    ok("код уехал в теле регистрации", call && call.init.body.includes('"invite":"KOD42"'), call && call.init.body);
    ok("код убран из адреса после регистрации", w.location.hash === "");
  }

  // --- 3. Приглашения: выписать, увидеть, отозвать ---------------------------
  {
    const api = makeApi({ "GET /api/me": () => ({ status: 200, body: { user: { username: "deniz", is_admin: true } } }) });
    const { $, w, tick, stub } = await boot({ api });
    ok("вошедший видит свои документы", !$("index").hidden);

    click(w, $("account-toggle"));
    ok("кнопка раскрыла меню", $("account-menu").hidden === false);
    ok("чтец экрана узнает, что меню раскрыто",
      $("account-toggle").getAttribute("aria-expanded") === "true",
      $("account-toggle").getAttribute("aria-expanded"));
    ok("фокус встал на первый пункт меню",
      w.document.activeElement === $("tokens-toggle"), w.document.activeElement.id);

    key(w, "Escape");
    ok("Escape закрыл меню", $("account-menu").hidden === true);
    ok("фокус вернулся на кнопку",
      w.document.activeElement === $("account-toggle"), w.document.activeElement.id);

    click(w, $("account-toggle"));
    click(w, w.document.getElementById("main"));
    ok("клик в стороне закрыл меню", $("account-menu").hidden === true);

    click(w, $("account-toggle"));
    click(w, $("invites-toggle"));
    await tick();
    ok("выбор пункта закрыл меню", $("account-menu").hidden === true);
    ok("открылся раздел приглашений", !$("invites").hidden);
    ok("список приглашений запрошен", stub.calls.some((c) => c.key === "GET /api/invites"));
    ok("видно, кому выписали", /Маше/.test($("invites-list").textContent), $("invites-list").textContent);

    $("invite-note").value = "Маше";
    $("invite-days").value = "14";
    submit(w, $("invite-form"));
    await tick();
    const call = stub.calls.find((c) => c.key === "POST /api/invites");
    ok("приглашение выписано через API", Boolean(call), JSON.stringify(stub.calls.map((c) => c.key)));
    ok("ссылка показана", !$("invite-fresh").hidden && $("invite-link").value.includes("#invite="), $("invite-link").value);
    ok("сказано, что ссылку потом не показать",
      /показать не получится/.test($("status").textContent), $("status").textContent);

    const revoke = [...w.document.querySelectorAll("#invites-list button")][0];
    ok("у живого приглашения есть кнопка отзыва", Boolean(revoke), $("invites-list").innerHTML);
    click(w, revoke);
    await tick();
    ok("отзыв ушёл в API", stub.calls.some((c) => c.key === "DELETE /api/invites/7"), JSON.stringify(stub.calls.map((c) => c.key)));
  }

  // --- 3б. API-токены: выписать, увидеть, отозвать ---------------------------
  {
    const api = makeApi({
      "GET /api/me": () => ({ status: 200, body: { user: { username: "deniz", is_admin: true } } }),
      "GET /api/tokens": () => ({
        status: 200,
        body: {
          tokens: [
            {
              id: 9, label: "дайджест", state: "active",
              created_at: "2026-09-15T10:00:00Z", expires_at: "2026-10-15T10:00:00Z",
              last_used_at: null,
            },
          ],
        },
      }),
      "POST /api/tokens": () => ({ status: 201, body: { id: 10, label: "вечный", token: "mdk-secret" } }),
      "DELETE /api/tokens/9": () => ({ status: 200, body: { ok: true } }),
    });
    const { $, w, tick, stub } = await boot({ api });

    click(w, $("account-toggle"));
    click(w, $("tokens-toggle"));
    await tick();
    ok("открылся раздел ключей", !$("tokens").hidden);
    ok("ключи запрошены", stub.calls.some((c) => c.key === "GET /api/tokens"));
    ok("видно, для чего ключ", /дайджест/.test($("tokens-list").textContent), $("tokens-list").textContent);
    ok("видно, что ключом ещё не пользовались",
      /не пользовались/.test($("tokens-list").textContent), $("tokens-list").textContent);

    $("token-label").value = "вечный";
    $("token-days").value = "0";
    submit(w, $("token-form"));
    await tick();
    const call = stub.calls.find((c) => c.key === "POST /api/tokens");
    ok("ключ выписан через API", Boolean(call), JSON.stringify(stub.calls.map((c) => c.key)));
    ok("в запросе срок 0 — бессрочный", call && call.init.body.includes('"days":0'), call && call.init.body);
    ok("значение ключа показано один раз",
      $("token-fresh").hidden === false && $("token-value").value === "mdk-secret", $("token-value").value);
    ok("сказано скопировать ключ сейчас",
      /скопируйте/.test($("status").textContent), $("status").textContent);

    const revoke = [...w.document.querySelectorAll("#tokens-list button")][0];
    ok("у ключа есть кнопка отзыва", Boolean(revoke), $("tokens-list").innerHTML);
    click(w, revoke);
    await tick();
    ok("отзыв ключа ушёл в API",
      stub.calls.some((c) => c.key === "DELETE /api/tokens/9"), JSON.stringify(stub.calls.map((c) => c.key)));
  }

  // --- 4. Ошибка сервера показывается текстом --------------------------------
  {
    const api = makeApi({
      "GET /api/config": () => ({ status: 200, body: { registration: "invite" } }),
      "POST /api/auth/register": () => ({ status: 403, body: { error: "приглашение не подошло" } }),
    });
    const { $, w, tick } = await boot({ api });
    // На экран регистрации надо сначала попасть: boot открывает вход.
    click(w, $("register-toggle"));
    await tick();
    ok("кнопка регистрации открыла форму", !$("register").hidden);
    ok("при режиме «по приглашению» сказано открыть ссылку",
      /ссылк/i.test($("register-hint").textContent), $("register-hint").textContent);
    $("register-name").value = "petya";
    $("register-pass").value = "parol1234";
    submit(w, $("register-form"));
    await tick();
    ok("ошибка сервера видна в статусе", /приглашение не подошло/.test($("status").textContent), $("status").textContent);
    ok("экран регистрации не сменился", !$("register").hidden);
  }

  // --- 5. Закрытая регистрация: кнопки регистрации нет -----------------------
  {
    const api = makeApi({ "GET /api/config": () => ({ status: 200, body: { registration: "closed" } }) });
    const { $ } = await boot({ api });
    ok("регистрация закрыта — кнопки нет", $("register-toggle").hidden === true);
    ok("подсказка про закрытую регистрацию", /закрыта/i.test($("register-hint").textContent), $("register-hint").textContent);
  }

  // --- 6. Создать документ: путь собирается и уводит в редактор --------------
  {
    const api = makeApi({ "GET /api/me": () => ({ status: 200, body: { user: { username: "deniz", is_admin: true } } }) });
    const { $, w, tick } = await boot({ api });
    ok("у себя видно форму создания документа", $("new-doc-form").hidden === false);

    const real = w.mdcloudEditorUrl;
    let seen = null;
    w.mdcloudEditorUrl = (base, owner, path) => {
      seen = { base, owner, path };
      return real(base, owner, path);
    };
    $("new-doc-path").value = "  /ДЗ/ИИ/задачи  ";
    submit(w, $("new-doc-form"));
    await tick();
    ok("форма берёт хозяина списка", seen && seen.owner === "deniz", JSON.stringify(seen));
    ok("путь очищен от пробелов и ведущего слэша", seen && seen.path === "ДЗ/ИИ/задачи", JSON.stringify(seen));
    $("new-doc-path").value = "  мои  задачи//две  ";
    submit(w, $("new-doc-form"));
    ok("пробелы стали дефисами, лишние слэши ушли",
      seen && seen.path === "мои-задачи/две", JSON.stringify(seen));
    ok("база — адрес редактора с сервера", seen && seen.base === "https://mathmd.denizsincar.ru", JSON.stringify(seen));
    const url = real("https://mathmd.denizsincar.ru", "deniz", "ДЗ/ИИ/задачи");
    ok("кириллица в пути экранируется посегментно",
      url === "https://mathmd.denizsincar.ru/#cloud=deniz/%D0%94%D0%97/%D0%98%D0%98/%D0%B7%D0%B0%D0%B4%D0%B0%D1%87%D0%B8", url);
  }

  // --- 7. В чужом списке заводить документы нечем ----------------------------
  {
    const api = makeApi({
      "GET /api/me": () => ({ status: 200, body: { user: { username: "deniz", is_admin: true } } }),
      "GET /api/docs/vasilisa": () => ({ status: 200, body: { docs: [] } }),
    });
    const { $ } = await boot({ path: "/vasilisa", api });
    ok("в чужом списке формы создания нет", $("new-doc-form").hidden === true);
  }

  // --- 8. Клик по пункту списка = клик по ссылке -----------------------------
  {
    const api = makeApi({
      "GET /api/me": () => ({ status: 200, body: { user: { username: "deniz", is_admin: true } } }),
      "GET /api/docs/deniz": () => ({
        status: 200,
        body: { docs: [{ path: "lab1", title: "Лаба", visibility: "public" }] },
      }),
    });
    const { $, w } = await boot({ api });
    // Переход на другую страницу в jsdom не работает — вместо него считаем
    // нажатия: у ссылки спрашивают click(), значит, переход состоялся бы.
    const opened = [];
    w.HTMLAnchorElement.prototype.click = function () {
      opened.push(this.getAttribute("href"));
    };
    const li = $("index-list").querySelector("li.folder > ul > li");
    const a = li.querySelector("a");
    ok("в списке есть ссылка", Boolean(a), $("index-list").innerHTML);

    click(w, li);
    ok("клик по пункту списка открывает тот же документ",
      opened.length === 1 && opened[0] === "/deniz/lab1", JSON.stringify(opened));

    click(w, a);
    ok("клик по самой ссылке переход не удваивает", opened.length === 1, JSON.stringify(opened));
  }

  // --- 8б. Список документов — деревом: папка заголовком, файлы под ней ------
  {
    const api = makeApi({
      "GET /api/me": () => ({ status: 200, body: { user: { username: "deniz", is_admin: true } } }),
      "GET /api/docs/deniz": () => ({
        status: 200,
        body: {
          docs: [
            { path: "заметки", title: "заметки", visibility: "public" },
            { path: "сафу/мо/лаб1", title: "лаб1", visibility: "private" },
            { path: "сафу/мо/лаб2", title: "лаб2", visibility: "public" },
            { path: "сафу/ии/конспект", title: "конспект", visibility: "public" },
          ],
        },
      }),
    });
    const { $ } = await boot({ api });
    const list = $("index-list");
    const heads = [...list.querySelectorAll("h2")].map((h) => h.textContent);
    ok("папки стали заголовками, корень — первым", heads.join(", ") === "/, сафу/ии, сафу/мо", heads.join(", "));
    const groups = [...list.querySelectorAll("li.folder")].map((g) => ({
      head: g.querySelector("h2").textContent,
      names: [...g.querySelectorAll("ul > li > a")].map((a) => a.textContent),
    }));
    ok("в папке видны только имена файлов, без пути",
      groups[0].names.join(",") === "заметки" && groups[2].names.join(",") === "лаб1,лаб2",
      JSON.stringify(groups));
    ok("закрытость помечена у нужного файла",
      /закрытый/.test(list.querySelectorAll("li.folder")[2].querySelectorAll("li")[0].textContent),
      list.innerHTML);
    const hrefs = [...list.querySelectorAll("a")].map((a) => decodeURIComponent(a.getAttribute("href")));
    ok("ссылки ведут на полный адрес документа",
      hrefs.join(", ") === "/deniz/заметки, /deniz/сафу/ии/конспект, /deniz/сафу/мо/лаб1, /deniz/сафу/мо/лаб2",
      hrefs.join(", "));
  }

  // --- 8в. Переименование: кнопка, новый путь, PATCH --------------------------
  {
    const doc = {
      owner: "deniz", path: "сафу/мо/лаб1", title: "лаб1",
      content: "текст", visibility: "public", updated_at: "2026-09-15T10:00:00Z", can_edit: true,
    };
    const api = makeApi({
      "GET /api/me": () => ({ status: 200, body: { user: { username: "deniz", is_admin: true } } }),
      "GET /api/docs/deniz/сафу/мо/лаб1": () => ({ status: 200, body: doc }),
      "GET /api/comments/deniz/сафу/мо/лаб1": () => ({
        status: 200,
        body: { comments: [], comments_on: true, can_comment: true, require_auth: false, viewer_authenticated: true },
      }),
      "PATCH /api/docs/deniz/сафу/мо/лаб1": () => ({
        status: 200,
        body: { ...doc, path: "сафу/мо/лаб3", title: "лаб3" },
      }),
      "GET /api/docs/deniz/сафу/мо/лаб3": () => ({
        status: 200, body: { ...doc, path: "сафу/мо/лаб3", title: "лаб3" },
      }),
      "GET /api/comments/deniz/сафу/мо/лаб3": () => ({
        status: 200,
        body: { comments: [], comments_on: true, can_comment: true, require_auth: false, viewer_authenticated: true },
      }),
    });
    const { $, w, tick } = await boot({ path: "/deniz/сафу/мо/лаб1", api });
    ok("кнопка переименования видна хозяину", $("rename-toggle").hidden === false);
    ok("форма переименования спрятана, пока её не позвали", $("rename-form").hidden === true);
    ok("в шапке документа виден адрес", /Адрес: deniz\/сафу\/мо\/лаб1/.test($("doc-meta").textContent), $("doc-meta").textContent);

    click(w, $("rename-toggle"));
    ok("кнопка раскрыла форму", $("rename-form").hidden === false);
    ok("поле заполнено текущим адресом", $("rename-path").value === "сафу/мо/лаб1", $("rename-path").value);

    $("rename-path").value = " сафу/мо/лаб3 ";
    submit(w, $("rename-form"));
    await tick(); // PATCH, затем перерисовка документа по новому адресу
    await tick();
    const call = api.calls.find((c) => c.key.startsWith("PATCH "));
    ok("перенос ушёл в API методом PATCH", Boolean(call), JSON.stringify(api.calls.map((c) => c.key)));
    ok("в теле новый путь, без пробелов", call && call.init.body.includes('"path":"сафу/мо/лаб3"'), call && call.init.body);
    ok("после переноса открылся новый адрес", /лаб3/.test($("doc-title").textContent), $("doc-title").textContent);
    ok("сказано, что документ перенесён", /перенесён/.test($("status").textContent), $("status").textContent);
  }

  // --- 8г. Вошедшему про подпись не напоминают --------------------------------
  {
    const api = makeApi({
      "GET /api/me": () => ({ status: 200, body: { user: { username: "deniz", is_admin: true } } }),
      "GET /api/docs/deniz/lab1": () => ({
        status: 200,
        body: {
          owner: "deniz", path: "lab1", title: "Лаба", content: "текст",
          visibility: "public", updated_at: "2026-09-15T10:00:00Z", can_edit: true,
        },
      }),
      "GET /api/comments/deniz/lab1": () => ({
        status: 200,
        body: { comments: [], comments_on: true, can_comment: true, require_auth: false, viewer_authenticated: true },
      }),
    });
    const { $ } = await boot({ path: "/deniz/lab1", api });
    ok("поля подписи у вошедшего нет", $("comment-name-row").hidden === true);
    ok("и подсказки про подпись тоже нет", $("comment-hint").textContent === "", $("comment-hint").textContent);
  }

  // --- 8д. Блок desmos: контейнер и рамка с графиком -------------------------
  {
    const plot = "y = x^2 - 2\ny = \\sin(x)";
    const blocks = [
      { line: 1, html: "<p>График ниже.</p>" },
      { line: 3, html: '<div class="desmos" data-desmos-body="' + encodeURIComponent(plot) + '"></div>' },
    ];
    const api = makeApi({
      "GET /api/me": () => ({ status: 200, body: { user: { username: "deniz", is_admin: true } } }),
      "GET /api/docs/deniz/график": () => ({
        status: 200,
        body: {
          owner: "deniz", path: "график", title: "График",
          content: "График ниже.\n\n```desmos\n" + plot + "\n```",
          visibility: "public", updated_at: "2026-09-15T10:00:00Z", can_edit: true,
        },
      }),
      "GET /api/comments/deniz/график": () => ({
        status: 200,
        body: { comments: [], comments_on: true, can_comment: true, require_auth: false, viewer_authenticated: true },
      }),
    });
    const { $, w } = await boot({
      path: "/deniz/график",
      api,
      hooks: {
        MathJax: { startup: { promise: Promise.resolve() }, typesetPromise: async () => {} },
        mdcloudRenderers: { showdown: { makeHtml: (md) => "<p>" + md + "</p>" } },
        mdcloudMd: { markdownBlocks: () => blocks },
      },
    });

    const html = w.mdcloudDesmosExtension.filter("```desmos\n" + plot + "\n```");
    ok("блок desmos становится контейнером графика",
      /class="desmos"/.test(html) && html.includes(encodeURIComponent(plot)), html);
    ok("выражения едут в атрибуте, а не разметкой",
      !/<p>|y = x\^2/.test(html), html);

    const frame = $("doc-body").querySelector(".desmos iframe");
    ok("в контейнер встала рамка с графиком", Boolean(frame), $("doc-body").innerHTML);
    ok("рамка ведёт в отдельный документ графика",
      frame && frame.getAttribute("src").startsWith("/embed/desmos#"), frame && frame.getAttribute("src"));
    ok("выражения уехали во фрагменте адреса",
      frame && frame.getAttribute("src") === "/embed/desmos#" + encodeURIComponent(plot),
      frame && frame.getAttribute("src"));
    ok("рамка названа для чтеца экрана",
      frame && frame.title === "График Desmos", frame && frame.title);
    // Никакого SDK на странице документа: он живёт внутри рамки, в своём
    // документе и со своей политикой.
    ok("SDK Desmos на странице документа не грузится",
      !w.document.querySelector('script[src*="desmos"]'), $("doc-body").innerHTML);
  }

  // --- 9. Комментарий ссылается на строку документа --------------------------
  {
    const blocks = [
      { line: 1, html: "<p>Первая строка</p>" },
      { line: 3, html: "<p>Вторая строка</p>" },
      { line: 5, html: "<p>Третья строка</p>" },
    ];
    const api = makeApi({
      "GET /api/me": () => ({ status: 200, body: { user: { username: "deniz", is_admin: true } } }),
      "GET /api/docs/deniz/lab1": () => ({
        status: 200,
        body: {
          owner: "deniz", path: "lab1", title: "Лаба",
          content: "Первая строка\n\nВторая строка\n\nТретья строка",
          visibility: "public", updated_at: "2026-09-15T10:00:00Z", can_edit: true,
        },
      }),
      "GET /api/comments/deniz/lab1": () => ({
        status: 200,
        body: {
          comments: [
            {
              id: 1, body: "Вот здесь {line 3} — важно", author_name: "Дениз",
              anonymous: false, mine: false, created_at: "2026-09-15T11:00:00Z",
            },
            {
              id: 2, body: "А [тут]{line5} другое", author_name: "Дениз",
              anonymous: false, mine: false, created_at: "2026-09-15T11:05:00Z",
            },
          ],
          can_comment: true, comments_on: true, require_auth: false, viewer_authenticated: true,
        },
      }),
    });
    const { $, w } = await boot({
      path: "/deniz/lab1",
      api,
      hooks: {
        MathJax: { startup: { promise: Promise.resolve() }, typesetPromise: async () => {} },
        mdcloudRenderers: { showdown: { makeHtml: (md) => "<p>" + md + "</p>" } },
        mdcloudMd: { markdownBlocks: () => blocks },
      },
    });

    const lines = [...w.document.querySelectorAll("#doc-body .doc-block")].map((b) => b.dataset.line);
    ok("документ разложен по блокам с номерами строк", lines.join(",") === "1,3,5", JSON.stringify(lines));

    // «{line 3}» — ссылка с подписью «строка 3», «[тут]{line 5}» — со своей.
    const links = [...$("comments").querySelectorAll("a.line-ref")];
    ok("ссылка без подписи показана как «строка 3»",
      links[0] && links[0].textContent === "строка 3", $("comments").innerHTML);
    ok("ссылка с подписью показана как «тут»",
      links[1] && links[1].textContent === "тут", $("comments").innerHTML);

    click(w, links[1]);
    ok("переход по ссылке ставит фокус на строку документа",
      w.document.activeElement.id === "line-5", w.document.activeElement.id || w.document.activeElement.tagName);
    // Фокуса чтецам мало: строку называем вслух в живом области статуса.
    ok("строка названа вслух в статусе",
      /Строка 5/.test($("status").textContent), $("status").textContent);
    key(w, "b", { code: "KeyB", altKey: true });
    ok("Alt+B вернул на ту самую ссылку в комментарии",
      w.document.activeElement === links[1], w.document.activeElement.tagName);

    // Пишем комментарий, выделяем слово и указываем строку: слово становится
    // подписью ссылки — «[Вот здесь]{line 3}».
    const field = $("comment-body");
    const submitButton = $("comment-form").querySelector('button[type="submit"]');
    submitButton.dispatchEvent(new w.Event("focusin", { bubbles: true }));

    field.focus();
    field.value = "Вот здесь";
    field.setSelectionRange(0, 9); // выделено всё слово
    field.dispatchEvent(new w.Event("select", { bubbles: true }));

    click(w, $("comment-line"));
    ok("выбор строки начался с первой строки",
      w.document.activeElement.dataset.line === "1", w.document.activeElement.dataset.line || w.document.activeElement.tagName);
    key(w, "ArrowDown");
    ok("стрелка вниз ведёт на следующую строку",
      w.document.activeElement.dataset.line === "3", w.document.activeElement.dataset.line || w.document.activeElement.tagName);
    key(w, "Enter");
    ok("Enter обернул выделенное слово ссылкой",
      field.value === "[Вот здесь]{line 3}", field.value);
    ok("фокус вернулся в комментарий", w.document.activeElement === field, w.document.activeElement.tagName);

    // Без выделения вставляется простая ссылка — и в то место, где курсор.
    field.value = "Смотри ";
    field.setSelectionRange(7, 7);
    field.dispatchEvent(new w.Event("keyup", { bubbles: true }));
    key(w, "Escape"); // снять выбор строки
    click(w, $("comment-line"));
    key(w, "Enter");
    ok("без выделения вставилась простая ссылка на первую строку",
      field.value === "Смотри {line 1}", field.value);

    $("line-5").focus();
    ok("ушли в документ", w.document.activeElement.id === "line-5", w.document.activeElement.id);
    key(w, "b", { code: "KeyB", altKey: true });
    ok("Alt+B вернул к последней нажатой кнопке комментария",
      w.document.activeElement === submitButton, w.document.activeElement.tagName);
  }
}

// Итог печатаем после main: внутри всё асинхронное, и выход по process.exit
// из синхронного хвоста убивал прогон раньше первой проверки.
main().then(() => {
  console.log(failed ? "\nПРОВАЛОВ: " + failed : "\nвсё чисто");
  // Код возврата, а не process.exit: выход обрывает ещё не сброшенный stdout,
  // и при запуске в конвейере вывод терялся целиком.
  process.exitCode = failed ? 1 : 0;
});
