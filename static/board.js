// The court board refreshes itself every five seconds.
//
// It asks the server for the same HTML the page was rendered with and swaps it
// in, so there is one rendering path (Go templates) rather than a second one
// written in JavaScript. If a refresh fails, the old content stays on screen
// and a banner says so: a board that quietly shows stale data as if it were
// current is worse than one that admits it is offline.
(function () {
  "use strict";

  var board = document.getElementById("board");
  var stale = document.getElementById("stale");
  if (!board) return;

  var eventID = board.getAttribute("data-event");
  var url = "/board/" + encodeURIComponent(eventID) + "?fragment=1";
  var intervalMs = 5000;
  var failures = 0;

  function markStale(isStale) {
    if (!stale) return;
    stale.hidden = !isStale;
    board.classList.toggle("is-stale", isStale);
  }

  function refresh() {
    // Give up on a single attempt well before the next one is due, so slow
    // requests cannot pile up on a weak venue connection.
    var controller = new AbortController();
    var timer = setTimeout(function () { controller.abort(); }, intervalMs - 1000);

    fetch(url, {
      headers: { "Accept": "text/html" },
      cache: "no-store",
      signal: controller.signal
    })
      .then(function (response) {
        if (!response.ok) throw new Error("status " + response.status);
        return response.text();
      })
      .then(function (html) {
        board.innerHTML = html;
        failures = 0;
        markStale(false);
      })
      .catch(function () {
        failures++;
        // One missed poll is normal on a phone; three in a row is a problem
        // worth telling the room about.
        if (failures >= 3) markStale(true);
      })
      .finally(function () {
        clearTimeout(timer);
      });
  }

  setInterval(refresh, intervalMs);

  // Refresh immediately when the screen comes back, instead of showing whatever
  // was on it when the laptop was closed.
  document.addEventListener("visibilitychange", function () {
    if (!document.hidden) refresh();
  });
})();
