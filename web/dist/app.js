'use strict';

/* lawdesk 프런트엔드.
   빌드 도구 없이 동작하도록 순수 ES2020 으로 작성한다.
   실행파일에 그대로 임베드되므로 외부 CDN 을 참조하지 않는다(오프라인 보장). */

const $ = (id) => document.getElementById(id);

const state = {
  docs: [],          // 전체 문서 메타
  filterDir: null,   // 트리에서 선택한 폴더
  selectedId: null,
  polling: null,
  lastQuery: '',
};

/* ------------------------------------------------------------ 공통 */

async function api(path, opts) {
  const res = await fetch(path, opts);
  const text = await res.text();
  let body = null;
  try { body = text ? JSON.parse(text) : null; } catch { /* HTML 오류 페이지 등 */ }
  if (!res.ok) throw new Error((body && body.error) || `요청 실패 (${res.status})`);
  return body;
}

const postJSON = (path, data) =>
  api(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data || {}),
  });

function esc(s) {
  return String(s == null ? '' : s)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

// 서버 스니펫은 <mark> 만 허용한다. 나머지는 이스케이프해서 삽입한다.
function snippetHTML(s) {
  return esc(s).replace(/&lt;mark&gt;/g, '<mark>').replace(/&lt;\/mark&gt;/g, '</mark>');
}

function fmtBytes(n) {
  if (!n) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB'];
  const i = Math.min(u.length - 1, Math.floor(Math.log(n) / Math.log(1024)));
  return (n / Math.pow(1024, i)).toFixed(i ? 1 : 0) + ' ' + u[i];
}

const KIND_LABEL = { scanned: '스캔', mixed: '일부 스캔', empty: '본문 없음' };
function kindTag(kind) {
  if (!kind || kind === 'native') return '';
  const cls = kind === 'scanned' ? 'scan' : kind === 'mixed' ? 'mixed' : 'empty';
  return ` <span class="tag ${cls}">${KIND_LABEL[kind] || kind}</span>`;
}

function banner(msg, isError) {
  const el = $('banner');
  if (!msg) { el.hidden = true; return; }
  el.textContent = msg;
  el.className = 'banner' + (isError ? ' error' : '');
  el.hidden = false;
}

/* ------------------------------------------------------------ 상태 */

async function refreshStatus() {
  let st;
  try {
    st = await api('/api/status');
  } catch (e) {
    banner('서버에 연결할 수 없습니다: ' + e.message, true);
    return;
  }

  $('version').textContent = st.version || '';
  if (st.root && !$('rootInput').value) $('rootInput').value = st.root;
  $('pickBtn').hidden = !st.picker_native;

  const notes = [];
  if (st.missing_tools && st.missing_tools.length) {
    notes.push('사용할 수 없는 기능: ' + st.missing_tools.join(', '));
  }
  if (!st.ocr_enabled && st.stats && st.stats.scanned > 0) {
    notes.push('스캔 문서가 있지만 OCR이 꺼져 있어 본문 검색이 되지 않습니다.');
  }
  banner(notes.join('  ·  '), false);

  renderStats(st.stats);
  applyProgress(st.progress);
}

function renderStats(s) {
  if (!s) return;
  const rows = [
    ['문서', s.docs.toLocaleString() + '건'],
    ['스캔 문서', s.scanned.toLocaleString() + '건'],
    ['OCR 처리', s.ocr_pages.toLocaleString() + '쪽'],
  ];
  const exts = Object.entries(s.by_ext || {}).sort((a, b) => b[1] - a[1]).slice(0, 5);
  $('statsBox').innerHTML =
    rows.map(([k, v]) => `<div><span>${k}</span><span>${v}</span></div>`).join('') +
    (exts.length
      ? '<div style="margin-top:8px"></div>' +
        exts.map(([k, v]) => `<div><span>${esc(k)}</span><span>${v}</span></div>`).join('')
      : '');
}

/* ------------------------------------------------------------ 색인 */

function applyProgress(p) {
  const box = $('progress');
  if (!p || (!p.running && p.phase !== 'error')) {
    box.hidden = true;
    $('cancelBtn').hidden = true;
    $('indexBtn').disabled = false;
    if (state.polling) { clearInterval(state.polling); state.polling = null; }
    return;
  }

  box.hidden = false;
  $('cancelBtn').hidden = !p.running;
  $('indexBtn').disabled = !!p.running;

  const pct = p.total > 0 ? Math.round((p.done / p.total) * 100) : 0;
  $('barFill').style.width = pct + '%';

  const PHASE = { walk: '파일 탐색 중', extract: '본문 추출 중', optimize: '인덱스 정리 중', done: '완료', error: '오류' };
  const parts = [`${PHASE[p.phase] || p.phase} ${p.done}/${p.total} (${pct}%)`];
  if (p.indexed) parts.push(`새 색인 ${p.indexed}`);
  if (p.skipped) parts.push(`변경없음 ${p.skipped}`);
  if (p.ocr_pages) parts.push(`OCR ${p.ocr_pages}쪽`);
  if (p.removed) parts.push(`삭제 ${p.removed}`);
  if (p.failed) parts.push(`실패 ${p.failed}`);
  $('progressText').textContent = parts.join(' · ');
  $('progressFile').textContent = p.current || '';

  if (p.error) banner(p.error, p.phase === 'error');
}

function startPolling() {
  if (state.polling) return;
  state.polling = setInterval(async () => {
    try {
      const p = await api('/api/index/progress');
      applyProgress(p);
      if (!p.running) {
        clearInterval(state.polling);
        state.polling = null;
        await refreshStatus();
        await loadTree();
        if (state.lastQuery) doSearch(state.lastQuery);
      }
    } catch {
      clearInterval(state.polling);
      state.polling = null;
    }
  }, 400);
}

async function startIndex() {
  const root = $('rootInput').value.trim();
  if (!root) { banner('폴더 경로를 입력하거나 [폴더 선택]을 눌러 주세요.', true); return; }
  try {
    banner('');
    await postJSON('/api/index/start', { root });
    startPolling();
  } catch (e) {
    banner(e.message, true);
  }
}

async function pickFolder() {
  try {
    const r = await postJSON('/api/pick-folder');
    if (r && r.path) { $('rootInput').value = r.path; startIndex(); }
  } catch (e) {
    banner(e.message, true);
  }
}

/* ------------------------------------------------------------ 트리 */

async function loadTree() {
  try {
    const r = await api('/api/tree');
    state.docs = r.docs || [];
  } catch (e) {
    banner('목록을 불러오지 못했습니다: ' + e.message, true);
    return;
  }
  $('docCount').textContent = state.docs.length.toLocaleString() + '건';
  renderTree();
  if (!state.lastQuery) renderResults(browseHits(), null);
}

// rel_path 들에서 폴더 집합을 만든다. 폴더별 문서 수를 함께 센다.
function renderTree() {
  const counts = new Map();
  for (const d of state.docs) {
    const dir = d.dir || '';
    // 상위 폴더에도 누적한다
    const segs = dir ? dir.split('/') : [];
    counts.set('', (counts.get('') || 0) + 1);
    let acc = '';
    for (const s of segs) {
      acc = acc ? acc + '/' + s : s;
      counts.set(acc, (counts.get(acc) || 0) + 1);
    }
  }

  const dirs = [...counts.keys()].filter((d) => d !== '').sort();
  const html = [nodeHTML('', '전체', 0, counts.get('') || 0)];
  for (const d of dirs) {
    const depth = d.split('/').length;
    html.push(nodeHTML(d, d.split('/').pop(), depth, counts.get(d)));
  }
  const tree = $('tree');
  tree.innerHTML = html.join('');
  tree.querySelectorAll('.tnode').forEach((el) => {
    el.onclick = () => {
      state.filterDir = el.dataset.dir || null;
      renderTree();
      if (state.lastQuery) doSearch(state.lastQuery);
      else renderResults(browseHits(), null);
    };
  });
}

function nodeHTML(dir, label, depth, count) {
  const active = (state.filterDir || '') === dir ? ' active' : '';
  const pad = 14 + depth * 12;
  return `<div class="tnode${active}" data-dir="${esc(dir)}" style="padding-left:${pad}px" title="${esc(dir || '전체')}">
    <span class="caret">${depth ? '▸' : '■'}</span><span>${esc(label)}</span><span class="cnt">${count}</span></div>`;
}

// 검색어가 없을 때 보여줄 목록(선택 폴더 기준).
function browseHits() {
  return state.docs.filter(inFilter).slice(0, 300);
}

function inFilter(d) {
  if (!state.filterDir) return true;
  const dir = d.dir || '';
  return dir === state.filterDir || dir.startsWith(state.filterDir + '/');
}

/* ------------------------------------------------------------ 검색 */

let searchTimer = null;
$('q').addEventListener('input', (e) => {
  const v = e.target.value;
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => doSearch(v), 180);
});

async function doSearch(q) {
  state.lastQuery = q;
  if (!q.trim()) {
    $('searchMeta').textContent = '';
    renderResults(browseHits(), null);
    return;
  }
  try {
    const r = await api('/api/search?limit=200&q=' + encodeURIComponent(q));
    const hits = r.hits.filter(inFilter);
    const bits = [`${hits.length}건`];
    if (!r.complete) bits.push('(상위 결과만)');
    if (r.mode === 'like') bits.push('부분일치');
    if (r.note) bits.push(r.note);
    $('searchMeta').textContent = bits.join(' · ');
    renderResults(hits, q);
  } catch (e) {
    $('searchMeta').textContent = '';
    renderResults([], q, e.message);
  }
}

function renderResults(hits, q, error) {
  const box = $('results');
  if (error) { box.innerHTML = `<div class="nores">검색 오류: ${esc(error)}</div>`; return; }
  if (!hits.length) {
    box.innerHTML = `<div class="nores">${
      q ? `“${esc(q)}” 에 대한 결과가 없습니다.`
        : state.docs.length ? '이 폴더에 문서가 없습니다.'
        : '먼저 폴더를 선택해 색인을 시작하세요.'
    }</div>`;
    return;
  }
  box.innerHTML = hits.map((h) => `
    <article class="hit${h.id === state.selectedId ? ' active' : ''}" data-id="${h.id}">
      <h3>${esc(h.title || h.name)}${kindTag(h.kind)}</h3>
      <div class="path">${esc(h.rel_path)}</div>
      <div class="snip">${snippetHTML(h.snippet || h.summary || '')}</div>
    </article>`).join('');

  box.querySelectorAll('.hit').forEach((el) => {
    el.onclick = () => selectDoc(Number(el.dataset.id));
    el.ondblclick = () => openDoc(Number(el.dataset.id), false);
  });
}

/* ------------------------------------------------------------ 상세 */

async function selectDoc(id) {
  state.selectedId = id;
  document.querySelectorAll('.hit').forEach((el) =>
    el.classList.toggle('active', Number(el.dataset.id) === id));

  let d;
  try { d = await api('/api/doc/' + id); }
  catch (e) { $('detail').innerHTML = `<div class="empty">${esc(e.message)}</div>`; return; }

  const outline = (d.outline || []).length
    ? `<section><h4>목차</h4><ul class="outline">${
        d.outline.map((h) => `<li data-lv="${Math.min(3, h.level || 1)}">${esc(h.text)}</li>`).join('')
      }</ul></section>` : '';

  const warns = (d.warnings || []).length
    ? `<section><h4>주의</h4>${d.warnings.map((w) => `<div class="warn">${esc(w)}</div>`).join('')}</section>` : '';

  const kv = [
    ['형식', d.ext],
    ['크기', fmtBytes(d.size)],
    d.pages ? ['쪽수', d.pages + '쪽'] : null,
    d.ocr_pages ? ['OCR 처리', d.ocr_pages + '쪽'] : null,
    ['본문 길이', (d.text_len || 0).toLocaleString() + '자'],
  ].filter(Boolean);

  $('detail').innerHTML = `
    <h2>${esc(d.title || d.name)}${kindTag(d.kind)}</h2>
    <div class="path">${esc(d.path)}</div>
    <div class="actions">
      <button class="btn btn-primary" id="openBtn">파일 열기</button>
      <button class="btn" id="revealBtn">폴더에서 보기</button>
    </div>
    ${outline}
    <section><h4>요약</h4><div class="summary">${esc(d.summary || '(요약 없음)')}</div></section>
    ${warns}
    <section><h4>정보</h4>${
      kv.map(([k, v]) => `<div class="kv"><span>${k}</span><span>${esc(v)}</span></div>`).join('')
    }</section>`;

  $('openBtn').onclick = () => openDoc(id, false);
  $('revealBtn').onclick = () => openDoc(id, true);
}

async function openDoc(id, reveal) {
  try { await postJSON('/api/open', { id, reveal }); }
  catch (e) { banner(e.message, true); }
}

/* ------------------------------------------------------------ 시작 */

$('indexBtn').onclick = startIndex;
$('pickBtn').onclick = pickFolder;
$('cancelBtn').onclick = () => postJSON('/api/index/cancel').catch(() => {});
$('rootInput').addEventListener('keydown', (e) => { if (e.key === 'Enter') startIndex(); });

(async function init() {
  await refreshStatus();
  await loadTree();
  const p = await api('/api/index/progress').catch(() => null);
  if (p && p.running) startPolling();
  $('q').focus();
})();
