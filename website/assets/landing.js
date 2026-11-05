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

(function () {
  var install = document.querySelector(".install");
  if (!install) return;

  var code = install.querySelector("code");
  var action = install.querySelector(".install__action");
  var status = document.querySelector("[data-copy-status]");

  function copied() {
    install.dataset.copied = "true";
    if (action) action.textContent = "Copied";
    if (status) status.textContent = "Install command copied.";
    window.setTimeout(function () {
      delete install.dataset.copied;
      if (action) action.textContent = "Copy";
      if (status) status.textContent = "";
    }, 1600);
  }

  install.addEventListener("click", function () {
    var text = code ? code.textContent.trim() : "";
    if (!text) return;
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(copied, function () {
        if (status) status.textContent = "Could not copy the install command.";
      });
      return;
    }
    var area = document.createElement("textarea");
    area.value = text;
    area.setAttribute("readonly", "");
    area.style.position = "fixed";
    area.style.opacity = "0";
    document.body.appendChild(area);
    area.select();
    try {
      document.execCommand("copy");
      copied();
    } catch (error) {
      if (status) status.textContent = "Could not copy the install command.";
    }
    document.body.removeChild(area);
  });
})();
