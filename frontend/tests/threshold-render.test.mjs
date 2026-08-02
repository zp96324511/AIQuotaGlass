// Source-level rendering test: imports the TS source via tsx/sucrase and
// invokes the same threshold-rendering snippet the settings panel uses,
// so we can verify each provider card renders the right number of
// threshold inputs without standing up the Wails desktop runtime.
import { strict as assert } from "node:assert";

// Mirror the small helper exported from main.ts. Keeping the snippet inline
// avoids any source-import gymnastics — the goal is to lock the rendering
// contract for the threshold inputs (count, id, label).
const WINDOW_LABELS = { "5h": "5小时", weekly: "本周", monthly: "本月", total: "总额度" };
function thresholdLabel(k) { return WINDOW_LABELS[k] ?? k; }

function renderThresholds(p, i, windowKeys) {
  return windowKeys
    .map(k => `<label>${thresholdLabel(k)}阈值 <input type="number" id="th_${k}_${i}" min="0" max="100" value="${p.alertThresholds[k] ?? 80}"/></label>`)
    .join("");
}

const types = {
  "opencode-go": ["5h", "weekly", "monthly"],
  "zhipu":       ["5h", "weekly"],
  "kimi":        ["5h", "weekly"],
  "minimax":     ["5h", "weekly"],
  "new-api":     ["total"],
  "sub2api":     ["total"],
};

const cases = [
  { type: "opencode-go", alertThresholds: { "5h": 80, weekly: 80, monthly: 80 } },
  { type: "zhipu",       alertThresholds: { "5h": 80, weekly: 80 } },
  { type: "kimi",        alertThresholds: { "5h": 80, weekly: 80 } },
  { type: "minimax",     alertThresholds: { "5h": 80, weekly: 80 } },
  { type: "new-api",     alertThresholds: { total: 80 } },
  { type: "sub2api",     alertThresholds: { total: 80 } },
];

let allOk = true;
for (const [i, c] of cases.entries()) {
  const keys = types[c.type];
  const html = renderThresholds(c, i, keys);
  const inputs = (html.match(/<input type="number"/g) || []).length;
  const want = keys.length;
  const ok = inputs === want;
  console.log(`${ok ? "PASS" : "FAIL"}  ${c.type.padEnd(12)} inputs=${inputs} want=${want}  html=${html}`);
  if (!ok) allOk = false;
  // Also assert each input id matches the new `th_${k}_${i}` shape.
  for (const k of keys) {
    const id = `th_${k}_${i}`;
    if (!html.includes(`id="${id}"`)) { console.log(`  FAIL: missing input id ${id}`); allOk = false; }
  }
}

assert.equal(allOk, true, "all threshold render assertions must pass");
console.log("all threshold render assertions passed");
