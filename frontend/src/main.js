import "./style.css";
import { Clipboard, Events } from "@wailsio/runtime";
import { LogService, PhoneService } from "../bindings/flashtransfer";
import { ConnectStatus, FilesDropped, Log, Progress } from "../bindings/events";

const $ = (id) => document.getElementById(id);

// page errors would otherwise be invisible in a release build; send them to the log
window.addEventListener("error", (e) => LogService.UI(`error: ${e.message} (${e.filename}:${e.lineno})`));
window.addEventListener("unhandledrejection", (e) => LogService.UI(`error: ${e.reason && e.reason.message || e.reason}`));

const state = {
    storageID: 0,
    path: [],          // Entry stack from storage root to current folder
    entries: [],
    selected: new Set(), // indexes into entries
    anchor: -1,        // last clicked row, for shift-click ranges
    busy: false,
};
let connectTimer = 0;
let watchTimer = 0;

// shortcut: typical USB 2 phone read speed, used until a real copy to the Mac is measured
const TYPICAL_SPEED = 25 * 1024 * 1024;
let measuredSpeed = 0; // bytes/s of the last copy to the Mac
let op = { start: 0, sent: 0 }; // current copy, fed by progress events

const current = () => state.path[state.path.length - 1];

// --- connect screen ---

// stickyIssue keeps the reason a requested connect failed on screen; otherwise the next
// check would replace it with "Image Capture is using the phone" once it takes the phone back
let stickyIssue = null;

async function tryConnect(stopImageCapture = false) {
    clearTimeout(connectTimer);
    const r = await PhoneService.Connect(stopImageCapture);
    if (r.Model) {
        stickyIssue = null;
        enterBrowser(r);
        return;
    }
    if (!r.Present) stickyIssue = null;
    if (stopImageCapture && r.Issue) stickyIssue = r.Issue;
    if (stickyIssue && r.Present && r.Issue && r.Issue.Code === "MTP-002") {
        showConnect(stickyIssue, true);
        connectTimer = setTimeout(() => tryConnect(false), 4000);
        return;
    }
    const issue = r.HandingBack ? {
        Info: true, Step: 2, Code: "", Title: "Disconnected",
        Why: "FlashTransfer let go of the phone. You can unplug it now; Photos and Image Capture get it back in a few seconds.",
        Fix: [],
    } : r.Present ? r.Issue : null;
    showConnect(issue, r.Present);
    connectTimer = setTimeout(() => tryConnect(false), r.Issue ? 4000 : 2000);
}

// --- connect progress ---
// Connecting has no known length, so the bar advances per stage, a bit more on every
// retry inside a stage, and eases toward that target so it never looks frozen.

const STAGE_START = { 1: 5, 2: 15, 3: 30, 4: 85 };
const STAGE_CAP = { 1: 12, 2: 28, 3: 80, 4: 97 };
const connectProgress = { timer: 0, start: 0, step: 0, target: 0, value: 0 };

function progressStep(step) {
    const p = connectProgress;
    if (!p.timer) {
        Object.assign(p, { start: performance.now(), step: 0, target: 0, value: 0 });
        $("c-progress").hidden = false;
        p.timer = setInterval(drawProgress, 200);
    }
    // a repeated stage means a retry inside it: nudge forward, never past the stage cap
    p.target = step === p.step ? Math.min(p.target + 12, STAGE_CAP[step]) : Math.max(p.target, STAGE_START[step]);
    p.step = step;
}

function drawProgress() {
    const p = connectProgress;
    p.value += (p.target - p.value) * 0.08;
    $("c-bar").value = p.value;
    const secs = Math.round((performance.now() - p.start) / 1000);
    $("c-pct").textContent = `${Math.floor(p.value)}%  ${secs} s`;
}

function endProgress(done) {
    const p = connectProgress;
    if (!p.timer) return;
    clearInterval(p.timer);
    p.timer = 0;
    if (done) {
        $("c-bar").value = 100;
        $("c-pct").textContent = "100%";
    }
    $("c-progress").hidden = true;
}

// setFlow marks stages before `step` done, `step` itself as `status`, later ones waiting.
function setFlow(step, status, text = "") {
    $("flow").hidden = false;
    document.querySelectorAll("#flow > li").forEach((li) => {
        const n = Number(li.dataset.step);
        li.className = n < step ? "done" : n === step ? status : "todo";
        li.querySelector(".live").textContent = n === step ? text : "";
    });
}

let shownIssue = ""; // code of the problem currently on screen

function showConnect(issue, present = false) {
    // polling repeats the same problem every few seconds; redrawing would close an
    // open Details section and could swallow a click on a button being replaced
    const box = $("c-issue");
    const key = issue ? issue.Code || issue.Title : "";
    if (key && key === shownIssue && !box.hidden && !$("connect").hidden) return;
    shownIssue = key;
    endProgress(false);
    $("browser").hidden = true;
    $("connect").hidden = false;
    box.replaceChildren();
    box.hidden = !issue;
    if (!issue) {
        setFlow(1, "active", present ? "Phone found, checking it..." : "Looking for your phone...");
        return;
    }
    const step = issue.Step || 3;
    setFlow(step, issue.Info ? "active" : "blocked");
    box.classList.toggle("info", !!issue.Info);
    renderIssue(box, issue);
    // line the box up with the stage it blocks
    box.style.marginTop = `${document.querySelector(`#flow > li[data-step="${step}"]`).offsetTop}px`;
    const again = button("Try Again", "", () => connectNow(false));
    if (issue.Info) {
        // asking to connect overrides the pause after Disconnect
        box.append(row(button("Connect Again", "primary", () => connectNow(true))));
    } else if (issue.CanStopImageCapture) {
        box.append(row(button("Stop Image Capture and Connect", "primary", () => connectNow(true)), again));
    } else {
        box.append(row(again));
    }
}

function connectNow(stopImageCapture) {
    LogService.UI(stopImageCapture ? "clicked Stop Image Capture and Connect / Connect Again" : "clicked Try Again");
    $("c-issue").hidden = true;
    setFlow(stopImageCapture ? 2 : 1, "active", stopImageCapture ? "Stopping Image Capture..." : "Checking again...");
    progressStep(stopImageCapture ? 2 : 1);
    tryConnect(stopImageCapture);
}

function backToConnect(issue) {
    clearInterval(watchTimer);
    showConnect(issue);
    connectTimer = setTimeout(() => tryConnect(false), 2000);
}

async function disconnect() {
    if (state.busy) return;
    clearInterval(watchTimer);
    const issue = await PhoneService.Disconnect();
    if (issue) {
        showConnect(issue, true);
        connectTimer = setTimeout(() => tryConnect(false), 4000);
    } else {
        tryConnect(false); // shows "Disconnected" until Image Capture has the phone back
    }
}

// --- browser ---

function enterBrowser(r) {
    endProgress(true);
    $("connect").hidden = true;
    $("browser").hidden = false;
    $("model").textContent = r.Model;
    const sel = $("storage");
    sel.replaceChildren(...r.Storages.map((s) => {
        const o = document.createElement("option");
        o.value = String(s.ID);
        o.textContent = `${s.Name} (${human(s.Free)} free)`;
        return o;
    }));
    sel.hidden = r.Storages.length < 2;
    openStorage(r.Storages[0].ID);

    clearInterval(watchTimer);
    watchTimer = setInterval(async () => {
        if (state.busy) return;
        const w = await PhoneService.StillConnected();
        if (!w.Connected) backToConnect(w.Issue);
    }, 3000);
}

async function openStorage(id) {
    state.storageID = id;
    await openFolder([await PhoneService.Root(id)]);
}

async function openFolder(path) {
    hideBanner();
    state.path = path;
    state.entries = [];
    state.selected.clear();
    renderCrumbs();
    renderList("Loading...");
    const r = await run(() => PhoneService.List(current()));
    if (!r || !ok(r)) return;
    state.entries = r.Entries || [];
    renderList();
}

const refresh = () => openFolder(state.path);

function renderCrumbs() {
    const crumbs = $("crumbs");
    crumbs.replaceChildren();
    state.path.forEach((e, i) => {
        if (i > 0) crumbs.append(Object.assign(document.createElement("span"), { className: "sep", textContent: "/" }));
        const b = button(i === 0 ? "Phone" : e.Name, "crumb", () => openFolder(state.path.slice(0, i + 1)));
        b.disabled = i === state.path.length - 1;
        crumbs.append(b);
    });
    $("btn-up").disabled = state.path.length < 2;
    $("drop-folder").textContent = state.path.length < 2 ? "the phone" : current().Name;
}

function renderList(message) {
    const rows = $("rows");
    rows.replaceChildren();
    const empty = $("empty");
    empty.hidden = !message && state.entries.length > 0;
    empty.textContent = message || "This folder is empty. Drop files here or use Copy to Phone.";
    state.entries.forEach((e, i) => {
        const r = document.createElement("div");
        r.className = "row" + (state.selected.has(i) ? " selected" : "");
        r.setAttribute("role", "row");
        r.append(
            icon(e.IsDir),
            Object.assign(document.createElement("span"), { className: "name", textContent: e.Name, title: e.Name }),
            Object.assign(document.createElement("span"), { className: "dim", textContent: e.IsDir ? "--" : human(e.Size) }),
            Object.assign(document.createElement("span"), { className: "dim", textContent: when(e.ModTime) }),
        );
        r.addEventListener("click", (ev) => select(i, ev));
        r.addEventListener("dblclick", () => e.IsDir && openFolder([...state.path, e]));
        rows.append(r);
    });
    updateSelection();
}

function select(i, ev) {
    if (ev.shiftKey && state.anchor >= 0) {
        const [a, b] = [Math.min(state.anchor, i), Math.max(state.anchor, i)];
        for (let k = a; k <= b; k++) state.selected.add(k);
    } else if (ev.metaKey) {
        state.selected.has(i) ? state.selected.delete(i) : state.selected.add(i);
        state.anchor = i;
    } else {
        state.selected = new Set([i]);
        state.anchor = i;
    }
    [...$("rows").children].forEach((r, k) => r.classList.toggle("selected", state.selected.has(k)));
    updateSelection();
}

function updateSelection() {
    const n = state.selected.size;
    $("btn-download").disabled = state.busy || n === 0;
    $("btn-delete").disabled = state.busy || n === 0;
    $("btn-upload").disabled = state.busy;
    $("btn-disconnect").disabled = state.busy;
    $("sel-info").textContent = n ? `${n} selected` : `${state.entries.length} items`;
    renderDetails();
}

function renderDetails() {
    const box = $("details");
    box.replaceChildren();
    const items = selectedEntries();
    if (!items.length) {
        box.append(Object.assign(document.createElement("p"), { className: "dim", textContent: "Select a file to see its details." }));
        return;
    }
    const files = items.filter((e) => !e.IsDir);
    const folders = items.length - files.length;
    const bytes = files.reduce((n, e) => n + e.Size, 0);
    const facts = [];

    if (items.length === 1) {
        const e = items[0];
        const big = icon(e.IsDir);
        big.classList.add("big");
        box.append(big, Object.assign(document.createElement("h3"), { className: "selectable", textContent: e.Name }));
        facts.push(
            ["Kind", e.IsDir ? "Folder" : kind(e.Name)],
            ["Size", e.IsDir ? "Shown after copying" : `${human(e.Size)} (${e.Size.toLocaleString()} bytes)`],
            ["Modified", when(e.ModTime) || "Unknown"],
            ["Where", state.path.length < 2 ? "Phone" : current().Path],
        );
    } else {
        box.append(Object.assign(document.createElement("h3"), { textContent: `${items.length} items selected` }));
        facts.push(["Size", human(bytes) + (folders ? ` + ${folders} folder${folders === 1 ? "" : "s"}` : "")]);
    }

    if (files.length) {
        facts.push(["Copy to Mac", `About ${duration(bytes / (measuredSpeed || TYPICAL_SPEED))}`]);
    }
    const dl = document.createElement("dl");
    facts.forEach(([k, v]) => dl.append(
        Object.assign(document.createElement("dt"), { textContent: k }),
        Object.assign(document.createElement("dd"), { className: "selectable", textContent: v }),
    ));
    box.append(dl);
    if (files.length) {
        const why = measuredSpeed
            ? `Estimate uses the speed of your last copy (${human(measuredSpeed)}/s).`
            : "Estimate uses a typical USB speed; it gets more accurate after your first copy.";
        box.append(note(folders ? why + " Folders are not included." : why));
    }
}

function kind(name) {
    const ext = name.includes(".") ? name.split(".").pop().toLowerCase() : "";
    const kinds = {
        Image: ["jpg", "jpeg", "png", "heic", "heif", "gif", "webp", "dng", "bmp"],
        Video: ["mp4", "mov", "mkv", "3gp", "avi", "webm"],
        Audio: ["mp3", "m4a", "aac", "wav", "ogg", "opus", "flac", "amr"],
        Document: ["pdf", "doc", "docx", "txt", "xls", "xlsx", "ppt", "pptx", "csv"],
        Archive: ["zip", "rar", "7z", "tar", "gz"],
        "Android app": ["apk"],
    };
    for (const [k, exts] of Object.entries(kinds)) if (exts.includes(ext)) return `${k} (${ext.toUpperCase()})`;
    return ext ? `${ext.toUpperCase()} file` : "File";
}

function duration(secs) {
    if (!isFinite(secs)) return "unknown";
    if (secs < 1) return "less than a second";
    if (secs < 60) return `${Math.ceil(secs)} sec`;
    if (secs < 3600) return `${Math.round(secs / 60)} min`;
    const h = Math.floor(secs / 3600);
    return `${h} hr ${Math.round((secs - h * 3600) / 60)} min`;
}

const selectedEntries = () => [...state.selected].sort((a, b) => a - b).map((i) => state.entries[i]);

// --- actions ---

async function download() {
    const items = selectedEntries();
    const r = await run(() => PhoneService.Download(items, "", false));
    recordSpeed();
    report(r, "to your Mac", async () => {
        const again = await run(() => PhoneService.Download(items, r.Folder, true));
        recordSpeed();
        return again;
    });
}

async function upload(paths = []) {
    const dir = current();
    const r = await run(() => PhoneService.Upload(paths, dir, false));
    const done = await report(r, "to the phone", () => run(() => PhoneService.Upload(r.Paths, dir, true)));
    if (done) refresh();
}

async function remove() {
    const r = await run(() => PhoneService.Delete(selectedEntries()));
    if (!r || r.Canceled || !ok(r)) return;
    toast(`Deleted ${r.Deleted} item${r.Deleted === 1 ? "" : "s"}.`);
    refresh();
}

// report shows the outcome of a copy; returns true if the copy ran.
async function report(r, where, replace) {
    if (!r || r.Canceled) return false;
    if (r.Files) toast(`Copied ${r.Files} file${r.Files === 1 ? "" : "s"} (${human(r.Bytes)}) ${where}.`);
    if (!ok(r)) return true;
    const skipped = r.Skipped || [];
    if (skipped.length) {
        showBanner({
            Code: "MTP-007",
            Title: `${skipped.length} file${skipped.length === 1 ? " was" : "s were"} not copied because ${skipped.length === 1 ? "it already exists" : "they already exist"}`,
            Why: "Existing files are never replaced unless you choose to, so nothing is lost by accident.",
            Fix: [],
            Details: skipped.join("\n"),
        }, [
            button("Replace Them", "primary", async () => {
                hideBanner();
                const again = await replace();
                report(again, where, replace);
                if (where === "to the phone") refresh();
            }),
            button("Keep Existing", "", hideBanner),
        ]);
    } else if (!r.Files) {
        toast("Nothing to copy.");
    }
    return true;
}

// run marks the app busy while a phone call is in flight.
// recordSpeed keeps the last copy-to-Mac speed for estimates; tiny copies are too noisy.
function recordSpeed() {
    const secs = (performance.now() - op.start) / 1000;
    if (op.start && op.sent >= 1024 * 1024 && secs > 0.5) {
        measuredSpeed = op.sent / secs;
        renderDetails();
    }
}

async function run(fn) {
    if (state.busy) return null;
    op = { start: 0, sent: 0 };
    state.busy = true;
    document.body.classList.add("busy");
    updateSelection();
    try {
        return await fn();
    } catch (err) {
        showBanner({ Code: "", Title: "Something went wrong", Why: "The app hit an unexpected error.", Fix: ["Try again."], Details: String(err) });
        return null;
    } finally {
        state.busy = false;
        document.body.classList.remove("busy");
        $("xfer").hidden = true;
        updateSelection();
    }
}

// ok handles Issue/Disconnected on any result; true if there was no problem.
function ok(r) {
    if (r.Disconnected) {
        backToConnect(r.Issue);
        return false;
    }
    if (r.Issue) {
        showBanner(r.Issue);
        return false;
    }
    return true;
}

// --- rendering helpers ---

function renderIssue(box, issue) {
    box.append(Object.assign(document.createElement("h2"), { textContent: issue.Title }));
    if (issue.Why) box.append(Object.assign(document.createElement("p"), { textContent: issue.Why }));
    if (issue.Fix && issue.Fix.length) {
        const ol = document.createElement("ol");
        issue.Fix.forEach((f) => ol.append(Object.assign(document.createElement("li"), { textContent: f })));
        box.append(ol);
    }
    if (issue.Details || issue.Code) {
        const d = document.createElement("details");
        d.append(Object.assign(document.createElement("summary"), { textContent: issue.Code ? `Details (error code ${issue.Code})` : "Details" }));
        d.append(Object.assign(document.createElement("pre"), { textContent: issue.Details || "No further details." }));
        box.append(d);
    }
}

function showBanner(issue, actions = [button("Dismiss", "", hideBanner)]) {
    const b = $("banner");
    b.replaceChildren();
    renderIssue(b, issue);
    b.append(row(...actions));
    b.hidden = false;
}

function hideBanner() {
    $("banner").hidden = true;
}

let toastTimer = 0;
function toast(text) {
    const t = $("toast");
    t.textContent = text;
    t.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => (t.hidden = true), 4000);
}

function button(label, cls, onClick) {
    const b = document.createElement("button");
    b.textContent = label;
    if (cls) b.className = cls;
    b.addEventListener("click", onClick);
    return b;
}

function row(...children) {
    const d = document.createElement("div");
    d.className = "buttons";
    d.append(...children);
    return d;
}

function note(text) {
    return Object.assign(document.createElement("p"), { className: "note", textContent: text });
}

function icon(isDir) {
    const span = document.createElement("span");
    span.className = "kind " + (isDir ? "folder" : "file");
    span.innerHTML = isDir
        ? '<svg viewBox="0 0 24 24"><path d="M3 6.5A1.5 1.5 0 0 1 4.5 5h4l2 2h9A1.5 1.5 0 0 1 21 8.5v9a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 17.5z"/></svg>'
        : '<svg viewBox="0 0 24 24"><path d="M6 2.5h8l5 5v13a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1v-17a1 1 0 0 1 1-1z"/><polyline points="14 2.5 14 7.5 19 7.5"/></svg>';
    return span;
}

function human(n) {
    if (n < 1024) return `${n} B`;
    const units = ["KB", "MB", "GB", "TB"];
    let v = n / 1024, u = 0;
    while (v >= 1024 && u < units.length - 1) { v /= 1024; u++; }
    return `${v.toFixed(v < 10 ? 1 : 0)} ${units[u]}`;
}

function when(t) {
    const d = new Date(t);
    // phones report folders with a zero date
    if (isNaN(d) || d.getFullYear() < 1980) return "";
    return d.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

// --- wiring ---

$("btn-upload").addEventListener("click", () => upload());
$("btn-download").addEventListener("click", download);
$("btn-delete").addEventListener("click", remove);
$("btn-disconnect").addEventListener("click", disconnect);
$("btn-up").addEventListener("click", () => openFolder(state.path.slice(0, -1)));
$("btn-refresh").addEventListener("click", refresh);
$("storage").addEventListener("change", (e) => openStorage(Number(e.target.value)));
$("list").addEventListener("keydown", (e) => {
    if (e.metaKey && e.key === "a") {
        e.preventDefault();
        state.selected = new Set(state.entries.map((_, i) => i));
        renderList();
    }
});

Events.On(ConnectStatus, (ev) => {
    $("c-issue").hidden = true;
    setFlow(ev.data.Step, "active", ev.data.Text);
    progressStep(ev.data.Step);
});

Events.On(FilesDropped, (ev) => {
    if (!$("browser").hidden && ev.data && ev.data.length) upload(ev.data);
});

Events.On(Progress, (ev) => {
    const p = ev.data;
    const now = performance.now();
    if (!op.start) op.start = now;
    op.sent = p.OpSent;
    // whole-copy numbers when the total is known, else the current file
    const [sent, total] = p.OpTotal ? [p.OpSent, p.OpTotal] : [p.Sent, p.Total];
    const secs = (now - op.start) / 1000;
    const speed = secs > 1 ? p.OpSent / secs : 0;
    let left = "estimating time...";
    if (speed > 0) left = `about ${duration((total - sent) / speed)} left${p.OpTotal ? "" : " for this file"}`;

    $("xfer").hidden = false;
    $("xfer-name").textContent = `File ${p.Index}: ${p.Name}`;
    $("xfer-bar").value = total ? sent / total : 1;
    $("xfer-pct").textContent = total ? `${Math.floor((sent * 100) / total)}%, ${left}` : left;
});

// --- log footer ---

const logLines = [];

function addLog(line) {
    logLines.push(line);
    if (logLines.length > 500) logLines.shift();
    $("log-last").textContent = line;
    if (!$("logpanel").hidden) {
        const pre = $("log-text");
        pre.textContent += line + "\n";
        pre.scrollTop = pre.scrollHeight;
    }
}

$("btn-log").addEventListener("click", () => {
    const panel = $("logpanel");
    panel.hidden = !panel.hidden;
    $("btn-log").textContent = panel.hidden ? "Show Log" : "Hide Log";
    $("btn-log").setAttribute("aria-expanded", String(!panel.hidden));
    if (!panel.hidden) {
        const pre = $("log-text");
        pre.textContent = logLines.length ? logLines.join("\n") + "\n" : "Nothing logged yet.";
        pre.scrollTop = pre.scrollHeight;
    }
});

$("btn-copylog").addEventListener("click", async () => {
    await Clipboard.SetText(logLines.join("\n"));
    toast("Log copied. Paste it into a message when asking for help.");
});

(await LogService.Lines()).forEach(addLog);
Events.On(Log, (ev) => addLog(ev.data));

tryConnect(false);
