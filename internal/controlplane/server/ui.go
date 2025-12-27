package server

import (
	"net/http"
)

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(uiHTML))
}

const uiHTML = `<!doctype html>
<html>
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1"/>
  <title>gobet controlplane</title>
  <style>
    body { font-family: ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, Arial; margin: 0; }
    .wrap { display: grid; grid-template-columns: 320px 1fr; height: 100vh; }
    .left { border-right: 1px solid #eee; padding: 12px; overflow:auto; }
    .right { padding: 12px; overflow:auto; }
    .bot { padding: 8px; border: 1px solid #eee; border-radius: 8px; margin-bottom: 8px; cursor: pointer; }
    .bot:hover { background: #fafafa; }
    textarea { width: 100%; min-height: 240px; font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; }
    pre { background:#0b1020; color:#d6e2ff; padding:12px; border-radius:8px; overflow:auto; min-height: 220px; }
    button { margin-right: 8px; }
    .row { display:flex; gap: 8px; align-items:center; flex-wrap: wrap; }
    .muted { color:#666; font-size: 12px; }
  </style>
</head>
<body>
<div class="wrap">
  <div class="left">
    <div class="row">
      <h3 style="margin:0">Bots</h3>
      <button onclick="reloadBots()">刷新</button>
    </div>
    <div class="muted">提示：先用右侧“创建Bot”提交一份完整配置（包含 wallet/market/exchangeStrategies）。</div>
    <div id="bots"></div>
  </div>
  <div class="right">
    <h3 style="margin-top:0">Bot 详情</h3>
    <div id="detail" class="muted">请选择左侧 bot</div>

    <hr/>
    <h3>创建 Bot</h3>
    <div class="row">
      <input id="newName" placeholder="bot 名称" style="width:260px"/>
      <button onclick="createBot()">创建</button>
    </div>
    <div class="muted">服务器会强制注入 log_file 与 persistence_dir（按 bot_id 隔离）。</div>
    <textarea id="newCfg" placeholder="粘贴 YAML 配置"></textarea>

    <hr/>
    <h3>账号（助记词）</h3>
    <div class="row">
      <button onclick="reloadAccounts()">刷新账号</button>
      <button onclick="runBalanceBatch()">批量同步余额</button>
      <button onclick="runRedeemBatch()">批量Redeem</button>
      <button onclick="runTradesBatch()">批量同步交易</button>
      <button onclick="runPositionsBatch()">批量同步持仓</button>
      <button onclick="runOpenOrdersBatch()">批量同步挂单</button>
      <button onclick="runEquityBatch()">批量生成净值快照</button>
      <button onclick="reloadJobRuns()">刷新任务记录</button>
    </div>
    <div id="accounts" class="muted"></div>
    <div id="jobs" class="muted"></div>
    <h4>创建账号</h4>
    <div class="row">
      <input id="accName" placeholder="账号名称" style="width:180px"/>
      <input id="accPath" placeholder="派生路径 (m/...)" style="width:260px" value="m/44'/60'/0'/0/0"/>
      <input id="accFunder" placeholder="funder/safe 地址 0x..." style="width:360px"/>
    </div>
    <textarea id="accMnemonic" placeholder="助记词（会加密存储，默认不回显）" style="min-height:100px"></textarea>
    <div class="row">
      <button onclick="createAccount()">创建账号</button>
    </div>
  </div>
</div>

<script>
let selectedBotId = null;
let logES = null;
let accountsCache = [];

async function api(path, opts) {
  const res = await fetch(path, Object.assign({headers: {'Content-Type':'application/json'}}, opts||{}));
  const data = await res.json().catch(()=> ({}));
  if (!res.ok) throw new Error(data.error || ('HTTP '+res.status));
  return data;
}

function botCard(b) {
  const div = document.createElement('div');
  div.className = 'bot';
  div.onclick = () => selectBot(b.id);
  div.innerHTML = '<b>'+escapeHTML(b.name)+'</b><div class="muted">'+escapeHTML(b.id)+'</div>';
  return div;
}

function escapeHTML(s){ return (s||'').replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('>','&gt;'); }

async function reloadBots() {
  const bots = await api('/api/bots');
  const root = document.getElementById('bots');
  root.innerHTML = '';
  bots.forEach(b => root.appendChild(botCard(b)));
}

async function reloadAccounts() {
  const accounts = await api('/api/accounts');
  accountsCache = accounts || [];
  const root = document.getElementById('accounts');
  if (accountsCache.length === 0) {
    root.innerHTML = '暂无账号';
    return;
  }
  let html = '<div><b>账号列表</b></div><ul>';
  for (const a of accountsCache) {
    html += '<li><span class="muted">'+escapeHTML(a.id)+'</span> - <b>'+escapeHTML(a.name)+'</b> - '+escapeHTML(a.eoa_address)+' - path='+escapeHTML(a.derivation_path)+'</li>';
  }
  html += '</ul>';
  root.innerHTML = html;
}

async function reloadJobRuns() {
  const runs = await api('/api/jobs/runs?limit=30');
  const root = document.getElementById('jobs');
  if (!runs || runs.length === 0) { root.innerHTML = '暂无任务记录'; return; }
  let html = '<div><b>最近任务</b></div><ul>';
  for (const r of runs) {
    const ok = (r.ok === true) ? 'OK' : (r.ok === false ? 'FAIL' : 'RUNNING');
    html += '<li>#'+r.id+' '+escapeHTML(r.job_name)+' ['+escapeHTML(r.scope)+'] '+ok+' <span class="muted">'+escapeHTML(r.started_at||'')+'</span></li>';
  }
  html += '</ul>';
  root.innerHTML = html;
}

async function selectBot(id) {
  selectedBotId = id;
  if (logES) { try { logES.close(); } catch(e) {} logES = null; }
  const data = await api('/api/bots/'+id);
  const b = data.bot;
  const p = data.process || {};

  const detail = document.getElementById('detail');
  detail.innerHTML =
    '<div class="row">' +
      '<div><b>' + escapeHTML(b.name) + '</b> <span class="muted">(' + escapeHTML(b.id) + ')</span></div>' +
    '</div>' +
    '<div class="row">' +
      '<button onclick="startBot()">启动</button>' +
      '<button onclick="stopBot()">停止</button>' +
      '<button onclick="restartBot()">重启</button>' +
      '<button onclick="loadLogTail()">加载日志tail</button>' +
      '<span class="muted">pid: ' + (p.pid || '-') + '</span>' +
    '</div>' +
    '<div class="muted">config: ' + escapeHTML(b.config_path) + ' | log: ' + escapeHTML(b.log_path) + '</div>' +
    '<h4>配置（保存后手动重启生效）</h4>' +
    '<textarea id="cfg">' + escapeHTML(b.config_yaml) + '</textarea>' +
    '<div class="row">' +
      '<button onclick="saveConfig()">保存配置</button>' +
      '<button onclick="showVersions()">版本/回滚</button>' +
      '<button onclick="bindAccount()">绑定账号到此Bot</button>' +
    '</div>' +
    '<div id="versions" class="muted"></div>' +
    '<h4>实时日志</h4>' +
    '<pre id="log"></pre>';

  // start SSE
  const logEl = document.getElementById('log');
  logEl.textContent = '';
  logES = new EventSource('/api/bots/'+id+'/logs/stream');
  logES.onmessage = (ev) => {
    logEl.textContent += ev.data + "\\n";
    logEl.scrollTop = logEl.scrollHeight;
  };
  logES.onerror = () => {
    // 静默
  };
}

async function showVersions() {
  const data = await api('/api/bots/'+selectedBotId+'/config/versions?limit=30');
  const cur = data.current_version || 0;
  const list = data.versions || [];
  let html = '<div><b>当前版本:</b> v'+cur+'</div>';
  html += '<div class="row"><button onclick="promptRollback()">回滚到版本...</button></div>';
  html += '<div class="muted">最近版本:</div>';
  html += '<ul>';
  for (const v of list) {
    const c = v.comment ? (' - '+escapeHTML(v.comment)) : '';
    html += '<li>v'+v.version+' '+escapeHTML(v.created_at || '')+c+'</li>';
  }
  html += '</ul>';
  document.getElementById('versions').innerHTML = html;
}

async function promptRollback() {
  const v = prompt('输入要回滚到的版本号（例如 1）');
  if (!v) return;
  const n = parseInt(v, 10);
  if (!n || n <= 0) { alert('版本号无效'); return; }
  const res = await api('/api/bots/'+selectedBotId+'/config/rollback', {method:'POST', body: JSON.stringify({version: n})});
  alert('已回滚到新版本 v'+res.current_version+'（需要手动重启生效）');
  await selectBot(selectedBotId);
}

async function createBot() {
  const name = document.getElementById('newName').value.trim();
  const cfg = document.getElementById('newCfg').value;
  const b = await api('/api/bots', {method:'POST', body: JSON.stringify({name, config_yaml: cfg})});
  await reloadBots();
  await selectBot(b.id);
}

async function saveConfig() {
  const cfg = document.getElementById('cfg').value;
  await api('/api/bots/'+selectedBotId+'/config', {method:'PUT', body: JSON.stringify({config_yaml: cfg})});
  alert('已保存（需要手动重启生效）');
}

async function startBot() {
  await api('/api/bots/'+selectedBotId+'/start', {method:'POST', body:'{}'});
  alert('已触发启动');
}
async function stopBot() {
  await api('/api/bots/'+selectedBotId+'/stop', {method:'POST', body:'{}'});
  alert('已触发停止');
}
async function restartBot() {
  await api('/api/bots/'+selectedBotId+'/restart', {method:'POST', body:'{}'});
  alert('已触发重启');
}

async function loadLogTail() {
  const data = await api('/api/bots/'+selectedBotId+'/logs?tail=200');
  const logEl = document.getElementById('log');
  logEl.textContent = (data.lines || []).join("\\n") + "\\n";
  logEl.scrollTop = logEl.scrollHeight;
}

async function createAccount() {
  const name = document.getElementById('accName').value.trim();
  const mnemonic = document.getElementById('accMnemonic').value.trim();
  const derivation_path = document.getElementById('accPath').value.trim();
  const funder_address = document.getElementById('accFunder').value.trim();
  await api('/api/accounts', {method:'POST', body: JSON.stringify({name, mnemonic, derivation_path, funder_address})});
  alert('账号已创建');
  document.getElementById('accMnemonic').value = '';
  await reloadAccounts();
}

async function bindAccount() {
  if (!selectedBotId) { alert('请先选择一个 bot'); return; }
  await reloadAccounts();
  if (accountsCache.length === 0) { alert('没有可绑定的账号'); return; }
  const hint = accountsCache.map(a => a.id+' ('+a.name+')').join('\\n');
  const id = prompt('输入要绑定的 account_id：\\n'+hint);
  if (!id) return;
  const res = await api('/api/bots/'+selectedBotId+'/bind_account', {method:'POST', body: JSON.stringify({account_id: id.trim()})});
  alert('已绑定，生成新配置版本 v'+res.current_version+'；请手动重启生效');
  await selectBot(selectedBotId);
}

async function runBalanceBatch() {
  const res = await api('/api/jobs/balance_sync', {method:'POST', body: JSON.stringify({trigger:'manual_ui'})});
  alert('已触发余额同步，run_id='+res.run_id);
  await reloadJobRuns();
}

async function runRedeemBatch() {
  const res = await api('/api/jobs/redeem', {method:'POST', body: JSON.stringify({trigger:'manual_ui'})});
  alert('已触发Redeem，run_id='+res.run_id);
  await reloadJobRuns();
}

async function runTradesBatch() {
  const res = await api('/api/jobs/trades_sync', {method:'POST', body: JSON.stringify({trigger:'manual_ui'})});
  alert('已触发交易同步，run_id='+res.run_id);
  await reloadJobRuns();
}

async function runPositionsBatch() {
  const res = await api('/api/jobs/positions_sync', {method:'POST', body: JSON.stringify({trigger:'manual_ui'})});
  alert('已触发持仓同步，run_id='+res.run_id);
  await reloadJobRuns();
}

async function runOpenOrdersBatch() {
  const res = await api('/api/jobs/open_orders_sync', {method:'POST', body: JSON.stringify({trigger:'manual_ui'})});
  alert('已触发挂单同步，run_id='+res.run_id);
  await reloadJobRuns();
}

async function runEquityBatch() {
  const res = await api('/api/jobs/equity_snapshot', {method:'POST', body: JSON.stringify({trigger:'manual_ui'})});
  alert('已触发净值快照，run_id='+res.run_id);
  await reloadJobRuns();
}

reloadBots();
reloadAccounts();
reloadJobRuns();
</script>
</body>
</html>`
