let stations = [];
let history = [];
let config = {};

// --- API ---
async function api(url, opts = {}) {
  const res = await fetch(url, { headers: { 'Content-Type': 'application/json' }, ...opts });
  return res.json();
}

// --- Init ---
async function init() {
  await Promise.all([loadStations(), loadHistory(), loadConfig(), loadStatus()]);
  renderStations();
  connectWS();
}

async function loadStations() {
  const r = await api('/api/stations?all=1');
  if (r.ok) stations = r.data;
  updateStats();
}

async function loadHistory() {
  const r = await api('/api/history');
  if (r.ok) history = r.data;
  renderHistory();
}

async function loadConfig() {
  const r = await api('/api/config');
  if (r.ok) {
    config = r.data;
    document.getElementById('cfg-token').value = config.bot_token || '';
    document.getElementById('cfg-channel').value = config.channel_id || '';
    document.getElementById('cfg-cron').value = config.scrape_cron || '0 */6 * * *';
    document.getElementById('cfg-format').value = config.post_format || '';
  }
}

async function loadStatus() {
  const r = await api('/api/status');
  if (r.ok) {
    const d = r.data;
    document.getElementById('stat-sched').textContent = d.running ? '运行中' : '空闲';
    document.getElementById('stat-sched').className = 'num ' + (d.running ? 'green' : 'gray');
  }
  const rs = await api('/api/stations/stats');
  if (rs.ok) {
    document.getElementById('stat-total').textContent = rs.data.total;
    document.getElementById('stat-blocked').textContent = rs.data.blacklisted;
    document.getElementById('stat-posted').textContent = rs.data.posted;
  }
}

function updateStats() {
  document.getElementById('stat-total').textContent = stations.length;
  document.getElementById('stat-blocked').textContent = stations.filter(s => s.blacklisted).length;
}

// --- Render ---
function renderStations() {
  const q = document.getElementById('search').value.toLowerCase();
  const showBlocked = document.getElementById('show-blocked').checked;
  let filtered = stations.filter(s => {
    if (s.blacklisted && !showBlocked) return false;
    if (q && !s.name.toLowerCase().includes(q) && !s.api_url.toLowerCase().includes(q)) return false;
    return true;
  });

  const tbody = document.getElementById('station-list');
  tbody.innerHTML = filtered.map(s => `
    <tr class="${s.blacklisted ? 'blocked' : ''}">
      <td title="${s.description || ''}"><strong>${esc(s.name)}</strong><br><small>${esc(s.api_url).substring(0,50)}</small></td>
      <td>${s.interface_type || '-'}</td>
      <td>${s.resource_count || '-'}条</td>
      <td><span class="${parseFloat(s.availability) >= 50 ? 'green' : 'red'}">${s.availability || '-'}</span></td>
      <td>${s.response_time || '-'}</td>
      <td><small>${(JSON.parse(s.tags||'[]')).join(', ')}</small></td>
      <td class="actions">
        <button class="btn btn-sm" onclick="manualPost('${s.slug}')" ${s.blacklisted?'disabled':''}>📤</button>
        <button class="btn btn-sm ${s.blacklisted?'btn-danger':'btn-outline'}" onclick="toggleBlock('${s.slug}',${!s.blacklisted})">${s.blacklisted?'🔓':'🚫'}</button>
      </td>
    </tr>
  `).join('');
}

function renderHistory() {
  const tbody = document.getElementById('history-list');
  tbody.innerHTML = history.map(h => `
    <tr>
      <td>${new Date(h.posted_at).toLocaleString('zh-CN')}</td>
      <td>${esc(h.content)}</td>
      <td><span class="badge badge-${h.action}">${h.action}</span></td>
      <td>${h.message_id}</td>
    </tr>
  `).join('');
}

// --- Actions ---
async function triggerScrape() {
  toast('触发采集中...');
  const r = await api('/api/trigger', { method: 'POST' });
  toast(r.ok ? '采集已启动，查看日志' : '失败: ' + r.error);
  setTimeout(() => { loadStations(); loadStatus(); }, 5000);
}

async function toggleBlock(slug, blacklisted) {
  const r = await api(`/api/stations/${slug}/blacklist`, {
    method: 'POST',
    body: JSON.stringify({ blacklisted })
  });
  if (r.ok) {
    const st = stations.find(s => s.slug === slug);
    if (st) st.blacklisted = blacklisted;
    renderStations();
    updateStats();
    toast(`${blacklisted ? '已屏蔽' : '已解除'}: ${slug}`);
  }
}

async function manualPost(slug) {
  const r = await api(`/api/stations/${slug}/post`, { method: 'POST' });
  if (r.ok) {
    toast(`已手动推送, MSG #${r.data.message_id}`);
    loadHistory();
  } else {
    toast('推送失败: ' + r.error);
  }
}

async function saveConfig() {
  const body = {
    bot_token: document.getElementById('cfg-token').value,
    channel_id: parseInt(document.getElementById('cfg-channel').value) || 0,
    scrape_cron: document.getElementById('cfg-cron').value,
    post_format: document.getElementById('cfg-format').value,
  };
  const r = await api('/api/config', { method: 'PUT', body: JSON.stringify(body) });
  toast(r.ok ? '配置已保存' : '保存失败: ' + r.error);
}

// --- Tabs ---
function switchTab(name) {
  document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
  document.querySelectorAll('.tab-content').forEach(t => t.classList.add('hidden'));
  document.querySelector(`.tab[onclick*="${name}"]`).classList.add('active');
  document.getElementById('tab-' + name).classList.remove('hidden');
}

// --- WebSocket ---
function connectWS() {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const ws = new WebSocket(`${proto}//${location.host}/ws`);
  ws.onmessage = (e) => {
    const msg = JSON.parse(e.data);
    switch (msg.type) {
      case 'scrape_triggered': toast('采集任务已启动'); break;
      case 'scrape_complete': toast(`采集完成: ${msg.data.new_count}新 ${msg.data.upd_count}更新`); loadStations(); loadStatus(); break;
      case 'manual_post': toast(`手动推送 #${msg.data.message_id}`); loadHistory(); break;
      case 'blacklist_changed': loadStations(); break;
      case 'config_updated': toast('配置已更新'); break;
    }
  };
  ws.onclose = () => setTimeout(connectWS, 3000);
}

// --- Utils ---
function esc(s) { return (s || '').replace(/</g,'&lt;').replace(/>/g,'&gt;'); }
function toast(msg) {
  const el = document.getElementById('toast');
  el.textContent = msg;
  el.classList.add('show');
  setTimeout(() => el.classList.remove('show'), 2500);
}

init();
