#!/usr/bin/env python3
"""Run the P2 workload on a dedicated k6 host and record active VUs."""
import datetime
import http.server
import json
import os
from pathlib import Path
import resource
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request


def fetch_json(url, payload=None):
    request = urllib.request.Request(url, data=json.dumps(payload).encode() if payload else None,
                                     headers={'Content-Type': 'application/json'})
    with urllib.request.urlopen(request, timeout=4) as response:
        return json.load(response)


def refresh_token(state, auth_url, secret, stop):
    while not stop.is_set():
        try:
            session = fetch_json(auth_url + '/v1/auth/login',
                                 {'client_id': 'field-team', 'client_secret': secret})
            state['valid_until'] = time.time() + session['expires_in']
            state['response'] = json.dumps({
                'access_token': session['access_token'],
                'expires_at_ms': int(state['valid_until'] * 1000),
            }).encode()
            state['ready'].set()
            stop.wait(15)
        except (OSError, ValueError, KeyError) as error:
            print(f'Auth refresh failed ({type(error).__name__})', file=sys.stderr, flush=True)
            stop.wait(2)


def token_server(state):
    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            body = state.get('response', b'')
            if self.path != '/token':
                self.send_error(404)
                return
            if not body or time.time() >= state.get('valid_until', 0) - 3:
                self.send_error(503)
                return
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *_args):
            pass

    class Server(http.server.ThreadingHTTPServer):
        request_queue_size = 256

    return Server(('127.0.0.1', 0), Handler)


def sample(process, stream):
    attrs = fetch_json('http://127.0.0.1:6565/v1/status')['data']['attributes']
    memory = Path(f'/proc/{process.pid}/status')
    rss_kb = next((int(line.split()[1]) for line in memory.read_text().splitlines()
                   if line.startswith('VmRSS:')), None)
    now = datetime.datetime.now(datetime.timezone.utc)
    print(json.dumps({'epoch': now.timestamp(), 'utc': now.isoformat(),
                      'vus': attrs['vus'], 'vus_max': attrs['vus-max'], 'rss_kb': rss_kb}),
          file=stream, flush=True)


def main():
    if len(sys.argv) != 2:
        raise SystemExit('Usage: run-m2-p2.py RESULTS_DIR')
    os.umask(0o077)
    secret = os.environ.pop('FIELD_TEAM_CLIENT_SECRET', '') or sys.stdin.readline().strip()
    auth = os.environ['P2_AUTH_URL'].rstrip('/')
    if not secret:
        raise SystemExit('Supply the Field Team test secret through stdin or FIELD_TEAM_CLIENT_SECRET.')
    output = Path(sys.argv[1]).resolve()
    output.mkdir()
    script = Path(__file__).with_name('m2-p2.js').resolve()
    soft, hard = resource.getrlimit(resource.RLIMIT_NOFILE)
    if soft < 65536:
        resource.setrlimit(resource.RLIMIT_NOFILE,
                           (65536 if hard == resource.RLIM_INFINITY else min(hard, 65536), hard))

    stop, state = threading.Event(), {'ready': threading.Event()}
    refresh = threading.Thread(target=refresh_token, args=(state, auth, secret, stop), daemon=True)
    refresh.start()
    if not state['ready'].wait(25):
        raise SystemExit('Auth did not issue a token.')
    with token_server(state) as server:
        relay = threading.Thread(target=server.serve_forever, daemon=True)
        relay.start()
        env = dict(os.environ, K6_NO_USAGE_REPORT='true',
                   P2_TOKEN_URL=f'http://127.0.0.1:{server.server_port}/token', P2_RUN_ID=output.name)
        command = [env.get('K6_BIN', 'k6'), 'run', '--address', '127.0.0.1:6565', '--quiet', str(script)]
        with (output / 'k6.log').open('w') as log, (output / 'active-vus.jsonl').open('w') as activity:
            process = subprocess.Popen(command, cwd=output, env=env, stdout=log, stderr=subprocess.STDOUT)
            try:
                while process.poll() is None:
                    try:
                        sample(process, activity)
                    except (OSError, ValueError, KeyError):
                        pass
                    time.sleep(1)
            finally:
                if process.poll() is None:
                    process.terminate()
                    process.wait(timeout=10)
                stop.set()
                server.shutdown()
            (output / 'exit-code').write_text(str(process.returncode) + '\n')
        print((output / 'result.json').read_text() if (output / 'result.json').exists() else (output / 'k6.log').read_text())
        return process.returncode


if __name__ == '__main__':
    sys.exit(main())
