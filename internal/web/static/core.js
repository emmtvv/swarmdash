// core.js loads synchronously in <head> (unlike the deferred app.js), so
// the helpers here are already defined when inline page scripts run
// during parsing. Nothing in this file may touch the DOM beyond <head>.

// UI translations for strings built in JS: layout.html embeds the current
// language's catalog (see web.JSMessages) as JSON; t() falls back to the
// English key, and formats %d/%s placeholders from extra arguments.
var t = (function () {
	let catalog = {};
	const el = document.getElementById("sd-i18n");
	if (el) { try { catalog = JSON.parse(el.textContent) || {}; } catch (e) {} }
	return function (key) {
		let msg = catalog[key] || key;
		let i = 1;
		const args = arguments;
		return msg.replace(/%[ds]/g, (m) => (i < args.length ? String(args[i++]) : m));
	};
})();
window.t = t;

// sdBytes formats a byte count the way the server-side bytesHuman does.
function sdBytes(b) {
	const units = "KMGTPE";
	if (b < 1024) return Math.round(b) + " B";
	let div = 1024, exp = 0;
	for (let n = Math.floor(b / 1024); n >= 1024; n = Math.floor(n / 1024)) { div *= 1024; exp++; }
	return (b / div).toFixed(1) + " " + units[exp] + "iB";
}
window.sdBytes = sdBytes;

// sdChart: plain-<canvas> line charts for the dashboard's and a service's
// "Last 24h" history - no charting library, matching the console's
// sparklines.
var sdChart = {
	// draw renders one or more series as filled lines. A series with
	// dash: true is dashed so it stays visible even when its values exactly
	// match another series' (e.g. running == desired) - two flat
	// overlapping solid lines would otherwise read as one. fixedMax pins
	// the y axis (e.g. 100 for percentages); fmt formats the axis label.
	draw(canvasID, series, fixedMax, fmt) {
		const canvas = document.getElementById(canvasID);
		if (!canvas) return;
		const ctx = canvas.getContext("2d");
		// Size the backing bitmap to the canvas's rendered size and scale
		// for devicePixelRatio, so the chart stays crisp on wide cards and
		// HiDPI screens instead of being stretched from a low-res bitmap.
		const dpr = window.devicePixelRatio || 1;
		const w = canvas.clientWidth, h = canvas.clientHeight;
		if (canvas.width !== w * dpr || canvas.height !== h * dpr) {
			canvas.width = w * dpr;
			canvas.height = h * dpr;
		}
		ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
		const pad = 14; // room for the min/max axis labels
		ctx.clearRect(0, 0, w, h);

		let max = fixedMax;
		if (!max) {
			let hi = 0, lo = Infinity;
			for (const s of series) for (const v of s.values) { if (v > hi) hi = v; if (v < lo) lo = v; }
			if (hi <= 0) max = 1;
			// A (nearly) flat series gets twice its value as the axis, so it
			// runs through the middle of the chart instead of hugging the top
			// edge; anything that actually moves keeps 15% headroom.
			else if (hi - lo <= hi * 0.1) max = hi * 2;
			else max = hi * 1.15;
		}

		// Lines, fills and the "0" label all share this baseline, so charts
		// side by side line up regardless of their data.
		const base = h - 2;
		const n = series[0].values.length;
		function point(i, v) {
			const x = n > 1 ? (i / (n - 1)) * w : w / 2;
			const y = base - (Math.max(0, v) / max) * (base - pad);
			return [x, y];
		}

		series.forEach((s) => {
			const pts = s.values.map((v, i) => point(i, v));
			ctx.beginPath();
			pts.forEach(([x, y], i) => (i === 0 ? ctx.moveTo(x, y) : ctx.lineTo(x, y)));
			ctx.lineTo(pts[pts.length - 1][0], base);
			ctx.lineTo(pts[0][0], base);
			ctx.closePath();
			ctx.fillStyle = s.color + "22";
			ctx.fill();

			ctx.beginPath();
			ctx.setLineDash(s.dash ? [5, 4] : []);
			pts.forEach(([x, y], i) => (i === 0 ? ctx.moveTo(x, y) : ctx.lineTo(x, y)));
			ctx.strokeStyle = s.color;
			ctx.lineWidth = 2;
			ctx.stroke();
			ctx.setLineDash([]);
		});

		ctx.fillStyle = "#888";
		ctx.font = "10px ui-monospace, monospace";
		ctx.textBaseline = "top";
		ctx.fillText(fmt ? fmt(max) : String(Math.round(max)), 2, 2);
		ctx.textBaseline = "bottom";
		ctx.fillText("0", 2, base - 3);
	},

	legend(elID, items) {
		const el = document.getElementById(elID);
		if (!el) return;
		el.innerHTML = "";
		items.forEach((it) => {
			const wrap = document.createElement("span");
			wrap.style.cssText = "display:inline-flex;align-items:center;gap:6px;margin-right:16px;";
			const swatch = document.createElement("span");
			swatch.style.cssText = "width:10px;height:2px;border-top:2px " + (it.dash ? "dashed" : "solid") + " " + it.color + ";display:inline-block;";
			const val = document.createElement("span");
			val.className = "mono";
			val.textContent = it.value;
			wrap.append(swatch, document.createTextNode(it.label + " "), val);
			el.appendChild(wrap);
		});
	},
};
window.sdChart = sdChart;
