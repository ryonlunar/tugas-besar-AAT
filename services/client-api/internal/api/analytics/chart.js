const byId = id => document.getElementById(id);
const format = new Intl.NumberFormat('id-ID');
const types = [
  ['SEISMIC', 'Gempa', '#086d69'], ['TSUNAMI', 'Tsunami', '#355ab2'],
  ['VOLCANIC', 'Gunung api', '#bc4822'], ['WILDFIRE', 'Kebakaran hutan', '#973670'],
  ['COASTAL', 'Pesisir', '#568315'],
];
const localInput = date => new Date(date - date.getTimezoneOffset() * 60_000).toISOString().slice(0, 16);
const now = new Date();
byId('to').value = localInput(now);
byId('from').value = localInput(new Date(now.getTime() - 7 * 86_400_000));

function fillTable(id, values) {
  const rows = Object.entries(values).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
  const table = byId(id).tBodies[0];
  table.replaceChildren();
  for (const [name, count] of rows) {
    const row = table.insertRow();
    row.insertCell().textContent = name;
    row.insertCell().textContent = format.format(count);
  }
}

function plot(data, from, to) {
  const interval = { '1 hour': 3_600_000, '1 day': 86_400_000, '7 days': 604_800_000 }[data.step];
  const start = Math.floor(+from / interval) * interval;
  const bins = Math.ceil((+to - start) / interval);
  const values = new Map(data.series.map(point => [point.category + ':' + Date.parse(point.bucket), point.count]));
  const max = Math.max(1, ...data.series.map(point => point.count));
  const svg = byId('chart');
  svg.replaceChildren();
  const add = (name, attributes, content) => {
    const el = document.createElementNS('http://www.w3.org/2000/svg', name);
    for (const [key, value] of Object.entries(attributes)) el.setAttribute(key, value);
    if (content) el.textContent = content;
    svg.append(el);
  };
  const x = index => 58 + (bins === 1 ? 365 : (index * 728) / (bins - 1));
  const y = count => 219 - 188 * count / max;
  for (const tick of new Set([0, Math.round(max / 2), max])) {
    add('line', { x1: 58, y1: y(tick), x2: 786, y2: y(tick), stroke: '#cddadb' });
    add('text', { x: 51, y: y(tick) + 4, 'text-anchor': 'end', fill: '#486065', 'font-size': 12 }, format.format(tick));
  }
  const legend = byId('legend');
  legend.replaceChildren();
  for (const [key, label, color] of types) {
    const points = Array.from({ length: bins }, (_, index) => [x(index), y(values.get(key + ':' + (start + index * interval)) ?? 0)]);
    add('polyline', { points: points.map(p => p.join(',')).join(' '), fill: 'none', stroke: color, 'stroke-width': 2.5 });
    if (bins === 1) add('circle', { cx: points[0][0], cy: points[0][1], r: 4, fill: color });
    const item = document.createElement('span');
    item.textContent = label;
    item.dataset.category = key;
    legend.append(item);
  }
  const date = new Intl.DateTimeFormat('id-ID', { month: 'short', day: 'numeric', hour: interval < 86_400_000 ? 'numeric' : undefined, timeZone: 'UTC' });
  for (const index of new Set([0, Math.floor((bins - 1) / 2), bins - 1])) {
    add('text', { x: x(index), y: 246, 'text-anchor': index === 0 ? 'start' : index === bins - 1 ? 'end' : 'middle', fill: '#486065', 'font-size': 13 }, date.format(start + index * interval));
  }
  byId('granularity').textContent = { '1 hour': 'Per jam', '1 day': 'Per hari', '7 days': 'Per 7 hari' }[data.step] + ' (UTC)';
}

byId('filters').addEventListener('submit', async event => {
  event.preventDefault();
  const from = new Date(byId('from').value);
  const to = new Date(byId('to').value);
  const status = byId('status');
  status.dataset.warning = 'false';
  status.textContent = 'Memuat data…';
  byId('results').hidden = true;
  const button = event.currentTarget.querySelector('button');
  button.disabled = true;
  try {
    if (!(from < to)) throw new Error('Waktu awal harus mendahului waktu akhir.');
    const query = new URLSearchParams({ from: from.toISOString(), to: to.toISOString() });
    const response = await fetch('/v1/analytics?' + query, {
      headers: { Authorization: 'Bearer ' + byId('token').value.trim() },
      cache: 'no-store', signal: AbortSignal.timeout(5000),
    });
    if (!response.ok) throw new Error(response.status === 401 ? 'Token tidak valid atau kedaluwarsa. Ambil token baru dari Auth.' : 'Data tidak dapat dimuat (HTTP ' + response.status + ').');
    const data = await response.json();
    plot(data, from, to);
    fillTable('severity', data.by_severity);
    fillTable('areas', data.by_area);
    status.dataset.warning = String(data.partial);
    status.textContent = data.partial ? 'Data belum lengkap. Unit tidak tersedia: ' + data.unavailable_units.join(', ') : 'Semua unit penyimpanan tersedia.';
    byId('results').hidden = false;
  } catch (error) {
    status.dataset.warning = 'true';
    status.textContent = error.message;
  } finally {
    button.disabled = false;
  }
});
