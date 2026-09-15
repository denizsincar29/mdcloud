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
      const key = method + " " + url;
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
async function boot({ path = "/", hash = "", api } = {}) {
  const dom = new JSDOM(html, {
    url: "https://mdcloud.denizsincar.ru" + path + hash,
    runScripts: "outside-only",
    pretendToBeVisual: true,
    virtualConsole: quietConsole(),
  });
  const w = dom.window;
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

async function main() {
  // --- 1. Свежее облако: сразу форма регистрации ------------------------------
  {
    const { w, $, stub, tick } = await boot();
    ok("на новом облаке открыта регистрация, а не вход", !$("register").hidden && $("login").hidden);
    ok("в подсказке сказано, что это первый аккаунт", /первый/i.test($("register-hint").textContent));

    $("register-name").value = "deniz";
    $("register-pass").value = "parol1234";
    $("register-invite-row").hidden = true; // приглашение не нужно
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
    ok("кнопка приглашений появилась (хозяин)", $("invites-toggle").hidden === false);
  }

  // --- 2. Ссылка-приглашение подставляет код ---------------------------------
  {
    const api = makeApi();
    const { $, w, tick, stub } = await boot({ hash: "#invite=KOD42", api });
    ok("по ссылке открылась регистрация", !$("register").hidden);
    ok("код из ссылки подставлен", $("register-invite").value === "KOD42", $("register-invite").value);
    ok("поле кода видно", $("register-invite-row").hidden === false);
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

    click(w, $("invites-toggle"));
    await tick();
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
    ok("при режиме «по приглашению» поле кода видно", $("register-invite-row").hidden === false);
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
}

// Итог печатаем после main: внутри всё асинхронное, и выход по process.exit
// из синхронного хвоста убивал прогон раньше первой проверки.
main().then(() => {
  console.log(failed ? "\nПРОВАЛОВ: " + failed : "\nвсё чисто");
  // Код возврата, а не process.exit: выход обрывает ещё не сброшенный stdout,
  // и при запуске в конвейере вывод терялся целиком.
  process.exitCode = failed ? 1 : 0;
});
