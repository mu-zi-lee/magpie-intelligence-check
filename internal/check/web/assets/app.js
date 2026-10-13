import { $, text, icon, hydrateIcons, syncSelect, dateTime, names } from './ui.js';
import { renderModelTrace, runModelTrace } from '../modeltrace/panel.js';
import { analyzeAnswer } from './answer-analysis.js';
import { renderSVGPreview } from './svg-preview.js';
import { renderHTMLPreview, stopHTMLPreview } from './html-preview.js';

let data, polling = false, stopped = false, pollError = '', targetSignature, historySignature, historyLimit = 30;
const config = $('#config');
const secretType = 'pass' + 'word';
config.elements.gateway_key.type = secretType;
config.elements.gateway_key.autocomplete = 'new-' + secretType;
let configDirty = false;
let awaitingTest;
const testErrors = new Map();
let shownRecord, reviewDirty = false, analysisSignature;
hydrateIcons();

function feedback(value, success = false) {
  $('#message').textContent = value;
  $('#message').className = 'feedback' + (success ? ' success' : '');
}
async function request(path, body) {
  const response = await fetch(path, {
    method: body === undefined ? 'GET' : 'POST',
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!response.ok) {
    const raw = await response.text();
    if (response.status === 401) throw Error('管理会话已失效，请从 Magpie 插件重新打开控制台。');
    if (/<!doctype|<html/i.test(raw)) throw Error(`控制台连接暂时不可用（HTTP ${response.status}），请稍后重试；持续失败请从插件重新打开远程入口。`);
    throw Error(raw.trim() || `请求失败（HTTP ${response.status}）`);
  }
  return response.json();
}
async function pending(button, work, output = $('#message')) {
  if (button.disabled) return;
  button.disabled = true;
  button.setAttribute('aria-busy', 'true');
  try { await work(); }
  catch (error) { output.textContent = error.message; output.classList.remove('success'); output.classList.add('error'); }
  finally { button.disabled = false; button.removeAttribute('aria-busy'); renderControls(); }
}
function selectView(name, focus = false) {
  if (name !== 'tests') stopHTMLPreview();
  document.querySelectorAll('[role=tab]').forEach(tab => {
    const active = tab.dataset.view === name;
    tab.setAttribute('aria-selected', String(active));
    tab.tabIndex = active ? 0 : -1;
    $(`#view-${tab.dataset.view}`).hidden = !active;
    if (active && focus) tab.focus();
  });
}
const tabs = [...document.querySelectorAll('[role=tab]')];
tabs.forEach((tab, i) => {
  tab.onclick = () => selectView(tab.dataset.view);
  tab.onkeydown = event => {
    const index = { ArrowRight: (i + 1) % tabs.length, ArrowLeft: (i + tabs.length - 1) % tabs.length, Home: 0, End: tabs.length - 1 }[event.key];
    if (index !== undefined) { event.preventDefault(); selectView(tabs[index].dataset.view, true); }
  };
});
function theme(mode) {
  document.documentElement.dataset.theme = mode;
  const label = mode === 'dark' ? '切换为亮色' : '切换为暗色';
  $('#theme').setAttribute('aria-label', label);
  $('#theme').dataset.tooltip = label;
  $('#theme').replaceChildren(icon(mode === 'dark' ? 'Sun' : 'Moon'));
}
try { theme(localStorage.getItem('theme') === 'light' ? 'light' : 'dark'); } catch { theme('dark'); }
$('#theme').onclick = () => {
  const mode = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
  theme(mode);
  try { localStorage.setItem('theme', mode); } catch {}
};
function openSettings() {
  if (!$('#settingsDialog').open) $('#settingsDialog').showModal();
}
$('#settings').onclick = openSettings;
$('#connectSettings').onclick = openSettings;
$('#closeSettings').onclick = () => $('#settingsDialog').close();
$('#settingsDialog').onclick = event => { if (event.target === $('#settingsDialog')) {
  const rect = event.target.getBoundingClientRect();
  if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) event.target.close();
} };
config.oninput = () => { configDirty = true; };
$('#showKey').onclick = () => {
  const show = config.elements.gateway_key.type === secretType;
  config.elements.gateway_key.type = show ? 'text' : secretType;
  $('#showKey').replaceChildren(icon(show ? 'EyeOff' : 'Eye'));
  $('#showKey').setAttribute('aria-label', show ? '隐藏输入的 Key' : '显示输入的 Key');
  $('#showKey').dataset.tooltip = $('#showKey').getAttribute('aria-label');
};
function requireGateway() {
  if (data?.gateway_configured) return true;
  openSettings();
  $('#configMessage').textContent = '请保存 Magpie 网关 URL 和 Key；本机默认 Key 为 magpie。';
  config.elements.gateway_url.focus();
  return false;
}
function modelRunning(model) { return data?.state.results?.some(r => r.model === model && r.verdict === 'running'); }
function renderControls() {
  if (!data) return;
  $('#run').disabled = $('#run').hasAttribute('aria-busy') || !$('#model').value || modelRunning($('#model').value);
  renderTestAnswer();
  renderModelTrace(data);
}
function renderTestAnswer() {
  if (!data) return;
  const model = $('#model').value, prompt = $('#prompt').value;
  const submissionError = testErrors.get(JSON.stringify([model, prompt]));
  const records = data.state.results.filter(r => r.model === model && r.prompt === prompt).reverse();
  if (awaitingTest?.model === model && awaitingTest.prompt === prompt && records.some(r => !awaitingTest.ids.has(r.id))) awaitingTest = undefined;
  const waiting = awaitingTest?.model === model && awaitingTest.prompt === prompt;
  syncSelect($('#testRecord'), [['', '最新回答'], ...records.map(r => [r.id, `${dateTime(r.time)} / ${names[r.verdict] || r.verdict}`])]);
  $('#testRecord').disabled = waiting || !records.length;
  const record = waiting ? undefined : records.find(r => r.id === $('#testRecord').value) || records[0];
  const running = waiting || record?.verdict === 'running';
  $('#cancelTest').hidden = !data.state.results.some(r => r.model === model && r.verdict === 'running' && r.prompt !== 'modeltrace');
  const answer = record?.answer || '';
  const elapsed = record ? Math.max(0, Math.floor((Date.now() - Date.parse(record.time)) / 1000)) : 0;
  $('#testStatus').textContent = running ? `检测进行中，已等待 ${elapsed} 秒（最长 180 秒）。` : record ? record.verdict === 'unavailable' ? '检测未完成' : '检测完成' : modelRunning(model) ? '该模型正在运行其他检测。' : '';
  $('#testStatus').className = 'inline-state' + (record?.verdict === 'unavailable' ? ' error' : '');
  $('#answerMeta').textContent = record ? `${record.model} / ${dateTime(record.time)} / ${names[record.verdict] || record.verdict} / ${record.tokens || 0} tokens` : model ? `${model} / ${data.prompts.find(p => p.id === prompt)?.name || ''}` : '尚未选择测试模型';
  $('#answerError').hidden = !record?.error && !submissionError;
  $('#answerError').textContent = submissionError || record?.error || '';
  const content = answer || (running ? '正在等待模型回答…' : record ? record.error ? '本次请求未返回回答正文。' : '本次回答为空。' : '当前模型和题目暂无回答');
  if ($('#testAnswer').textContent !== content) $('#testAnswer').textContent = content;
  $('#testAnswer').setAttribute('aria-busy', String(Boolean(running)));
  $('#copyAnswer').disabled = !answer;
  $('#downloadAnswer').hidden = !answer;
  if (record) $('#downloadAnswer').href = '/api/results/' + encodeURIComponent(record.id) + '/download';
  const review = $('#answerReview');
  review.hidden = !answer || running;
  if (shownRecord !== record?.id) { reviewDirty = false; $('#reviewMessage').textContent = ''; }
  shownRecord = record?.id;
  if (record && !reviewDirty) {
    review.elements.decision.value = record.review || 'pending';
    review.elements.note.value = record.review_note || '';
  }
  $('#reviewStatus').textContent = record?.review ? ({ accepted: '已确认通过', rejected: '已标记问题', pending: '待确认' }[record.review] + ' / ' + dateTime(record.reviewed_at)) : '未审核';
  const signature = JSON.stringify([record?.id, record?.answer, record?.error, record?.verdict]);
  if (analysisSignature !== signature) {
    analysisSignature = signature;
    renderSVGPreview(running ? undefined : record);
    renderHTMLPreview(running ? undefined : record);
    const panel = $('#answerAnalysis'); panel.replaceChildren(); panel.hidden = !answer;
    if (answer) {
      panel.append(text('h3', '自动辅助检查'));
      for (const [label, value] of analyzeAnswer(record, data.prompts.find(p => p.id === prompt))) {
        const row = text('div', '', 'analysis-row'); row.append(text('span', label, 'muted'), text('span', value)); panel.append(row);
      }
    }
  }
}
function showPrompt() {
  $('#promptText').textContent = data?.prompts.find(p => p.id === $('#prompt').value)?.text || '';
  $('#guidance').textContent = data?.guidance?.[$('#prompt').value] || '';
}
function dayCount(model) {
  const day = new Date().toISOString().slice(0, 10);
  const cleared = data.state.cleared_usage?.[day] || {};
  return data.state.results.filter(r => (!model || r.model === model) && new Date(r.time).toISOString().slice(0, 10) === day).length + (model ? cleared[model] || 0 : Object.values(cleared).reduce((a, b) => a + b, 0));
}
function validPass(target) { return target.enabled && !target.held && target.verdict === 'passed' && Date.now() - Date.parse(target.checked_at) < 900000; }
function renderTargets() {
  const targets = Object.values(data.state.targets);
  const signature = JSON.stringify([targets, data.state.results.filter(r => r.verdict === 'running').map(r => r.model), data.state.config.daily_limit, targets.map(t => [dayCount(t.model), validPass(t), Date.parse(t.allow_until) > Date.now()])]);
  if (signature === targetSignature) return;
  targetSignature = signature;
  const rows = targets.map(target => {
    const row = document.createElement('tr');
    const model = text('td', target.model, 'model-id');
    model.dataset.label = '模型';
    const mode = document.createElement('select');
    mode.setAttribute('aria-label', `检测策略 ${target.model}`);
    for (const [value, label] of [['monitor', '检测＋告警'], ['strict', '严格模式：异常时拦截']]) {
      const option = text('option', label);
      option.value = value;
      option.disabled = value === 'strict' && target.model.startsWith('group/');
      mode.append(option);
    }
    mode.value = target.mode;
    mode.onchange = async () => {
      mode.disabled = true;
      try {
        await request('/api/action', { model: target.model, action: mode.value });
        feedback('检测策略已更新', true); await refresh();
      } catch (error) {
        mode.value = target.mode; feedback(error.message);
      } finally {
        mode.disabled = false;
      }
    };
    model.append(mode);
    const status = text('td', '');
    status.dataset.label = '状态';
    const allowed = Date.parse(target.allow_until) > Date.now();
    const label = !target.enabled ? '监测关闭' : allowed ? '限时放行' : target.held ? '人工暂停' : target.state === 'passed' && !validPass(target) ? '待复检' : target.state === 'review' ? (target.mode === 'strict' ? '质量异常（已拦截）' : '质量异常（仅告警）') : names[target.state] || target.state;
    const statusClass = !target.enabled ? '' : allowed ? 'passed' : target.held ? 'review' : target.state === 'passed' && !validPass(target) ? 'unverified' : target.state;
    status.append(text('span', label, 'state ' + statusClass));
    if (target.reason) status.append(text('span', target.reason, 'cell-meta'));
    if (target.state !== target.verdict && target.verdict !== 'unverified') {
      status.append(text('span', `上次质量结论：${target.verdict === 'passed' ? '通过' : '复测未通过'}`, 'cell-meta'));
    }
    const checked = text('td', dateTime(target.checked_at));
    checked.dataset.label = '最近质量结论';
    const budget = text('td', `${dayCount(target.model)} / ${data.state.config.daily_limit}`, 'numeric');
    budget.dataset.label = '今日预算 (UTC)';
    const actions = text('td', '');
    actions.dataset.label = '操作';
    const tools = text('div', '', 'actions');
    for (const [name, action, image] of [['重新检测', 'test', 'RefreshCw'], ['放行 15 分钟', 'allow', 'Clock3'], target.held ? ['解除暂停', 'resume', 'Play'] : ['暂停', 'hold', 'Pause']]) {
      const button = text('button', '', 'icon-button');
      button.type = 'button'; button.append(icon(image));
      button.setAttribute('aria-label', `${name} ${target.model}`);
      button.dataset.tooltip = name; button.title = name;
      button.disabled = (action !== 'test' && !target.enabled) || (action === 'test' && modelRunning(target.model));
      button.onclick = () => pending(button, async () => {
        if (action === 'test' && !requireGateway()) return;
        await request('/api/action', { model: target.model, action });
        feedback(`${target.model}：${action === 'test' ? '已开始检测' : action === 'allow' ? '已放行 15 分钟' : action === 'resume' ? '已解除暂停' : '已暂停'}`, true);
        await refresh();
      });
      tools.append(button);
    }
    const toggle = text('label', '', 'toggle');
    const input = document.createElement('input'); input.type = 'checkbox'; input.checked = target.enabled;
    input.setAttribute('aria-label', `监测 ${target.model}`);
    input.onchange = async () => {
      input.disabled = true;
      try { await request('/api/target', { model: target.model, enabled: input.checked }); await refresh(); }
      catch (error) { input.checked = target.enabled; feedback(error.message); }
      finally { input.disabled = false; }
    };
    toggle.append(input, text('span', '', 'toggle-track')); tools.append(toggle); actions.append(tools);
    const remove = text('button', '', 'icon-button'); remove.append(icon('Trash2'));
    remove.setAttribute('aria-label', `删除 ${target.model}`); remove.dataset.tooltip = '删除监测目标'; remove.disabled = modelRunning(target.model);
    remove.onclick = () => {
      if (!confirm(`删除 ${target.model}？该模型将不再受监测门禁限制，历史记录保留。`)) return;
      pending(remove, async () => { await request('/api/target/delete', { model: target.model }); feedback('已删除监测目标，历史记录保留', true); await refresh(); });
    };
    tools.append(remove);
    row.append(model, status, checked, budget, actions);
    return row;
  });
  if (!rows.length) {
    const row = text('tr', '', 'empty-row'); const cell = text('td', '添加一个模型开始监测', 'empty-state'); cell.colSpan = 5; row.append(cell); rows.push(row);
  }
  $('#targets').replaceChildren(...rows);
}
function filteredHistory() {
  return [...data.state.results].reverse().filter(r => (!$('#historyModel').value || r.model === $('#historyModel').value) && (!$('#historyVerdict').value || r.verdict === $('#historyVerdict').value) && (!$('#historyPrompt').value || r.prompt === $('#historyPrompt').value) && (!$('#historyReview').value || (r.review || 'pending') === $('#historyReview').value));
}
function renderHistory() {
  if (!data) return;
  syncSelect($('#historyPrompt'), [['', '全部题目'], ...data.prompts.map(p => [p.id, p.name]), ['modeltrace', 'ModelTrace']]);
  const filtered = filteredHistory();
  const candidates = filtered.filter(r => r.answer && r.verdict !== 'running');
  for (const side of ['A', 'B']) syncSelect($('#compare' + side), [['', '选择回答'], ...candidates.map(r => [r.id, `${r.model} / ${r.prompt} / ${dateTime(r.time)}`])]);
  renderComparison();
  $('#historyCount').textContent = `${filtered.length} 条记录`;
  $('#loadHistory').hidden = filtered.length <= historyLimit;
  const records = filtered.slice(0, historyLimit);
  const signature = JSON.stringify(records);
  if (signature === historySignature) return;
  historySignature = signature;
  const expanded = new Set([...$('#results').querySelectorAll('details[open]')].map(d => d.dataset.id));
  $('#results').replaceChildren(...records.map(record => {
    const details = text('details', '', 'record'); details.dataset.id = record.id; details.open = expanded.has(record.id);
    const summary = text('summary', '', 'history-summary');
    summary.append(text('span', dateTime(record.time), 'record-date'), text('span', record.model, 'record-model model-id'), text('span', data.prompts.find(p => p.id === record.prompt)?.name || 'ModelTrace', 'record-type'), text('span', names[record.verdict] || record.verdict, 'state ' + record.verdict), text('span', `${record.tokens || 0} tokens`, 'record-tokens numeric'));
    const body = text('div', '', 'record-body');
    if (record.error) body.append(text('p', record.error, 'inline-state error'));
    if (record.review) body.append(text('p', `人工审核：${({accepted:'确认通过', rejected:'标记问题', pending:'待确认'})[record.review]}${record.review_note ? ' / ' + record.review_note : ''}`, 'inline-state'));
    body.append(text('pre', record.answer || (record.verdict === 'running' ? '等待回答' : '未返回回答正文')));
    const download = text('a', '下载原始回答', 'download'); download.prepend(icon('Download'));
    download.href = '/api/results/' + encodeURIComponent(record.id) + '/download'; body.append(download);
    const prompt = record.prompt_text || data.prompts.find(p => p.id === record.prompt)?.text;
    if (prompt) body.append(text('p', '提示词原文', 'prompt-label'), text('pre', prompt));
    details.append(summary, body); return details;
  }));
  if (!records.length) $('#results').append(text('p', '暂无符合条件的记录', 'empty-state'));
}
function renderComparison() {
  for (const side of ['A', 'B']) {
    const record = data?.state.results.find(r => r.id === $('#compare' + side).value);
    $('#compareText' + side).textContent = record?.answer || '尚未选择回答';
    $('#compareMeta' + side).textContent = record ? `${record.model} / ${record.prompt} / ${dateTime(record.time)} / ${record.tokens || 0} tokens / ${{accepted:'确认通过', rejected:'标记问题', pending:'待确认'}[record.review || 'pending']}${record.review_note ? ' / ' + record.review_note : ''}` : '';
  }
}
$('#compareA').onchange = $('#compareB').onchange = renderComparison;
async function refresh() {
  if (polling || stopped) return;
  polling = true;
  try {
    data = await request('/api/state');
    if (pollError && $('#message').textContent === pollError) feedback('');
    pollError = '';
    $('#health').textContent = data.health ? '服务异常' : data.gateway_configured ? '网关已配置' : '等待连接';
    $('#health').className = 'service-state ' + (data.health ? 'warning' : data.gateway_configured ? 'connected' : '');
    $('#health').title = data.health || '';
    $('#connectionNotice').hidden = data.gateway_configured;
    $('#keyState').textContent = data.gateway_configured ? 'Key 已保存；留空保留已保存的 Key。' : '尚未保存 Key；本机默认 Key 为 magpie。';
    $('#logHealth').textContent = data.log_health;
    if (!configDirty) for (const key of ['gateway_url', 'log_path', 'daily_limit']) config.elements[key].value = data.state.config[key];
    const targets = Object.values(data.state.targets);
    $('#targetCount').textContent = targets.length;
    $('#statEnabled').textContent = targets.filter(t => t.enabled).length;
    $('#statPassed').textContent = targets.filter(validPass).length;
    $('#statReview').textContent = targets.filter(t => t.enabled && (t.held || t.verdict === 'review' || t.state === 'unavailable')).length;
    $('#statRequests').textContent = dayCount();
    syncSelect($('#model'), targets.map(t => [t.model, t.model + (t.enabled ? '' : '（监测关闭）')]));
    syncSelect($('#prompt'), data.prompts.map(p => [p.id, p.name]));
    syncSelect($('#historyModel'), [['', '全部模型'], ...[...new Set([...targets.map(t => t.model), ...data.state.results.map(r => r.model)])].map(m => [m, m])]);
    showPrompt(); renderTargets(); renderControls(); renderHistory();
    $('#lastSync').textContent = '更新于 ' + dateTime(new Date().toISOString());
  } catch (error) {
    pollError = error.message; feedback(pollError);
    $('#health').textContent = '连接中断'; $('#health').className = 'service-state warning';
  } finally { polling = false; }
}
function changeTest() { $('#testRecord').value = ''; showPrompt(); renderControls(); }
$('#prompt').onchange = changeTest;
$('#model').onchange = changeTest;
$('#testRecord').onchange = renderTestAnswer;
$('#answerReview').oninput = () => { reviewDirty = true; };
$('#answerReview').onsubmit = event => {
  event.preventDefault();
  const id = shownRecord;
  const values = Object.fromEntries(new FormData(event.target));
  pending(event.target.querySelector('button'), async () => {
    await request('/api/results/' + encodeURIComponent(id) + '/review', values);
    if (shownRecord === id) { reviewDirty = false; $('#reviewMessage').textContent = '审核已保存'; }
    await refresh();
  }, $('#reviewMessage'));
};
$('#copyAnswer').onclick = () => pending($('#copyAnswer'), async () => {
  await navigator.clipboard.writeText($('#testAnswer').textContent);
  feedback('回答已复制', true);
});
for (const id of ['historyModel', 'historyVerdict', 'historyPrompt', 'historyReview']) $('#' + id).onchange = () => { historyLimit = 30; renderHistory(); };
$('#exportHistory').onclick = () => {
  if (!data) return;
  const url = URL.createObjectURL(new Blob([JSON.stringify({ exported_at: new Date().toISOString(), results: filteredHistory() }, null, 2)], { type: 'application/json' }));
  const link = document.createElement('a');
  link.href = url; link.download = 'intelligence-check-results.json'; link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
};
$('#clearHistory').onclick = () => pending($('#clearHistory'), async () => {
  const records = filteredHistory();
  if (!records.length) return;
  if (!confirm(`永久清理当前筛选的 ${records.length} 条记录？原始回答与审核将删除，已消耗预算和模型门禁保持不变。`)) return;
  await request('/api/results/clear', { ids: records.map(r => r.id) });
  await refresh();
  feedback('记录已清理，预算与模型门禁保持不变', true);
});
$('#loadHistory').onclick = () => { historyLimit += 30; renderHistory(); };
$('#run').onclick = () => pending($('#run'), async () => {
  if (!requireGateway()) return;
  const model = $('#model').value, prompt = $('#prompt').value;
  testErrors.delete(JSON.stringify([model, prompt]));
  awaitingTest = { model, prompt, ids: new Set(data.state.results.map(r => r.id)) };
  $('#testRecord').value = ''; renderTestAnswer();
  try {
    await request('/api/action', { model, action: 'test', prompt });
    feedback('已开始检测', true); await refresh();
  } catch (error) {
    awaitingTest = undefined;
    testErrors.set(JSON.stringify([model, prompt]), error.message);
    renderTestAnswer();
    throw error;
  }
});
$('#cancelTest').onclick = () => pending($('#cancelTest'), async () => {
  await request('/api/action', { model: $('#model').value, action: 'cancel-test' });
  await refresh();
});
$('#traceRun').onclick = () => pending($('#traceRun'), async () => {
  if (!requireGateway()) return;
  await runModelTrace(request); feedback('已开始三条指纹挑战', true); await refresh();
});
$('#loadModels').onclick = () => pending($('#loadModels'), async () => {
  if (!requireGateway()) return;
  const models = await request('/api/models');
  $('#catalog').replaceChildren(...models.map(m => { const option = text('option', m.group); option.value = m.id; return option; }));
  feedback(`已读取 ${models.length} 个模型`, true);
});
$('#target').onsubmit = event => {
  event.preventDefault();
  const form = event.target;
  pending(form.querySelector('button:last-child'), async () => {
    await request('/api/target', { model: form.elements.model.value.trim(), enabled: true });
    form.reset(); feedback('已添加监测目标', true); await refresh();
  });
};
async function saveConfig() {
  const values = Object.fromEntries(new FormData(config)); values.daily_limit = Number(values.daily_limit);
  await request('/api/config', values);
  config.elements.gateway_key.value = ''; config.elements.gateway_key.type = secretType;
  $('#showKey').replaceChildren(icon('Eye'));
  $('#showKey').setAttribute('aria-label', '显示输入的 Key');
  $('#showKey').dataset.tooltip = '显示输入的 Key';
  configDirty = false; await refresh();
}
async function checkConnection() {
  const result = await request('/api/connection/check', {});
  $('#configMessage').textContent = `连接成功：${result.model_count} 个模型`;
  $('#configMessage').className = 'inline-state success';
}
config.onsubmit = event => {
  event.preventDefault();
  pending(config.querySelector('button.primary'), async () => {
    await saveConfig();
    $('#configMessage').textContent = '设置已保存，正在验证连接…';
    await checkConnection();
  }, $('#configMessage'));
};
$('#checkConnection').onclick = () => pending($('#checkConnection'), async () => {
  if (configDirty || !data?.gateway_configured) {
    $('#configMessage').textContent = '请先保存当前连接设置。'; return;
  }
  await checkConnection();
}, $('#configMessage'));
$('#closeRemote').onclick = () => pending($('#closeRemote'), async () => {
  await request('/api/console/close', {}); stopped = true;
  feedback('远程访问已结束，请从插件重新打开。');
});
await refresh();
setInterval(refresh, 3000);
