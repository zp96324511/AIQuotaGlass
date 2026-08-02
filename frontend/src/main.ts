import { AppService } from "../bindings/aiquotaglass";
import { Events } from "@wailsio/runtime";

// ---------------------------------------------------------------------------
// Types (mirror of the Go bindings)
// ---------------------------------------------------------------------------
interface WindowStatus {
    key: string;
    label: string;
    percent: number;
    used: number;
    total: number;
    resetInSec: number;
    status: string;
}
interface UsageDetail {
    requests: number;
    cost: number;
    cacheHit: number;
    todayCost?: number;
    periodCost?: number;
    groupName?: string;
    rateMultiplier?: number;
    peakActive?: boolean;
    expiresAt?: string;
    expiresInSec?: number;
}
interface ProviderResult {
    providerId: string;
    providerName: string;
    windows: WindowStatus[];
    detail?: UsageDetail;
    updatedAt: string;
    error?: string;
}
interface ProviderConfig {
    id: string;
    name: string;
    type: string;
    enabled: boolean;
    workspace?: string;
    cookie?: string;
    alertThresholds: Record<string, number>;
    detail?: { showUsageDetail?: boolean; international?: boolean };
    sortOrder?: number;
}
interface AppConfig {
    refreshIntervalSec: number;
    nativeNotify: boolean;
    edgeDock: boolean;
    alwaysOnTop: boolean;
    opacity: number;
    snapProviderID?: string;
    providers: ProviderConfig[];
}
interface ProviderType {
    type: string;
    name: string;
    description: string;
    fields: ProviderField[];
}
interface ProviderField {
    key: string;
    label: string;
    kind: string; // text | password | checkbox
    required?: boolean;
    placeholder?: string;
}

// The same frontend serves two windows: the floating widget (default) and the
// settings popup (loaded with ?settings=1).
const isSettings = new URLSearchParams(location.search).has("settings");

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------
let currentConfig: AppConfig | null = null;
let results: ProviderResult[] = [];
let draft: AppConfig | null = null; // editable copy while the settings popup is open
let providerTypes: ProviderType[] = []; // registered (coded) provider adapters
let snapProvider = ""; // provider shown by the edge-docked bar (from widget:snap)
let toastTimer: ReturnType<typeof setTimeout> | null = null;
const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T;

// ---------------------------------------------------------------------------
// Formatting helpers
// ---------------------------------------------------------------------------
function fmtReset(sec: number): string {
    if (sec <= 0) return "即将重置";
    const d = Math.floor(sec / 86400);
    const h = Math.floor((sec % 86400) / 3600);
    const m = Math.floor((sec % 3600) / 60);
    if (d > 0) return `${d}天${h}时后`;
    if (h > 0) return `${h}时${m}分后`;
    return `${m}分后`;
}

function fmtPercent(p: number): string {
    if (p < 0) return "无限";
    return `${(Math.round(p * 10) / 10).toFixed(1)}%`;
}

function fmtQuota(v: number): string {
    return new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 4 }).format(v);
}

function quotaTitle(w: WindowStatus): string {
    if (w.total <= 0) return "";
    return `${w.label}: 已用 ${fmtQuota(w.used)} / 总量 ${fmtQuota(w.total)}`;
}

function barClass(p: number, threshold: number): string {
    if (threshold > 0 && p >= threshold) return "bar-danger";
    if (p >= 80) return "bar-danger";
    if (p >= 60) return "bar-warn";
    return "bar-ok";
}

function clampInt(v: string, min: number, max: number): number {
    const n = parseInt(v, 10);
    if (isNaN(n)) return min;
    return Math.max(min, Math.min(max, n));
}

function cloneConfig(c: AppConfig): AppConfig {
    return JSON.parse(JSON.stringify(c));
}

function defaultConfig(): AppConfig {
    return {
        refreshIntervalSec: 300, nativeNotify: true, edgeDock: true,
        alwaysOnTop: true, opacity: 1, snapProviderID: "", providers: [],
    } as AppConfig;
}

// Window opacity is applied in the frontend (CSS) because the Wails v3
// transparent window renders via DirectComposition and native layered-window
// alpha would break it. The settings popup always stays opaque.
function applyOpacity() {
    if (!isSettings) {
        document.body.style.opacity = String(currentConfig?.opacity ?? 1);
    }
}

// ---------------------------------------------------------------------------
// Main widget rendering
// ---------------------------------------------------------------------------
function cardHTML(r: ProviderResult): string {
    const err = r.error
        ? `<div class="prov-error">${escapeHtml(r.error)}</div>`
        : "";
    const bars = (r.windows || []).map(w => {
        const th = currentConfig?.providers.find(p => p.id === r.providerId)?.alertThresholds?.[w.key] ?? 80;
        const sub = w.resetInSec >= 0 ? `<div class="row-sub">${fmtReset(w.resetInSec)}</div>` : "";
        const title = quotaTitle(w);
        const titleAttr = title ? ` title="${escapeHtml(title)}"` : "";
        return `
        <div class="row">
            <span class="row-label">${w.label}</span>
            <div class="track"${titleAttr}><div class="fill ${barClass(w.percent, th)}" style="width:${Math.min(100, w.percent)}%"></div></div>
            <span class="row-val">${fmtPercent(w.percent)}</span>
        </div>
        ${sub}`;
    }).join("");

    const d = r.detail;
    const detail = d && (d.requests > 0 || (d.todayCost ?? 0) > 0 || (d.periodCost ?? 0) > 0)
        ? (() => {
            const parts: string[] = [];
            if (d.todayCost) parts.push(`今日 $${d.todayCost.toFixed(1)}`);
            if (d.periodCost) parts.push(`近30天 $${d.periodCost.toFixed(1)}`);
            // Relay panels report spend figures instead of a request count;
            // classic providers keep the generic cost/requests line.
            if (!(d.todayCost || d.periodCost)) {
                if (d.requests) parts.push(`${d.requests} 次请求`);
                if (d.cost) parts.push(`费用 $${d.cost.toFixed(4)}`);
            }
            if (d.cacheHit) parts.push(`缓存命中 ${d.cacheHit.toFixed(1)}%`);
            if ((d.rateMultiplier ?? 0) > 0) {
                parts.push(`倍率 x${parseFloat((d.rateMultiplier!).toFixed(2))}${d.peakActive ? " (峰值)" : ""}`);
            }
            return `<div class="prov-detail">${parts.join(" · ")}</div>`;
        })()
        : "";
    const meta = d && (d.groupName || d.expiresAt)
        ? (() => {
            const m: string[] = [];
            if (d.groupName) m.push(`分组 ${d.groupName}`);
            if (d.expiresAt) {
                const days = d.expiresInSec && d.expiresInSec > 0 ? Math.ceil(d.expiresInSec / 86400) : 0;
                m.push(`有效期至 ${d.expiresAt}${days ? ` (剩${days}天)` : ""}`);
            }
            return `<div class="prov-detail">${m.join(" · ")}</div>`;
        })()
        : "";

    const dot = r.error ? "dot-error" : "dot-ok";
    return `
    <div class="card" data-provider-id="${escapeHtml(r.providerId)}">
        <div class="card-head">
            <span class="dot ${dot}"></span>
            <span class="card-name">${escapeHtml(r.providerName)}</span>
            <span class="card-time">${r.updatedAt}</span>
        </div>
        ${err}
        ${bars}
        ${detail}
        ${meta}
    </div>`;
}

function cssEsc(s: string): string {
    return s.replace(/["\\]/g, "\\$&");
}

// orderOf returns the config position of a provider (SortOrder is applied when
// the config is saved), so panels stay in user-defined order regardless of the
// order in which their queries complete.
function orderOf(id: string): number {
    const idx = currentConfig?.providers.findIndex(p => p.id === id) ?? -1;
    return idx < 0 ? Number.MAX_SAFE_INTEGER : idx;
}

function sortResults(list: ProviderResult[]): ProviderResult[] {
    return [...list].sort((a, b) => orderOf(a.providerId) - orderOf(b.providerId));
}

function render() {
    const list = $<HTMLDivElement>("providerList");
    if (!list) return; // settings popup has no provider list
    const hasAccounts = (currentConfig?.providers ?? []).some(p => p.enabled);
    if (!hasAccounts) {
        list.innerHTML = `<div class="empty-hint">尚未配置厂商, 点击 ⚙ 开始</div>`;
        return;
    }
    list.innerHTML = sortResults(results).map(cardHTML).join("");
}

// insertCardAt places a card at its provider's config position, keeping the
// panel order stable while accounts finish at different times.
function insertCardAt(html: string, id: string) {
    const list = $<HTMLDivElement>("providerList");
    const pos = orderOf(id);
    if (pos < Number.MAX_SAFE_INTEGER) {
        const cards = Array.from(list.querySelectorAll<HTMLElement>(".card[data-provider-id]"));
        for (const c of cards) {
            if (orderOf(c.dataset.providerId ?? "") > pos) {
                c.insertAdjacentHTML("beforebegin", html);
                return;
            }
        }
    }
    list.insertAdjacentHTML("beforeend", html);
}

// loadingCardHTML reserves a panel slot while an account is being queried.
function loadingCardHTML(id: string): string {
    const name = currentConfig?.providers.find(p => p.id === id)?.name ?? id;
    return `
    <div class="card card-loading" data-provider-id="${escapeHtml(id)}">
        <div class="card-head">
            <span class="dot dot-ok"></span>
            <span class="card-name">${escapeHtml(name)}</span>
            <span class="card-time">加载中...</span>
        </div>
    </div>`;
}

// upsertProvider replaces the panel of one provider in place (per-account
// incremental rendering) instead of rebuilding every card.
function upsertProvider(r: ProviderResult) {
    const idx = results.findIndex(x => x.providerId === r.providerId);
    if (idx >= 0) results[idx] = r; else results.push(r);

    const existing = $<HTMLDivElement>("providerList").querySelector(`[data-provider-id="${cssEsc(r.providerId)}"]`);
    if (existing) {
        existing.outerHTML = cardHTML(r);
        return;
    }
    insertCardAt(cardHTML(r), r.providerId);
}

function escapeHtml(s: string): string {
    return s.replace(/[&<>"']/g, c => ({
        "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
    }[c]!));
}

// ---------------------------------------------------------------------------
// Edge-docked bar (slim mode)
// ---------------------------------------------------------------------------
const SNAP_KEYS = ["5h", "weekly", "monthly"] as const;
const SNAP_LABELS: Record<string, string> = { "5h": "5小时", weekly: "本周", monthly: "本月" };

function snappedDir(): string {
    return document.body.dataset.snap ?? "";
}

// updateSnapBar fills the three slim progress bars (5h / weekly / monthly) of
// the snap provider. The active axis depends on the dock direction.
function updateSnapBar() {
    const dir = snappedDir();
    if (!dir) return;
    const res = results.find(r => r.providerId === snapProvider && !r.error)
        ?? results.find(r => !r.error);
    const cfg = currentConfig;
    for (const key of SNAP_KEYS) {
        const fill = $<HTMLDivElement>(`snapFill_${key}`);
        if (!fill) continue;
        const w = res?.windows?.find(x => x.key === key);
        const th = cfg?.providers.find(p => p.id === res?.providerId)?.alertThresholds?.[key] ?? 80;
        const pct = w ? Math.min(100, Math.max(0, w.percent)) : 0;
        if (dir === "left" || dir === "right") {
            fill.style.width = "100%";
            fill.style.height = pct + "%";
        } else {
            fill.style.height = "100%";
            fill.style.width = pct + "%";
        }
        const percent = $<HTMLSpanElement>(`snapPct_${key}`);
        if (percent) percent.textContent = Math.round(pct) + "%";
        fill.classList.toggle("bar-danger", !!w && th > 0 && w.percent >= th);
        const track = fill.parentElement;
        if (track instanceof HTMLElement) {
            track.title = w ? quotaTitle(w) || `${w.label} ${fmtPercent(w.percent)}` : (res?.error || "暂无数据");
        }
    }
}

// applySnapMode switches between the full widget and the docked bar.
function applySnapMode(dir: string, providerID: string) {
    if (providerID) snapProvider = providerID;
    if (dir) {
        document.body.dataset.snap = dir;
        $<HTMLDivElement>("snapBar").classList.remove("hidden");
        updateSnapBar();
    } else {
        delete document.body.dataset.snap;
        $<HTMLDivElement>("snapBar").classList.add("hidden");
    }
}

// ---------------------------------------------------------------------------
// Settings popup (separate window)
// ---------------------------------------------------------------------------
function fieldsFor(type: string): ProviderField[] {
    return providerTypes.find(t => t.type === type)?.fields ?? [];
}

// isValueField reports whether a field kind maps to a real config value read
// back from the DOM (as opposed to UI-only fields such as the help tutorial).
function isValueField(f: ProviderField): boolean {
    return f.kind === "text" || f.kind === "password" || f.kind === "checkbox";
}

function typeName(t: string): string {
    return providerTypes.find(x => x.type === t)?.name ?? t;
}

// Get/set a provider config slot addressed by a ProviderField.Key.
function getFieldValue(p: ProviderConfig, key: string): string {
    switch (key) {
        case "workspace": return p.workspace ?? "";
        case "cookie": return p.cookie ?? "";
        case "detail.showUsageDetail": return p.detail?.showUsageDetail ? "1" : "0";
        case "detail.international": return p.detail?.international ? "1" : "0";
        default: return "";
    }
}
function setFieldValue(p: ProviderConfig, key: string, raw: string) {
    switch (key) {
        case "workspace": p.workspace = raw; break;
        case "cookie": p.cookie = raw; break;
        case "detail.showUsageDetail": p.detail = { ...p.detail, showUsageDetail: raw === "1" }; break;
        case "detail.international": p.detail = { ...p.detail, international: raw === "1" }; break;
    }
}

function renderSettings() {
    const cfg = draft ?? currentConfig ?? defaultConfig();
    const body = $<HTMLDivElement>("settingsBody");

    const providerForms = cfg.providers.map((p, i) => {
        const t = p.type || "opencode-go";
        const typesOpts = (providerTypes.length
            ? providerTypes.map(tt => `<option value="${tt.type}" ${tt.type === t ? "selected" : ""}>${escapeHtml(tt.name)}</option>`)
            : [`<option value="opencode-go">OpenCode Go</option>`]
        ).join("");

        // Parameter form rendered from the type's registered field schema.
        const fieldInputs = fieldsFor(t).map(f => {
            if (f.kind === "help") {
                return `<details class="help-box"><summary>获取配置信息教程</summary><div class="help-body">${escapeHtml(f.label)}</div></details>`;
            }
            if (f.kind === "checkbox") {
                return `<label class="check"><input type="checkbox" id="f_${i}_${f.key}" ${getFieldValue(p, f.key) === "1" ? "checked" : ""}/> ${f.label}</label>`;
            }
            const inputType = f.kind === "password" ? "password" : "text";
            const ph = f.placeholder ? ` placeholder="${escapeHtml(f.placeholder)}"` : "";
            const req = f.required ? " required" : "";
            return `<label>${f.label} <input type="${inputType}" id="f_${i}_${f.key}" value="${escapeHtml(getFieldValue(p, f.key))}"${ph}${req}/></label>`;
        }).join("");

        return `
        <details class="prov-form" open data-i="${i}">
            <summary>
                <label><input type="checkbox" id="en_${i}" ${p.enabled ? "checked" : ""}> <span class="prov-name">${escapeHtml(p.name)}</span><span class="prov-badge">${escapeHtml(typeName(t))}</span></label>
            </summary>
            <div class="fields">
                <input type="hidden" id="id_${i}" value="${escapeHtml(p.id)}"/>
                <label class="type-row">排序号 <input type="number" id="sort_${i}" min="0" value="${p.sortOrder ?? i}"/></label>
                <label class="type-row">厂商类型
                    <select id="type_${i}">${typesOpts}</select>
                </label>
                <label>名称 <input type="text" id="name_${i}" value="${escapeHtml(p.name)}"/></label>
                ${fieldInputs}
                <div class="thresholds">
                    <label>5h阈值 <input type="number" id="th5_${i}" min="0" max="100" value="${p.alertThresholds["5h"] ?? 80}"/></label>
                    <label>周阈值 <input type="number" id="thw_${i}" min="0" max="100" value="${p.alertThresholds["weekly"] ?? 80}"/></label>
                    <label>月阈值 <input type="number" id="thm_${i}" min="0" max="100" value="${p.alertThresholds["monthly"] ?? 80}"/></label>
                    <label>总额度阈值 <input type="number" id="tht_${i}" min="0" max="100" value="${p.alertThresholds["total"] ?? 80}"/></label>
                </div>
                <div class="prov-actions">
                    <button type="button" class="del-btn" data-del="${i}">删除此账号</button>
                </div>
            </div>
        </details>`;
    }).join("");

    const snapOpts = cfg.providers.map(p =>
        `<option value="${escapeHtml(p.id)}" ${p.id === cfg.snapProviderID ? "selected" : ""}>${escapeHtml(p.name)}</option>`
    ).join("");

    body.innerHTML = `
        <div class="global">
            <label>刷新间隔(秒) <input type="number" id="interval" min="30" step="10" value="${cfg.refreshIntervalSec}"/></label>
            <label class="check"><input type="checkbox" id="nativeNotify" ${cfg.nativeNotify ? "checked" : ""}/> 系统通知</label>
            <label class="check"><input type="checkbox" id="edgeDock" ${cfg.edgeDock ? "checked" : ""}/> 贴边吸附</label>
            <label class="check"><input type="checkbox" id="alwaysOnTop" ${cfg.alwaysOnTop ? "checked" : ""}/> 窗口置顶</label>
            <label>贴边展示账号
                <select id="snapProvider">
                    <option value="">自动(第一个账号)</option>
                    ${snapOpts}
                </select>
            </label>
            <label>透明度 <input type="range" id="opacity" min="30" max="100" value="${Math.round((cfg.opacity ?? 1) * 100)}"/> <span id="opacityVal"></span></label>
        </div>
        <div class="providers-sec">${providerForms || '<div class="empty-hint">暂无账号配置, 点击下方 "+ 添加账号"</div>'}</div>`;

    const opacity = $<HTMLInputElement>("opacity");
    const showOpacity = () => { $("opacityVal").textContent = opacity.value + "%"; };
    opacity.addEventListener("input", showOpacity);
    showOpacity();

    body.querySelectorAll("select[id^=type_]").forEach(sel => {
        sel.addEventListener("change", () => {
            const i = parseInt((sel as HTMLElement).id.replace("type_", ""), 10);
            changeProviderType(i, (sel as HTMLSelectElement).value);
        });
    });
    body.querySelectorAll(".del-btn").forEach(btn => {
        btn.addEventListener("click", () => {
            removeProvider(parseInt((btn as HTMLElement).dataset.del!, 10));
        });
    });
}

// Read the current DOM state back into the draft so that in-place edits are
// not lost when the form re-renders (e.g. after add/remove/type change).
function readProviderFromDom(i: number): ProviderConfig {
    const type = $<HTMLSelectElement>(`type_${i}`)?.value || "opencode-go";
        const sortEl = $<HTMLInputElement>(`sort_${i}`);
        const sortRaw = sortEl?.value.trim() ?? "";
        const p: ProviderConfig = {
        id: $<HTMLInputElement>(`id_${i}`).value || `prov_${i}`,
        type,
        name: $<HTMLInputElement>(`name_${i}`).value || "未命名厂商",
        enabled: $<HTMLInputElement>(`en_${i}`).checked,
        alertThresholds: {
            "5h": clampInt($<HTMLInputElement>(`th5_${i}`).value, 0, 100),
            weekly: clampInt($<HTMLInputElement>(`thw_${i}`).value, 0, 100),
            monthly: clampInt($<HTMLInputElement>(`thm_${i}`).value, 0, 100),
            total: clampInt($<HTMLInputElement>(`tht_${i}`)?.value ?? "", 0, 100),
        },
        detail: {},
        sortOrder: sortRaw === "" ? i : clampInt(sortRaw, 0, 9999),
    };
    fieldsFor(type).forEach(f => {
        if (!isValueField(f)) return;
        const el = $(`f_${i}_${f.key}`);
        if (!el) return;
        if (f.kind === "checkbox") {
            setFieldValue(p, f.key, (el as HTMLInputElement).checked ? "1" : "0");
        } else {
            setFieldValue(p, f.key, (el as HTMLInputElement).value.trim());
        }
    });
    return p;
}

function syncDraftFromDom() {
    if (!draft) return;
    const list = document.querySelectorAll(".prov-form");
    list.forEach((_, i) => {
        if (!draft!.providers[i]) return;
        draft!.providers[i] = readProviderFromDom(i);
    });
    const intervalEl = $<HTMLInputElement>("interval");
    if (intervalEl) draft.refreshIntervalSec = parseInt(intervalEl.value, 10) || draft.refreshIntervalSec;
    draft.nativeNotify = $<HTMLInputElement>("nativeNotify").checked;
    draft.edgeDock = $<HTMLInputElement>("edgeDock").checked;
    draft.alwaysOnTop = $<HTMLInputElement>("alwaysOnTop").checked;
    draft.snapProviderID = $<HTMLSelectElement>("snapProvider")?.value ?? "";
    draft.opacity = parseInt($<HTMLInputElement>("opacity").value, 10) / 100;
}

function nextProviderId(baseType: string): string {
    const ids = new Set((draft?.providers ?? []).map(p => p.id));
    let base = baseType;
    let n = 1;
    while (ids.has(base)) {
        n++;
        base = `${baseType}-${n}`;
    }
    return base;
}

function addProvider() {
    syncDraftFromDom();
    if (!draft) return;
    const t = providerTypes[0]?.type ?? "opencode-go";
    draft.providers.push({
        id: nextProviderId(t),
        name: `${typeName(t)} ${draft.providers.length + 1}`,
        type: t,
        enabled: true,
        workspace: "",
        cookie: "",
        alertThresholds: { "5h": 80, weekly: 80, monthly: 80, total: 80 },
        detail: {},
        sortOrder: draft.providers.length,
    });
    renderSettings();
}

function changeProviderType(i: number, newType: string) {
    syncDraftFromDom();
    if (!draft) return;
    const p = draft.providers[i];
    if (!p) return;
    p.type = newType;
    // Type-specific credentials do not carry across adapters; reset them.
    setFieldValue(p, "workspace", "");
    setFieldValue(p, "cookie", "");
    setFieldValue(p, "detail.showUsageDetail", "0");
    renderSettings();
}

function removeProvider(i: number) {
    syncDraftFromDom();
    if (!draft) return;
    draft.providers.splice(i, 1);
    renderSettings();
}

function collectConfig(): AppConfig | null {
    const interval = parseInt($<HTMLInputElement>("interval").value, 10);
    if (!interval || interval < 30) {
        toast("刷新间隔不能小于 30 秒");
        return null;
    }
    const providers: ProviderConfig[] = [];
    const list = document.querySelectorAll(".prov-form");
    list.forEach((_, i) => providers.push(readProviderFromDom(i)));
    return {
        refreshIntervalSec: interval,
        nativeNotify: $<HTMLInputElement>("nativeNotify").checked,
        edgeDock: $<HTMLInputElement>("edgeDock").checked,
        alwaysOnTop: $<HTMLInputElement>("alwaysOnTop").checked,
        snapProviderID: $<HTMLSelectElement>("snapProvider")?.value ?? "",
        opacity: parseInt($<HTMLInputElement>("opacity").value, 10) / 100,
        providers,
    };
}

// ---------------------------------------------------------------------------
// Toast
// ---------------------------------------------------------------------------
function toast(msg: string, ms = 3500) {
    const el = $<HTMLDivElement>("toast");
    el.textContent = msg;
    el.classList.remove("hidden");
    if (toastTimer) clearTimeout(toastTimer);
    toastTimer = setTimeout(() => el.classList.add("hidden"), ms);
}

// ---------------------------------------------------------------------------
// Wiring
// ---------------------------------------------------------------------------
async function initSettings() {
    $<HTMLDivElement>("widget").classList.add("hidden");
    $<HTMLDivElement>("settingsWin").classList.remove("hidden");
    try {
        providerTypes = (await AppService.GetProviderTypes()) ?? [];
    } catch (e) {
        console.error(e);
        providerTypes = [];
    }
    draft = cloneConfig(currentConfig ?? defaultConfig());
    renderSettings();

    $<HTMLButtonElement>("btnSettingsClose").addEventListener("click", () => AppService.CloseSettings());
    $<HTMLButtonElement>("btnCancel").addEventListener("click", () => AppService.CloseSettings());
    $<HTMLButtonElement>("btnAddProvider").addEventListener("click", addProvider);
    $<HTMLButtonElement>("btnTestNotify").addEventListener("click", async () => {
        const btn = $<HTMLButtonElement>("btnTestNotify");
        btn.disabled = true;
        try {
            await AppService.TestNotify();
            toast("测试通知已发送");
        } catch (e) {
            toast("测试通知失败: " + e);
        } finally {
            btn.disabled = false;
        }
    });
    $<HTMLButtonElement>("btnSave").addEventListener("click", async () => {
        const cfg = collectConfig();
        if (!cfg) return;
        try {
            await AppService.SaveConfig(cfg);
            currentConfig = cfg;
            toast("已保存");
            AppService.CloseSettings();
        } catch (e) {
            toast("保存失败: " + e);
        }
    });
}

function initWidget() {
    render();

    $<HTMLButtonElement>("btnRefresh").addEventListener("click", () => {
        AppService.RefreshAll().then(res => { results = sortResults(res); render(); }).catch(e => toast("刷新失败: " + e));
    });
    $<HTMLButtonElement>("btnSettings").addEventListener("click", () => AppService.OpenSettings());
    $<HTMLButtonElement>("btnClose").addEventListener("click", () => AppService.HideToTray());

    // Drag release -> edge snap
    document.addEventListener("mouseup", () => AppService.SnapIfNearEdge());

    // Click on the docked bar -> expand back to the full widget
    $<HTMLDivElement>("snapBar").addEventListener("click", () => AppService.ExpandWidget());

    Events.On("usage:loading", (e: any) => {
        const ids = e.data as string[];
        for (const id of ids) {
            if (results.some(x => x.providerId === id)) continue;
            if ($<HTMLDivElement>("providerList").querySelector(`[data-provider-id="${cssEsc(id)}"]`)) continue;
            insertCardAt(loadingCardHTML(id), id);
        }
    });
    Events.On("usage:update", (e: any) => {
        const list = e.data as ProviderResult[];
        for (const r of list) upsertProvider(r);
        updateSnapBar();
    });
    Events.On("widget:snap", (e: any) => {
        const d = e.data as { dir: string; providerID: string };
        applySnapMode(d.dir, d.providerID ?? "");
    });
    Events.On("usage:alert", (e: any) => {
        const d = e.data as { provider: string; window: string; percent: number; threshold: number };
        toast(`⚠ ${d.provider} ${d.window} 用量 ${fmtPercent(d.percent)} (阈值 ${d.threshold}%)`, 8000);
    });
}

async function init() {
    try {
        currentConfig = await AppService.GetConfig();
    } catch (e) {
        console.error(e);
        currentConfig = null;
    }
    applyOpacity();

    if (isSettings) {
        initSettings();
    } else {
        initWidget();
        const res = await AppService.RefreshAll();
        results = sortResults(res);
        render();
    }

    Events.On("config:saved", (e: any) => {
        currentConfig = e.data as AppConfig;
        applyOpacity();
        // Rebuild panels so SortOrder changes take effect immediately instead
        // of waiting for the next app start.
        render();
        updateSnapBar();
    });
}

init();
