#!/usr/bin/env python3
"""Collect source access, outage recovery and correlation evidence on a local demo stack."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys

ROOT=Path(__file__).resolve().parents[3]
os.chdir(ROOT)
spec=importlib.util.spec_from_file_location('trial',ROOT/'scripts/verify-integration.py')
trial=importlib.util.module_from_spec(spec)
spec.loader.exec_module(trial)
t=trial.Trials()
OUT=ROOT/'docs/report/evidence'

def save(name,value):
    (OUT/name).write_text(json.dumps(value,indent=2)+'\n')

checks=[]
for source,key in [('BMKG','BMKG_API_KEY'),('BMKG','PVMBG_TOKEN'),('PVMBG','PVMBG_TOKEN'),('PVMBG','BMKG_API_KEY')]:
    url='http://127.0.0.1:'+t.env.get('BMKG_MOCK_HTTP_PORT','8081')+'/seismic-events' if source=='BMKG' else t.pvmbg+'/volcanic-reports'
    headers={'X-BMKG-Key':t.env[key]} if source=='BMKG' else {'Authorization':'Bearer '+t.env[key]}
    status,_,_=trial.request(url,headers=headers)
    checks.append({'source':source,'credential':key,'status':status})
s=t.login('media')
status,body,_=t.read(s['access_token'],'?source=BMKG&limit=1')
media={'status':status,'fields':sorted(body['data'][0]),'full_status':t.read(s['access_token'],'?view=full')[0],'raw_fields_status':t.read(s['access_token'],'?fields=attributes')[0]}
ids=trial.compose('ps','-q').split()
containers=json.loads(subprocess.check_output(['docker','inspect',*ids],text=True))
networks={c['Config']['Labels']['com.docker.compose.service']:sorted(c['NetworkSettings']['Networks']) for c in containers}
ports={c['Config']['Labels']['com.docker.compose.service']:c['NetworkSettings']['Ports'] for c in containers if c['Config']['Labels']['com.docker.compose.service'] in ['postgres','nats']}
save('access-and-networks.json',{'source_credentials':checks,'media':media,'networks':networks,'private_ports':ports})

s=t.login()
before=t.container_state()
t.admin('outage',{'enabled':True})
try:
    def inspect_outage():
        code,data,_=t.read(s['access_token'],'?source=PVMBG&view=full&limit=50')
        if code!=200 or data['meta']['sources']['PVMBG']['status']!='stale':return None
        bc,bd,_=t.read(s['access_token'],'?source=BMKG&view=full&limit=1')
        if bc!=200 or bd['meta']['sources']['BMKG']['status']!='fresh':return None
        return {'pvmbg_status':code,'pvmbg_records':len(data['data']),'pvmbg_meta':data['meta']['sources']['PVMBG'],'bmkg_status':bc,'bmkg_meta':bd['meta']['sources']['BMKG']}
    outage=trial.eventually(inspect_outage)
finally:
    t.admin('outage',{'enabled':False})
recovery=trial.eventually(lambda: (data['meta']['sources']['PVMBG'] if (data:=t.read(s['access_token'],'?source=PVMBG&view=full&limit=1')[1])['meta']['sources']['PVMBG']['status']=='fresh' else None))
save('outage-recovery.json',{'outage':outage,'recovery':recovery,'containers_before':before,'containers_after':t.container_state()})

cid='report-m1-correlation'
code,data,_=trial.request(t.api+'/v1/hazards?source=BMKG&view=full&limit=1',token=s['access_token'],headers={'X-Correlation-ID':cid})
assert code==200
logs=subprocess.check_output(['docker','compose','logs','--no-color','--no-log-prefix','--since','5m','client-api','aggregator'],text=True)
selected=[]
for line in logs.splitlines():
    try:row=json.loads(line)
    except json.JSONDecodeError:continue
    if row.get('correlation_id')==cid:selected.append(row)
assert len(selected)>=2
(OUT/'correlation.log').write_text('\n'.join(json.dumps(row) for row in selected)+'\n')
print(json.dumps({'credential_requests':len(checks),'media_status':media['status'],'outage':outage['pvmbg_meta']['status'],'recovery':recovery['status'],'correlation_logs':len(selected)}))
