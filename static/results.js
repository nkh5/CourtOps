// A short, non-interactive celebration. No dependencies or tracking.
(function () {
  "use strict";
  var motion = window.matchMedia("(prefers-reduced-motion: reduce)");
  if (motion.matches || !document.querySelector(".podium")) return;
  var layer = document.createElement("div");
  layer.className = "results-confetti";
  layer.setAttribute("aria-hidden", "true");
  var colors = ["#142b4b", "#365f98", "#75a8ed", "#bad2f6", "#ffffff"];
  for (var i = 0; i < 75; i++) {
    var piece = document.createElement("i");
    piece.style.setProperty("--x", Math.random() * 100 + "%");
    piece.style.setProperty("--color", colors[i % colors.length]);
    piece.style.setProperty("--duration", 2.5 + Math.random() * 1.5 + "s");
    piece.style.setProperty("--delay", Math.random() * 0.65 + "s");
    piece.style.setProperty("--drift", Math.random() * 200 - 100 + "px");
    piece.style.setProperty("--spin", Math.random() * 900 - 450 + "deg");
    layer.appendChild(piece);
  }
  document.body.appendChild(layer);
  function cleanup() {
    layer.remove();
    motion.removeEventListener("change", cleanup);
  }
  motion.addEventListener("change", cleanup);
  window.setTimeout(cleanup, 5000);
})();
