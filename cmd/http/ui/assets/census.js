/*
 * FFXIV Census site script. Loaded deferred by the layout; every behaviour
 * here is an enhancement, and each page works without it.
 *
 *   Primary nav: a nav marked data-current-into-view scrolls sideways (on
 *   phones) so its aria-current item is in view. Only the bar scrolls; the
 *   page does not move.
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

    // Entrances run on full page loads only: once the first one has had its
    // time, anything HTMX swaps in later appears without motion.
    window.setTimeout(function () {
        document.documentElement.classList.add("census-settled");
    }, 400);
})();
