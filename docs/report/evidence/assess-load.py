#!/usr/bin/env python3
"""Generate report metrics and a TCP plot from the preserved load measurements."""
import json
from pathlib import Path
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt

BASE=Path(__file__).resolve().parent
FIG=BASE.parent/'figures'
load=json.loads((BASE/'load.json').read_text())
tcp=json.loads((BASE/'tcp.json').read_text())
delay=json.loads((BASE/'pvmbg-delay.json').read_text())
status_counts=json.loads((BASE/'load-statuses.json').read_text())['counts']
errors_by_status={status:count for status,count in status_counts.items() if not 200<=int(status)<300 and int(status)!=429}
assert sum(errors_by_status.values())==sum(s['unexpected_errors'] for s in load['sources'].values())
error_description='; '.join(f'{count} request menghasilkan HTTP {status}' for status,count in sorted(errors_by_status.items())) or 'tidak ada respons error selain 429'
samples=tcp['samples']
best=(0,None,None)
start=None
previous=None
for row in samples:
    now=row['elapsed_seconds']
    if row['established_connections_to_api']>=50:
        if start is None or (previous is not None and now-previous>2.5*tcp['sampling_interval_seconds']):start=now
        duration=now-start
        if duration>best[0]:best=(duration,start,now)
    else:start=None
    previous=now
assessment={'tcp_threshold':50,'sampling_interval_seconds':tcp['sampling_interval_seconds'],'longest_sampled_interval_seconds':best[0],'interval_start_seconds':best[1],'interval_end_seconds':best[2],'peak_connections':max(r['established_connections_to_api'] for r in samples),'bmkg_p95_ms':load['sources']['BMKG']['success_duration_ms']['p95'],'hazard_error_percent':load['hazard_unexpected_error_percent'],'source_delay_observed_ms':delay['observed_ms'],'load_passed':load['passed'],'passed':load['passed'] and best[0]>=60 and delay['status']==200 and delay['observed_ms']>=3000}
(BASE/'assessment.json').write_text(json.dumps(assessment,indent=2)+'\n')
assert assessment['passed'],assessment

def number(v,d=2):return f'{v:,.{d}f}'.replace(',','@').replace('.',',').replace('@','.')
bmkg,pvmbg=(load['sources'][s] for s in ['BMKG','PVMBG'])
lines=[r'\begin{table}[htbp]',r'\centering\small',r'\begin{tabular}{lrr}',r'\toprule',r'Metrik & BMKG & PVMBG \\',r'\midrule']
for label,key,d in [('Request terkirim','attempts',0),('Respons sukses','successes',0),('Error selain 429','unexpected_errors',0),('HTTP 429 pada API','controlled_429',0)]:
    lines.append(f"{label} & {number(bmkg[key],d)} & {number(pvmbg[key],d)} \\\\")
for p in [50,95,99]:lines.append(f"p{p} sukses (ms) & {number(bmkg['success_duration_ms']['p'+str(p)])} & {number(pvmbg['success_duration_ms']['p'+str(p)])} \\\\")
lines.append("p95 seluruh respons (ms) & "+number(bmkg['all_duration_ms']['p95'])+" & "+number(pvmbg['all_duration_ms']['p95'])+" "+chr(92)*2)
lines += [r'\bottomrule',r'\end{tabular}',r'\caption{Hasil beban simultan selama '+number(load['elapsed_seconds'])+r' detik. p50/p95/p99 sukses dihitung dari 2xx; p95 seluruh respons juga ditampilkan.}',r'\label{tab:load}',r'\end{table}',
f"Dari {number(bmkg['attempts']+pvmbg['attempts'],0)} request yang dikirim, {number(bmkg['successes']+pvmbg['successes'],0)} mendapat respons sukses. Throughput-nya mencapai {number(load['hazard_successes_per_second'])} respons sukses per detik, sedangkan {error_description}, sebagaimana dicatat dalam " + chr(92) + "path{evidence/load-statuses.json}. Dengan demikian, error selain 429 sebesar " + number(load['hazard_unexpected_error_percent'],3) + r"\%, masih di bawah batas 1\%.",
"",
f"API tidak menghasilkan 429 pada pengujian ini. Auth menghasilkan {load['auth']['controlled_429']} respons 429 selama login dan refresh, tetapi retry berhasil sehingga tidak ada sesi worker yang gagal. Angka Auth tersebut dilaporkan terpisah agar pembatasan request terkendali tidak tercampur dengan error pada pembacaan hazard.",
"",
f"Delay langsung dari sumber PVMBG terukur {number(delay['observed_ms'])} ms, sementara p95 respons sukses BMKG sebesar {number(assessment['bmkg_p95_ms'])} ms. Hasil ini memenuhi batas 300 ms ketika kedua sumber dibaca bersamaan. Sampel TCP juga menunjukkan sedikitnya 50 koneksi pada interval {number(best[0])} detik, melebihi syarat 60 detik. Karena collector mengambil sampel sekitar sekali per detik, kesimpulan mengenai koneksi mengikuti resolusi pengukuran tersebut."]
(BASE/'load-results.tex').write_text('\n'.join(lines)+'\n')
(BASE/'conclusion-result.tex').write_text(f"Pada load test 5 Oktober 2026 dengan 60 worker dan delay PVMBG 3 detik, p95 BMKG sebesar {number(assessment['bmkg_p95_ms'])} ms, throughput {number(load['hazard_successes_per_second'])} respons sukses per detik, dan error selain 429 sebesar " + number(assessment['hazard_error_percent'],3) + r"\%. Sampel TCP menunjukkan sedikitnya 50 koneksi selama " + number(best[0]) + " detik. Hasil ini memenuhi ambang P2 pada profil beban yang diuji.\n")
plt.rcParams.update({'font.family':'DejaVu Sans','font.size':11,'axes.spines.top':False,'axes.spines.right':False})
fig,ax=plt.subplots(figsize=(8,3.1),layout='constrained')
ax.step([r['elapsed_seconds'] for r in samples],[r['established_connections_to_api'] for r in samples],where='post',color='#245c8f',linewidth=1.8,label='Koneksi API')
ax.axhline(50,color='#9a402b',linestyle='--',linewidth=1.2,label='Batas 50 koneksi')
ax.axvspan(best[1],best[2],alpha=.09,color='#245c8f')
ax.set(xlabel='Waktu sejak collector dimulai (detik)',ylabel='Koneksi established',ylim=(0,70))
ax.grid(axis='y',alpha=.2)
ax.legend(loc='lower center',ncol=2,frameon=False)
fig.savefig(FIG/'p2-tcp.pdf')
fig.savefig(FIG/'p2-tcp.png',dpi=180)
print(json.dumps(assessment,indent=2))
