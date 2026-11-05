(function () {
  var rotator = document.querySelector(".hero-title__rotator");
  if (!rotator) return;

  var words = Array.prototype.slice.call(rotator.querySelectorAll(".hero-title__word"));
  if (words.length < 2) return;

  if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
    words[0].textContent = "you, your agent, or your app";
    words[0].dataset.state = "active";
    words.slice(1).forEach(function (word) {
      word.dataset.state = "leave";
    });
    return;
  }

  var index = 0;

  function cycle() {
    var current = words[index];
    index = (index + 1) % words.length;
    var next = words[index];

    next.dataset.state = "enter";
    // Read a layout value so the entering word starts below the frame before
    // the active transition moves it into place.
    void next.offsetWidth;
    current.dataset.state = "leave";
    next.dataset.state = "active";

    window.setTimeout(cycle, 2400);
  }

  window.setTimeout(cycle, 2200);
})();
