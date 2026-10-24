/*
 * FFXIV Census shared chart theme.
 *
 * Loaded by the layout right after Chart.js, before any page script, so
 * every chart on every page starts from the same defaults: fonts, text and
 * grid colours, tooltip style, thin marks, responsive sizing and
 * reduced-motion handling. Colours are read from the CSS tokens in
 * styles.css (the single source); nothing here hard-codes a hex value.
 *
 * Page scripts use window.CensusCharts for colours and formatting:
 *   CensusCharts.slot(n)          categorical slot 1-8 (0 = neutral "other")
 *   CensusCharts.raceColor(name)  fixed colour for a race, on every page
 *   CensusCharts.raceColors(list) the same for a list of labels
 *   CensusCharts.regionColor(code) fixed colour for a region code (NA ...)
 *   CensusCharts.sortRaces(labels, values) labels/values in fixed race order
 *                                 (use for doughnuts so neighbours are the
 *                                 validated adjacent pairs)
 *   CensusCharts.role(key)        role mark colour: "tank", "healer", "dps"
 *   CensusCharts.grandCompany(key) Grand Company mark colour: "maelstrom",
 *                                 "twin-adder", "immortal-flames"
 *   CensusCharts.primary()        Aether: a single or primary series
 *   CensusCharts.comparison()     Crystal: a comparison series
 *   CensusCharts.wash(colour)     the ~10% area fill for a line's colour
 *   CensusCharts.surface()        panel surface (rings and gaps between marks)
 *   CensusCharts.format(n)        1,234,567
 *   CensusCharts.compact(n)       1.2K / 3.4M for axis ticks
 *   CensusCharts.formatDate(iso)  "Sat, 24 Oct 2026" for tooltip titles
 *
 * Opt-in plugins (set under options.plugins):
 *   censusCrosshair: { enabled: true }  vertical hairline at the hovered X
 *   censusBarValues: { enabled: true }  value at the tip of each bar
 */
(function () {
    "use strict";

    var root = document.documentElement;
    var cache = {};

    function token(name) {
        if (!(name in cache)) {
            cache[name] = getComputedStyle(root).getPropertyValue(name).trim();
        }
        return cache[name];
    }

    // Categorical palette: eight CVD-checked slots, assigned in fixed order.
    var SLOTS = [
        "--color-chart-1", // Aether hue
        "--color-chart-2", // Crystal hue
        "--color-chart-3",
        "--color-chart-4",
        "--color-chart-5",
        "--color-chart-6",
        "--color-chart-7",
        "--color-chart-8"
    ];

    // Fixed race and region -> slot mapping: a race's slot is its 1-based
    // position in the census race order, which the layout renders from the
    // domain taxonomy into #census-taxonomy (the same order the server uses
    // for swatches). A race keeps its colour on every chart and page,
    // whatever its rank or which races a filter leaves visible.
    var taxonomy = (function () {
        var node = document.getElementById("census-taxonomy");
        try {
            return JSON.parse(node ? node.textContent : "{}") || {};
        } catch (e) {
            return {};
        }
    })();
    var RACE_ORDER = taxonomy.races || [];
    var REGION_ORDER = taxonomy.regions || [];

    // Semantic mark palettes: a role or Grand Company keeps its game colour on
    // every chart. Unknown keys fall back to the neutral "other" colour.
    var ROLES = {
        tank: "--color-mark-tank",
        healer: "--color-mark-healer",
        dps: "--color-mark-dps"
    };
    var GRAND_COMPANIES = {
        "maelstrom": "--color-gc-maelstrom",
        "twin-adder": "--color-gc-twin-adder",
        "immortal-flames": "--color-gc-immortal-flames"
    };

    function semantic(map, key) {
        return token(map[key] || "--color-chart-other");
    }

    function slot(n) {
        if (n >= 1 && n <= SLOTS.length) {
            return token(SLOTS[n - 1]);
        }
        return token("--color-chart-other");
    }

    function raceColor(name) {
        return slot(RACE_ORDER.indexOf(name) + 1);
    }

    function regionColor(code) {
        return slot(REGION_ORDER.indexOf(code) + 1);
    }

    function raceColors(names) {
        return (names || []).map(raceColor);
    }

    function sortRaces(labels, values) {
        var rank = function (name) {
            var i = RACE_ORDER.indexOf(name);
            return i < 0 ? RACE_ORDER.length : i;
        };
        var rows = (labels || []).map(function (label, i) {
            return { label: label, value: values ? values[i] : undefined };
        });
        rows.sort(function (a, b) { return rank(a.label) - rank(b.label); });
        return {
            labels: rows.map(function (r) { return r.label; }),
            values: rows.map(function (r) { return r.value; })
        };
    }

    function wash(colour) {
        var hex = String(colour).replace("#", "");
        if (hex.length !== 6) {
            return colour;
        }
        var n = parseInt(hex, 16);
        return "rgba(" + ((n >> 16) & 255) + ", " + ((n >> 8) & 255) + ", " + (n & 255) + ", 0.1)";
    }

    var numberFormat = new Intl.NumberFormat("en-US");
    var compactFormat = new Intl.NumberFormat("en-US", { notation: "compact", maximumFractionDigits: 1 });

    function format(n) {
        return numberFormat.format(Number(n) || 0);
    }

    function compact(n) {
        return Math.abs(Number(n)) >= 1000 ? compactFormat.format(Number(n)) : format(n);
    }

    function formatDate(iso) {
        var d = new Date(iso + "T00:00:00Z");
        if (isNaN(d.getTime())) {
            return iso;
        }
        return d.toLocaleDateString("en-GB", { weekday: "short", day: "numeric", month: "short", year: "numeric", timeZone: "UTC" });
    }

    var reducedMotion = window.matchMedia ? window.matchMedia("(prefers-reduced-motion: reduce)") : { matches: false };

    var crosshair = {
        id: "censusCrosshair",
        afterDatasetsDraw: function (chart, args, opts) {
            if (!opts || !opts.enabled) {
                return;
            }
            var active = chart.tooltip && chart.tooltip.getActiveElements();
            if (!active || !active.length) {
                return;
            }
            var x = active[0].element.x;
            var area = chart.chartArea;
            var ctx = chart.ctx;
            ctx.save();
            ctx.beginPath();
            ctx.moveTo(x, area.top);
            ctx.lineTo(x, area.bottom);
            ctx.lineWidth = 1;
            ctx.strokeStyle = token("--color-ink-muted");
            ctx.stroke();
            ctx.restore();
        }
    };

    var barValues = {
        id: "censusBarValues",
        afterDatasetsDraw: function (chart, args, opts) {
            if (!opts || !opts.enabled) {
                return;
            }
            var horizontal = chart.options.indexAxis === "y";
            var ctx = chart.ctx;
            ctx.save();
            ctx.font = "600 12px " + token("--font-sans");
            ctx.fillStyle = token("--color-ink");
            ctx.textBaseline = horizontal ? "middle" : "bottom";
            ctx.textAlign = horizontal ? "left" : "center";
            chart.data.datasets.forEach(function (dataset, di) {
                var meta = chart.getDatasetMeta(di);
                if (meta.hidden) {
                    return;
                }
                meta.data.forEach(function (bar, i) {
                    var text = compact(dataset.data[i]);
                    if (horizontal) {
                        ctx.fillText(text, bar.x + 6, bar.y);
                    } else {
                        ctx.fillText(text, bar.x, bar.y - 4);
                    }
                });
            });
            ctx.restore();
        }
    };

    function applyMotion() {
        if (!window.Chart) {
            return;
        }
        // Mutate the default animation instead of replacing it: a fresh object
        // drops Chart.js's resolver wiring, and the tooltip's own animations
        // then fail ("this._fn is not a function"), so no tooltip ever draws.
        var animation = window.Chart.defaults.animation;
        animation.duration = reducedMotion.matches ? 0 : 400;
        animation.easing = "easeOutQuart";
        window.Chart.defaults.transitions.active.animation.duration = reducedMotion.matches ? 0 : 150;
    }

    function apply() {
        var Chart = window.Chart;
        if (!Chart) {
            return;
        }
        var d = Chart.defaults;
        var ink = token("--color-ink");
        var muted = token("--color-ink-muted");
        var line = token("--color-line");

        d.font.family = token("--font-sans");
        d.font.size = 12;
        d.color = muted;
        d.borderColor = line;
        d.responsive = true;
        d.maintainAspectRatio = false;
        applyMotion();

        d.scale.grid.color = line;
        d.scale.grid.tickColor = line;
        d.scale.border.color = line;
        d.scale.ticks.color = muted;
        d.scale.ticks.padding = 8;

        d.elements.line.borderWidth = 2;
        // Monotone cubic curves never overshoot the data, so a line over sparse
        // daily counts cannot dip below zero between points.
        d.elements.line.cubicInterpolationMode = "monotone";
        d.elements.line.borderCapStyle = "round";
        d.elements.line.borderJoinStyle = "round";
        d.elements.point.radius = 0;
        d.elements.point.hoverRadius = 5;
        d.elements.point.hitRadius = 12;
        d.elements.point.borderWidth = 2;
        d.elements.bar.borderRadius = 4;
        d.elements.bar.borderSkipped = "start";
        d.datasets.bar.maxBarThickness = 24;
        d.elements.arc.borderColor = token("--color-surface");
        d.elements.arc.borderWidth = 2;

        var legend = d.plugins.legend;
        legend.position = "bottom";
        legend.align = "center";
        legend.labels.color = muted;
        legend.labels.usePointStyle = true;
        legend.labels.pointStyle = "circle";
        legend.labels.boxWidth = 8;
        legend.labels.boxHeight = 8;
        legend.labels.padding = 12;

        var tip = d.plugins.tooltip;
        tip.backgroundColor = token("--color-raised");
        tip.borderColor = line;
        tip.borderWidth = 1;
        tip.titleColor = muted;
        tip.bodyColor = ink;
        tip.titleFont = { weight: "500" };
        tip.bodyFont = { weight: "600" };
        tip.padding = 10;
        tip.cornerRadius = 8;
        tip.boxPadding = 6;
        tip.usePointStyle = true;
        tip.caretSize = 0;

        Chart.register(crosshair, barValues);
    }

    apply();

    if (reducedMotion.addEventListener) {
        reducedMotion.addEventListener("change", applyMotion);
    }

    // Canvas text does not re-render when the web font arrives; redraw once
    // Manrope is ready so axis labels match the page.
    if (document.fonts && document.fonts.ready && window.Chart) {
        document.fonts.ready.then(function () {
            Object.values(window.Chart.instances).forEach(function (chart) {
                chart.update("none");
            });
        });
    }

    window.CensusCharts = {
        token: token,
        slot: slot,
        raceColor: raceColor,
        raceColors: raceColors,
        regionColor: regionColor,
        sortRaces: sortRaces,
        role: function (key) { return semantic(ROLES, key); },
        grandCompany: function (key) { return semantic(GRAND_COMPANIES, key); },
        primary: function () { return token("--color-aether"); },
        comparison: function () { return token("--color-crystal"); },
        surface: function () { return token("--color-surface"); },
        wash: wash,
        format: format,
        compact: compact,
        formatDate: formatDate
    };
})();
