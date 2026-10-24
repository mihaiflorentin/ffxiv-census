/*
 * FFXIV Census site script. Loaded deferred by the layout; every behaviour
 * here is an enhancement, and each page works without it.
 *
 *   Primary nav: a nav marked data-current-into-view scrolls sideways (on
 *   phones) so its aria-current item is in view. Only the bar scrolls; the
 *   page does not move.
 *
 *   Hover tips: marks carrying data-tip / data-tip-value show their figures
 *   in one shared tooltip on hover, focus and tap.
 */
(function () {
    "use strict";

    function currentIntoView() {
        document.querySelectorAll("[data-current-into-view]").forEach(function (bar) {
            var current = bar.querySelector('[aria-current="page"]');
            if (!current || bar.scrollWidth <= bar.clientWidth) {
                return;
            }
            var barBox = bar.getBoundingClientRect();
            var itemBox = current.getBoundingClientRect();
            if (itemBox.left >= barBox.left && itemBox.right <= barBox.right) {
                return;
            }
            // Centre the current item in the bar.
            bar.scrollLeft += itemBox.left - barBox.left - (barBox.width - itemBox.width) / 2;
        });
    }

    currentIntoView();

    // Switch: after HTMX swaps the region a switch option targets, move focus
    // to the region's data-switch-heading and announce its
    // data-switch-announce through the shell's polite live region.
    function announce(text) {
        var live = document.getElementById("census-announcer");
        if (!live || !text) {
            return;
        }
        // Clear first so repeating the same text is still read out.
        live.textContent = "";
        window.setTimeout(function () {
            live.textContent = text;
        }, 50);
    }

    // The switch link is replaced by the swap, so remember which region it
    // targets while it is still in the page, then act when HTMX reports the
    // new region loaded (htmx:load fires on the swapped-in element).
    var pendingRegion = null;

    document.body.addEventListener("htmx:beforeRequest", function (evt) {
        var trigger = evt.target;
        if (trigger && trigger.classList && trigger.classList.contains("switch-option")) {
            pendingRegion = trigger.getAttribute("hx-target");
        }
    });

    document.body.addEventListener("htmx:load", function () {
        if (!pendingRegion) {
            return;
        }
        var region = document.querySelector(pendingRegion);
        if (!region) {
            return;
        }
        pendingRegion = null;
        var heading = region.querySelector("[data-switch-heading]");
        if (heading) {
            heading.focus();
        }
        announce(region.getAttribute("data-switch-announce"));
    });

    // Hover tips: any element with data-tip (a title) and data-tip-value
    // (its figures, formatted by the server) shows them in one shared
    // tooltip, styled like the chart tooltips, on hover, keyboard focus and
    // tap. data-tip-swatch adds a colour key (swatch classes). The tooltip is
    // aria-hidden: every figure is also in the page's text or tables, and
    // focusable marks carry their own labels.
    var tip = null;
    var tipHost = null;

    function tipElement() {
        if (tip) {
            return tip;
        }
        tip = document.createElement("div");
        tip.className = "census-tip";
        tip.setAttribute("aria-hidden", "true");
        tip.hidden = true;
        document.body.appendChild(tip);
        return tip;
    }

    function hostOf(node) {
        return node && node.closest ? node.closest("[data-tip]") : null;
    }

    function fillTip(host) {
        var el = tipElement();
        el.textContent = "";
        var title = document.createElement("span");
        title.className = "census-tip-title";
        var swatch = host.getAttribute("data-tip-swatch");
        if (swatch) {
            var mark = document.createElement("span");
            mark.className = "census-tip-swatch " + swatch;
            title.appendChild(mark);
        }
        title.appendChild(document.createTextNode(host.getAttribute("data-tip")));
        var value = document.createElement("span");
        value.className = "census-tip-value";
        value.textContent = host.getAttribute("data-tip-value") || "";
        el.appendChild(title);
        el.appendChild(value);
        el.hidden = false;
        return el;
    }

    // Place the tooltip centred on x, just above y (or below bottom when
    // there is no room above), kept inside the viewport.
    function placeTip(el, x, y, bottom) {
        var gap = 10;
        var edge = 8;
        var width = el.offsetWidth;
        var height = el.offsetHeight;
        var left = Math.max(edge, Math.min(window.innerWidth - width - edge, x - width / 2));
        var top = y - height - gap;
        if (top < edge) {
            top = bottom + gap;
        }
        el.style.left = Math.round(left) + "px";
        el.style.top = Math.round(top) + "px";
    }

    function showTip(host, x, y) {
        var el = fillTip(host);
        tipHost = host;
        var box = host.getBoundingClientRect();
        if (x === undefined) {
            // Keyboard focus and taps: anchor to the mark itself.
            placeTip(el, box.left + box.width / 2, box.top, box.bottom);
        } else {
            placeTip(el, x, y, y + 24);
        }
        el.classList.add("census-tip-shown");
    }

    function hideTip() {
        tipHost = null;
        if (tip) {
            tip.classList.remove("census-tip-shown");
            tip.hidden = true;
        }
    }

    document.addEventListener("pointermove", function (evt) {
        var host = hostOf(evt.target);
        if (!host) {
            if (tipHost && evt.pointerType !== "touch") {
                hideTip();
            }
            return;
        }
        if (evt.pointerType === "touch") {
            return;
        }
        if (host !== tipHost) {
            showTip(host, evt.clientX, evt.clientY);
        } else {
            placeTip(tip, evt.clientX, evt.clientY, evt.clientY + 24);
        }
    });

    document.addEventListener("pointerdown", function (evt) {
        if (evt.pointerType !== "touch") {
            return;
        }
        var host = hostOf(evt.target);
        if (host) {
            showTip(host);
        } else {
            hideTip();
        }
    });

    document.documentElement.addEventListener("mouseleave", hideTip);

    document.addEventListener("focusin", function (evt) {
        var host = hostOf(evt.target);
        if (host) {
            showTip(host);
        } else if (tipHost) {
            hideTip();
        }
    });

    document.addEventListener("focusout", function (evt) {
        if (tipHost && !tipHost.contains(evt.relatedTarget)) {
            hideTip();
        }
    });

    document.addEventListener("keydown", function (evt) {
        if (evt.key === "Escape") {
            hideTip();
        }
    });

    window.addEventListener("scroll", function () {
        if (tipHost && tipHost.contains(document.activeElement)) {
            showTip(tipHost);
            return;
        }
        hideTip();
    }, { passive: true });

    // Entrances run on full page loads only: once the first one has had its
    // time, anything HTMX swaps in later appears without motion.
    window.setTimeout(function () {
        document.documentElement.classList.add("census-settled");
    }, 400);
})();
