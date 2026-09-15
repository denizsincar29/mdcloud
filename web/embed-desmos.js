// График Desmos по выражениям из фрагмента адреса.
//
// Строку с выражениями кладёт в адрес страница облака — по строке на
// выражение, всё закодировано целиком. Здесь она разбирается и уходит в
// калькулятор строками LaTeX: разметку мы не собираем и в документ ничего не
// вставляем, поэтому чужой текст из фрагмента остаётся текстом.

// expressions разбирает фрагмент адреса: одна строка — одно выражение.
function expressions() {
  const raw = location.hash.replace(/^#/, "");
  if (!raw) return [];
  let text = raw;
  try {
    text = decodeURIComponent(raw);
  } catch (err) {
    console.warn("[desmos] фрагмент адреса не разобрался:", err);
  }
  return text.split("\n").map((s) => s.trim()).filter(Boolean);
}

// Вход в график: штатный метод Desmos ставит фокус в список выражений; если
// версия API его не знает, фокусируем сам iframe — тогда до списка дойдёт Tab.
function enterGraph(calc, graph) {
  if (calc && typeof calc.focusFirstExpression === "function") {
    try {
      calc.focusFirstExpression();
      return;
    } catch (err) {
      console.warn("[desmos] вход в график:", err);
    }
  }
  const frame = graph.querySelector("iframe");
  if (frame) frame.focus();
}

function start() {
  const graph = document.getElementById("graph");
  const enter = document.getElementById("enter");
  const lines = expressions();

  if (typeof Desmos === "undefined" || typeof Desmos.Calculator !== "function") {
    graph.textContent = "График Desmos не загрузился.";
    return;
  }

  let calc;
  try {
    calc = Desmos.Calculator(graph, {
      expressions: true,
      settingsMenu: false,
      border: false,
      projectorMode: true,
    });
  } catch (err) {
    graph.textContent = "График Desmos не построился.";
    console.error("[desmos] калькулятор не создался:", err);
    return;
  }

  lines.forEach((expr, i) => {
    try {
      calc.setExpression({ id: "e" + i, latex: expr });
    } catch (err) {
      console.warn("[desmos] выражение не распознано:", expr, err);
    }
  });

  enter.hidden = false;
  enter.addEventListener("click", () => enterGraph(calc, graph));
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", start);
} else {
  start();
}
