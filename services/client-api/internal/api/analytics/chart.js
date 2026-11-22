const byId = id => document.getElementById(id);
const number = new Intl.NumberFormat('id-ID');
const fullDate = new Intl.DateTimeFormat('id-ID', {
  day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit', timeZone: 'UTC',
});
const categories = [
  ['SEISMIC', 'Gempa'], ['TSUNAMI', 'Tsunami'], ['VOLCANIC', 'Gunung api'],
  ['WILDFIRE', 'Kebakaran hutan'], ['COASTAL', 'Pesisir'],
];
const severities = [['AWAS', 'Awas'], ['SIAGA', 'Siaga'], ['WASPADA', 'Waspada'], ['NORMAL', 'Normal']];
const day = 86_400_000;
const steps = {
  '1 hour': [day / 24, 'Per jam'], '1 day': [day, 'Per hari'], '7 days': [7 * day, 'Per 7 hari'],
};
const areaPageSize = 6;
const presetButtons = [...byId('presets').querySelectorAll('button')];
const chart = byId('chart');
const svg = byId('chart-svg');
let result;
let activeCategory = '';
let areaPage = 0;
let chartCursor;

function setPreset(button) {
  const end = Math.ceil(Date.now() / 60_000) * 60_000;
  byId('from').value = new Date(end - Number(button.dataset.days) * day).toISOString().slice(0, 16);
  byId('to').value = new Date(end).toISOString().slice(0, 16);
  for (const preset of presetButtons) preset.setAttribute('aria-pressed', String(preset === button));
}
for (const button of presetButtons) button.addEventListener('click', () => setPreset(button));
setPreset(presetButtons[0]);
for (const id of ['from', 'to']) {
  byId(id).addEventListener('input', () => {
    for (const preset of presetButtons) preset.setAttribute('aria-pressed', 'false');
  });
}

const typeButtons = new Map(categories.map(([key, label]) => {
  const button = document.createElement('button');
  button.type = 'button';
  button.dataset.kind = key;
  button.setAttribute('aria-controls', 'trend');
  const title = document.createElement('span');
  title.className = 'type-label';
  title.textContent = label;
  const count = document.createElement('span');
  count.className = 'type-count';
  button.append(title, count);
  button.addEventListener('click', () => selectCategory(activeCategory === key ? '' : key));
  byId('types').append(button);
  return [key, button];
}));

function selectCategory(key) {
  activeCategory = key;
  for (const [category, button] of typeButtons) button.setAttribute('aria-pressed', String(key === category));
  byId('all-types').hidden = !key;
  renderChart();
}
byId('all-types').addEventListener('click', () => {
  const previous = typeButtons.get(activeCategory);
  selectCategory('');
  previous?.focus();
});

function renderSeverity() {
  const rows = severities.map(([key, label]) => {
    const count = result.data.by_severity[key] ?? 0;
    const row = document.createElement('li');
    row.dataset.level = key;
    const name = document.createElement('span');
    name.className = 'level-name';
    name.textContent = label;
    const value = document.createElement('span');
    value.className = 'level-count';
    value.textContent = number.format(count);
    const bar = document.createElement('progress');
    bar.max = Math.max(result.total, 1);
    bar.value = count;
    bar.setAttribute('aria-hidden', 'true');
    row.append(name, value, bar);
    return row;
  });
  byId('severity').replaceChildren(...rows);
}

function renderAreas() {
  const search = byId('area-search');
  const query = search.value.trim().toLocaleLowerCase('id-ID');
  const found = result.areas.filter(([name]) => name.toLocaleLowerCase('id-ID').includes(query));
  const offset = areaPage * areaPageSize;
  const rows = found.slice(offset, offset + areaPageSize).map(([name, count]) => {
    const row = document.createElement('tr');
    row.insertCell().textContent = name;
    row.insertCell().textContent = number.format(count);
    return row;
  });
  const all = result.areas.length;
  byId('area-count').textContent = query
    ? number.format(found.length) + ' dari ' + number.format(all) + ' wilayah'
    : number.format(all) + ' wilayah';
  search.hidden = all === 0;
  byId('areas').tBodies[0].replaceChildren(...rows);
  byId('areas').hidden = found.length === 0;
  byId('area-empty').hidden = found.length > 0;
  byId('area-empty').textContent = all ? 'Wilayah tidak ditemukan.' : emptyRange();
  byId('area-nav').hidden = found.length <= areaPageSize;
  byId('area-page').textContent = number.format(offset + 1) + '–' +
    number.format(Math.min(offset + areaPageSize, found.length)) + ' dari ' + number.format(found.length);
  byId('area-prev').disabled = areaPage === 0;
  byId('area-next').disabled = offset + areaPageSize >= found.length;
}

byId('area-search').addEventListener('input', () => {
  areaPage = 0;
  renderAreas();
});
byId('area-prev').addEventListener('click', () => { areaPage--; renderAreas(); });
byId('area-next').addEventListener('click', () => { areaPage++; renderAreas(); });

function paint(name, attributes, label) {
  const node = document.createElementNS('http://www.w3.org/2000/svg', name);
  for (const [key, value] of Object.entries(attributes)) node.setAttribute(key, value);
  if (label) node.textContent = label;
  svg.append(node);
  return node;
}

function emptyRange() {
  return result.data.partial ? 'Tidak ada kejadian dari unit yang tersedia.' : 'Belum ada kejadian di rentang ini.';
}

function renderChart() {
  if (!result || byId('details').hidden) return;
  const { start, interval, bins, counts, from, to } = result;
  const selected = categories.filter(([key]) => !activeCategory || key === activeCategory);
  const visible = selected.filter(([key]) => counts.get(key).some(Boolean));
  const label = selected.length === 1 ? selected[0][1] : 'Semua jenis';
  byId('granularity').textContent = label + ' · ' + result.step;
  byId('chart-empty').hidden = visible.length > 0;
  byId('chart-empty').textContent = activeCategory && !result.data.partial
    ? 'Tidak ada kejadian untuk jenis ini di rentang yang dipilih.'
    : emptyRange();
  byId('plot').hidden = visible.length === 0;
  chartCursor = undefined;
  if (visible.length === 0 || !chart.clientWidth) return;

  const width = chart.clientWidth;
  const height = chart.clientHeight;
  const peak = Math.max(...visible.flatMap(([key]) => counts.get(key)));
  const magnitude = 10 ** Math.floor(Math.log10(Math.max(1, peak / 4)));
  const stride = [1, 2, 5, 10].find(factor => factor * magnitude >= peak / 4) * magnitude;
  const max = Math.ceil(peak / stride) * stride;
  const left = Math.max(36, 12 + number.format(max).length * 7);
  const right = width - 15;
  const bottom = height - 28;
  const x = index => bins === 1 ? (left + right) / 2 : left + index * (right - left) / (bins - 1);
  const y = count => bottom - count * (bottom - 9) / max;

  svg.setAttribute('viewBox', '0 0 ' + width + ' ' + height);
  svg.replaceChildren();
  for (let tick = 0; tick <= max; tick += stride) {
    paint('line', { x1: left, x2: right, y1: y(tick), y2: y(tick), class: 'gridline' });
    paint('text', { x: left - 8, y: y(tick) + 4, 'text-anchor': 'end', class: 'axis-label' }, number.format(tick));
  }
  const date = new Intl.DateTimeFormat('id-ID', {
    day: 'numeric', month: 'short', timeZone: 'UTC',
    ...(interval < day && { hour: '2-digit', minute: '2-digit' }),
  });
  const tickCount = Math.min(bins, Math.max(2, Math.floor((right - left) / 105) + 1), 6);
  for (let n = 0; n < tickCount; n++) {
    const index = tickCount === 1 ? 0 : Math.round(n * (bins - 1) / (tickCount - 1));
    const anchor = tickCount === 1 ? 'middle' : n === 0 ? 'start' : n === tickCount - 1 ? 'end' : 'middle';
    paint('text', { x: x(index), y: height - 7, 'text-anchor': anchor, class: 'axis-label' },
      date.format(start + index * interval));
  }
  for (const [key] of visible) {
    const points = counts.get(key).map((count, index) => x(index) + ',' + y(count));
    if (bins === 1) {
      paint('circle', { cx: x(0), cy: y(counts.get(key)[0]), r: 4, class: 'point', 'data-kind': key });
    } else {
      paint('polyline', { points: points.join(' '), class: 'series', 'data-kind': key });
    }
  }
  const cursor = paint('line', { class: 'cursor', y1: 9, y2: bottom });
  const dots = visible.map(([key]) => paint('circle', { class: 'point', 'data-kind': key, r: 4 }));
  const readout = byId('readout');
  let current = -1;
  function inspect(index) {
    index = Math.max(0, Math.min(bins - 1, index));
    if (index === current) return;
    current = index;
    cursor.setAttribute('x1', x(index));
    cursor.setAttribute('x2', x(index));
    dots.forEach((dot, i) => {
      dot.setAttribute('cx', x(index));
      dot.setAttribute('cy', y(counts.get(visible[i][0])[index]));
    });
    const time = document.createElement('time');
    const bucket = start + index * interval;
    time.dateTime = new Date(Math.max(bucket, from)).toISOString();
    time.textContent = fullDate.format(Math.max(bucket, from)) + ' – ' +
      fullDate.format(Math.min(bucket + interval, to)) + ' UTC';
    const readings = selected.map(([key, name]) => {
      const value = document.createElement('span');
      value.className = 'reading-label';
      value.dataset.kind = key;
      value.textContent = name + ' ' + number.format(counts.get(key)[index]);
      return value;
    });
    readout.replaceChildren(time, ...readings);
  }
  const latest = Math.max(...visible.map(([key]) => counts.get(key).findLastIndex(Boolean)));
  inspect(latest);
  chartCursor = {
    point: offset => inspect(Math.round((offset - left) * (bins - 1) / (right - left))),
    move: direction => inspect(current + direction),
  };
}
new ResizeObserver(renderChart).observe(chart);
function inspectPointer(event) {
  chartCursor?.point(event.clientX - chart.getBoundingClientRect().left);
}
chart.addEventListener('pointermove', event => {
  if (event.pointerType === 'mouse') inspectPointer(event);
});
chart.addEventListener('pointerdown', inspectPointer);
chart.addEventListener('keydown', event => {
  if (!chartCursor || !['ArrowLeft', 'ArrowRight'].includes(event.key)) return;
  event.preventDefault();
  chartCursor.move(event.key === 'ArrowLeft' ? -1 : 1);
});

function showStatus(message, tone) {
  const notice = byId('status');
  notice.hidden = !message;
  notice.dataset.tone = tone;
  notice.textContent = message;
}

function showResult(data, from, to) {
  const [interval, step] = steps[data.step];
  const start = Math.floor(from / interval) * interval;
  const bins = Math.ceil((to - start) / interval);
  const counts = new Map(categories.map(([key]) => [key, Array(bins).fill(0)]));
  for (const point of data.series) {
    const index = (Date.parse(point.bucket) - start) / interval;
    counts.get(point.category)[index] = point.count;
  }
  const total = data.total_events ?? Object.values(data.by_severity).reduce((sum, count) => sum + count, 0);
  const areaOrder = new Intl.Collator('id-ID');
  const areas = Object.entries(data.by_area).sort((a, b) => b[1] - a[1] || areaOrder.compare(a[0], b[0]));
  result = { from, to, data, total, areas, interval, step, start, bins, counts };

  const noCoverage = data.coverage?.available_units === 0;
  const coverage = byId('coverage');
  coverage.dataset.partial = String(data.partial);
  coverage.textContent = (data.partial ? 'Data parsial' : 'Data lengkap') +
    (data.coverage ? ' · ' + data.coverage.available_units + ' dari ' + data.coverage.total_units + ' unit' : '');
  byId('total').textContent = noCoverage ? '—' : number.format(total);
  byId('window').textContent = fullDate.format(from) + ' – ' + fullDate.format(to) + ' UTC';
  byId('types').hidden = noCoverage;
  byId('details').hidden = noCoverage;
  byId('results').hidden = false;
  if (noCoverage) {
    showStatus('Data tidak tersedia. Seluruh unit penyimpanan tidak dapat diakses.', 'error');
    return;
  }

  for (const [key, button] of typeButtons) {
    const count = data.by_category?.[key] ?? counts.get(key).reduce((sum, value) => sum + value, 0);
    button.lastElementChild.textContent = number.format(count);
  }
  if (data.partial) {
    showStatus('Angka belum lengkap. Unit tidak tersedia: ' + data.unavailable_units.join(', ') + '.', 'warning');
  } else {
    showStatus('', '');
  }
  byId('area-search').value = '';
  areaPage = 0;
  renderAreas();
  renderSeverity();
  selectCategory(activeCategory);
}

byId('filters').addEventListener('submit', async event => {
  event.preventDefault();
  const from = Date.parse(byId('from').value + 'Z');
  const to = Date.parse(byId('to').value + 'Z');
  const earlier = result ? ' Hasil sebelumnya masih ditampilkan.' : '';
  if (!(from < to) || to - from > 366 * day) {
    showStatus('Pilih rentang hingga 366 hari dengan waktu akhir setelah waktu mulai.' + earlier, 'error');
    return;
  }

  const button = byId('apply');
  button.disabled = true;
  showStatus('Memuat data…' + earlier, 'info');
  try {
    const query = new URLSearchParams({ from: new Date(from).toISOString(), to: new Date(to).toISOString() });
    const response = await fetch('/v1/analytics?' + query, {
      headers: { Authorization: 'Bearer ' + byId('token').value.trim() },
      cache: 'no-store', signal: AbortSignal.timeout(5000),
    });
    if (response.status === 401) {
      showStatus('Token tidak valid atau kedaluwarsa. Masukkan token baru dari Auth.' + earlier, 'error');
      byId('token').focus();
      return;
    }
    if (!response.ok) {
      showStatus('Data tidak dapat dimuat (HTTP ' + response.status + ').' + earlier, 'error');
      return;
    }
    showResult(await response.json(), from, to);
  } catch {
    showStatus('Data tidak dapat dimuat. Coba lagi.' + earlier, 'error');
  } finally {
    button.disabled = false;
  }
});
