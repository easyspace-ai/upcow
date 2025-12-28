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
    .acct { padding: 8px; border: 1px solid #eee; border-radius: 8px; margin-bottom: 8px; cursor: pointer; }
    .acct:hover { background: #fafafa; }
    textarea { width: 100%; min-height: 240px; font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; }
    pre { background:#0b1020; color:#d6e2ff; padding:12px; border-radius:8px; overflow:auto; min-height: 220px; }
    button { margin-right: 8px; }
    .row { display:flex; gap: 8px; align-items:center; flex-wrap: wrap; }
    .muted { color:#666; font-size: 12px; }
    .grid2 { display:grid; grid-template-columns: 1fr 1fr; gap: 12px; }
  </style>
</head>
<body>
<div class="wrap">
  <div class="left">
    <div class="row">
      <h3 style="margin:0">Bots</h3>
      <button onclick="reloadBots()">刷新</button>
    </div>
    <div class="muted">提示：Bot 配置无需包含 wallet（私钥不会入库）。启动前请先绑定 3 位账号ID（1账号1bot）。</div>
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
    <div id="accountDetail" class="muted"></div>
    <div id="jobs" class="muted"></div>
    <h4>创建账号</h4>
    <div class="row">
      <input id="accId" placeholder="账号ID(三位数，例如 456)" style="width:220px"/>
      <input id="accName" placeholder="账号名称(可选)" style="width:220px"/>
    </div>
    <div class="muted">
      助记词不会通过网页提交。请在服务启动前使用 <code>cmd/mnemonic-init</code> 生成本地加密助记词文件（默认 <code>data/mnemonic.enc</code>），并设置 <code>GOBET_MASTER_KEY</code>。
    </div>
    <div class="row">
      <button onclick="createAccount()">创建账号</button>
    </div>
  </div>
</div>

<script>
let selectedBotId = null;
let selectedAccountId = null;
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
  const root = document.getElementById('bots');
  root.innerHTML = '';
  try {
    const bots = await api('/api/bots');
    const list = Array.isArray(bots) ? bots : [];
    if (list.length === 0) { root.innerHTML = '<div class="muted">暂无 bots</div>'; return; }
    list.forEach(b => root.appendChild(botCard(b)));
  } catch (e) {
    root.innerHTML = '<div class="muted">加载 bots 失败：'+escapeHTML(String(e && e.message ? e.message : e))+'</div>';
  }
}

async function reloadAccounts() {
  const root = document.getElementById('accounts');
  try {
    const accounts = await api('/api/accounts');
    accountsCache = Array.isArray(accounts) ? accounts : [];
    if (accountsCache.length === 0) { root.innerHTML = '暂无账号'; return; }
    root.innerHTML = '<div><b>账号列表</b></div>';
    for (const a of accountsCache) {
      const div = document.createElement('div');
      div.className = 'acct';
      div.onclick = () => selectAccount(a.id);
      div.innerHTML = '<b>'+escapeHTML(a.name)+'</b><div class="muted">'+escapeHTML(a.id)+'</div><div class="muted">'+escapeHTML(a.eoa_address)+'</div>';
      root.appendChild(div);
    }
  } catch (e) {
    accountsCache = [];
    root.innerHTML = '<div class="muted">加载账号失败：'+escapeHTML(String(e && e.message ? e.message : e))+'</div>';
  }
}

async function reloadJobRuns() {
  const qs = selectedAccountId ? ('&account_id='+encodeURIComponent(selectedAccountId)) : '';
  const root = document.getElementById('jobs');
  try {
    const runs = await api('/api/jobs/runs?limit=30'+qs);
    const list = Array.isArray(runs) ? runs : [];
    if (list.length === 0) { root.innerHTML = '暂无任务记录'; return; }
    let html = '<div><b>最近任务</b></div><ul>';
    for (const r of list) {
      const ok = (r.ok === true) ? 'OK' : (r.ok === false ? 'FAIL' : 'RUNNING');
      html += '<li>#'+r.id+' '+escapeHTML(r.job_name)+' ['+escapeHTML(r.scope)+'] '+ok+' <span class="muted">'+escapeHTML(r.started_at||'')+'</span></li>';
    }
    html += '</ul>';
    root.innerHTML = html;
  } catch (e) {
    root.innerHTML = '<div class="muted">加载任务失败：'+escapeHTML(String(e && e.message ? e.message : e))+'</div>';
  }
}

async function selectAccount(id) {
  selectedAccountId = id;
  await reloadJobRuns();
  const detail = document.getElementById('accountDetail');
  detail.innerHTML = '<div class="muted">加载中...</div>';

  const acc = await api('/api/accounts/'+id);
  const stats = await api('/api/accounts/'+id+'/stats');
  const equity = await api('/api/accounts/'+id+'/equity?limit=60');
  const balances = await api('/api/accounts/'+id+'/balances?limit=60');
  const positions = await api('/api/accounts/'+id+'/positions');
  const orders = await api('/api/accounts/'+id+'/open_orders');
  const trades = await api('/api/accounts/'+id+'/trades?limit=50');

  const eq = equity.equity || [];
  const bal = balances.balances || [];
  const latestEq = eq.length ? eq[0].total_equity_usdc : null;
  const oldestEq = eq.length ? eq[eq.length-1].total_equity_usdc : null;
  const deltaEq = (latestEq != null && oldestEq != null) ? (latestEq - oldestEq) : null;
  const latestBal = bal.length ? bal[0].balance_usdc : null;

  const posList = (positions.positions || []);
  const ordList = (orders.open_orders || []);
  const trList = (trades.trades || []);

  let html = '';
  html += '<div class="row"><b>账号:</b> '+escapeHTML(acc.account.name)+' <span class="muted">('+escapeHTML(acc.account.id)+')</span></div>';
  html += '<div class="muted">funder: '+escapeHTML(acc.account.funder_address)+' | eoa: '+escapeHTML(acc.account.eoa_address)+' | path: '+escapeHTML(acc.account.derivation_path)+'</div>';
  html += '<div class="row">';
  html += '<button onclick="accSyncBalance()">同步余额</button>';
  html += '<button onclick="accSyncTrades()">同步交易</button>';
  html += '<button onclick="accSyncPositions()">同步持仓</button>';
  html += '<button onclick="accSyncOpenOrders()">同步挂单</button>';
  html += '<button onclick="accRedeem()">Redeem</button>';
  html += '<button onclick="accEquitySnapshot()">生成净值快照</button>';
  html += '</div>';

  html += '<div class="grid2">';
  html += '<div><b>概览</b><div class="muted">最新余额: '+(latestBal==null?'-':latestBal.toFixed(6))+' USDC</div>';
  html += '<div class="muted">最新净值: '+(latestEq==null?'-':latestEq.toFixed(6))+' USDC</div>';
  html += '<div class="muted">净值变化(样本窗口): '+(deltaEq==null?'-':deltaEq.toFixed(6))+' USDC</div>';
  html += '<div class="muted">24h Trades: '+escapeHTML(String(stats.trades||0))+' | 24h Volume: '+escapeHTML(String(stats.volume_usdc||0))+'</div></div>';
  html += '<div><b>数量</b><div class="muted">持仓条目: '+posList.length+' | 挂单: '+ordList.length+' | 最近成交: '+trList.length+'</div></div>';
  html += '</div>';

  html += '<hr/><b>净值快照(最近)</b><pre>'+eq.map(e => (e.ts+' total='+e.total_equity_usdc)).slice(0,20).join('\\n')+'</pre>';
  html += '<b>余额快照(最近)</b><pre>'+bal.map(b => (b.ts+' balance='+b.balance_usdc+' src='+b.source)).slice(0,20).join('\\n')+'</pre>';
  html += '<b>持仓(当前)</b><pre>'+posList.slice(0,50).map(p => (p.slug+' '+p.outcome+' size='+p.size+' cur='+p.cur_price)).join('\\n')+'</pre>';
  html += '<b>挂单(当前)</b><pre>'+ordList.slice(0,50).map(o => (o.market+' '+o.side+' '+o.price+' size='+o.original_size+' matched='+o.size_matched)).join('\\n')+'</pre>';
  html += '<b>成交(最近)</b><pre>'+trList.slice(0,50).map(t => (t.match_time_ts+' '+t.side+' '+t.market+' '+t.price+' size='+t.size)).join('\\n')+'</pre>';

  detail.innerHTML = html;
}

async function accSyncBalance(){ const r = await api('/api/accounts/'+selectedAccountId+'/sync_balance',{method:'POST',body:'{}'}); alert('已触发同步余额 run_id='+r.run_id); await reloadJobRuns(); }
async function accSyncTrades(){ const r = await api('/api/accounts/'+selectedAccountId+'/sync_trades',{method:'POST',body:'{}'}); alert('已触发同步交易 run_id='+r.run_id); await reloadJobRuns(); }
async function accSyncPositions(){ const r = await api('/api/accounts/'+selectedAccountId+'/sync_positions',{method:'POST',body:'{}'}); alert('已触发同步持仓 run_id='+r.run_id); await reloadJobRuns(); }
async function accSyncOpenOrders(){ const r = await api('/api/accounts/'+selectedAccountId+'/sync_open_orders',{method:'POST',body:'{}'}); alert('已触发同步挂单 run_id='+r.run_id); await reloadJobRuns(); }
async function accRedeem(){ const r = await api('/api/accounts/'+selectedAccountId+'/redeem',{method:'POST',body:'{}'}); alert('已触发Redeem run_id='+r.run_id); await reloadJobRuns(); }
async function accEquitySnapshot(){ const r = await api('/api/accounts/'+selectedAccountId+'/equity_snapshot',{method:'POST',body:'{}'}); alert('已生成净值快照'); await reloadJobRuns(); }

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
  try {
    if (!selectedBotId) {
      alert('请先选择一个 bot');
      return;
    }
    const res = await api('/api/bots/'+selectedBotId+'/start', {method:'POST', body:'{}'});
    if (res && res.already_running) {
      alert('Bot 已在运行中 (pid: ' + (res.pid || '-') + ')');
    } else {
      alert('Bot 已启动 (pid: ' + (res.pid || '-') + ')');
    }
    await selectBot(selectedBotId); // 刷新状态
  } catch (err) {
    alert('启动失败：' + err.message);
    console.error('startBot error:', err);
  }
}
async function stopBot() {
  try {
    if (!selectedBotId) {
      alert('请先选择一个 bot');
      return;
    }
    const res = await api('/api/bots/'+selectedBotId+'/stop', {method:'POST', body:'{}'});
    if (res && res.already_stopped) {
      alert('Bot 已停止');
    } else {
      alert('Bot 已停止');
    }
    await selectBot(selectedBotId); // 刷新状态
  } catch (err) {
    alert('停止失败：' + err.message);
    console.error('stopBot error:', err);
  }
}
async function restartBot() {
  try {
    if (!selectedBotId) {
      alert('请先选择一个 bot');
      return;
    }
    const res = await api('/api/bots/'+selectedBotId+'/restart', {method:'POST', body:'{}'});
    alert('Bot 已重启 (pid: ' + (res.pid || '-') + ')');
    await selectBot(selectedBotId); // 刷新状态
  } catch (err) {
    alert('重启失败：' + err.message);
    console.error('restartBot error:', err);
  }
}

async function loadLogTail() {
  const data = await api('/api/bots/'+selectedBotId+'/logs?tail=200');
  const logEl = document.getElementById('log');
  logEl.textContent = (data.lines || []).join("\\n") + "\\n";
  logEl.scrollTop = logEl.scrollHeight;
}

async function createAccount() {
  try {
    const account_id = document.getElementById('accId').value.trim();
    const name = document.getElementById('accName').value.trim();
    if (!account_id) {
      alert('请输入账号ID（三位数，例如 456）');
      return;
    }
    const res = await api('/api/accounts', {method:'POST', body: JSON.stringify({account_id, name})});
    if (res && res.warning) {
      alert('账号已创建（有提示）：' + res.warning);
    } else {
      alert('账号已创建');
    }
    await reloadAccounts();
  } catch (err) {
    alert('创建账号失败：' + err.message);
    console.error('createAccount error:', err);
  }
}

async function bindAccount() {
  if (!selectedBotId) { alert('请先选择一个 bot'); return; }
  await reloadAccounts();
  if (accountsCache.length === 0) { alert('没有可绑定的账号'); return; }
  const hint = accountsCache.map(a => a.id+' ('+a.name+')').join('\\n');
  const id = prompt('输入要绑定的 account_id：\\n'+hint);
  if (!id) return;
  const res = await api('/api/bots/'+selectedBotId+'/bind_account', {method:'POST', body: JSON.stringify({account_id: id.trim()})});
  alert('已绑定：account_id='+res.account_id+'；请手动重启生效');
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
