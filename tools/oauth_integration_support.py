"""Private-state helpers for bounded OAuth integration checks (no SDK dependency)."""
import json
import os
from pathlib import Path
import subprocess
import urllib.error
import urllib.request


class Integration:
    def __init__(self, env_file):
        self.settings = {k: json.loads(v) for k, v in
                         (line.split('=', 1) for line in Path(env_file).read_text().splitlines())}
        s = self.settings
        if (s.get('DATABASE_DBNAME') != 'sub2api_integration_test' or
                s.get('DATABASE_USER') != 'sub2api_it_runtime' or
                s.get('DATABASE_SSLMODE') != 'verify-full'):
            raise RuntimeError('only named TLS integration runtime accepted')
        self.pg = dict(os.environ, PGHOST=s['DATABASE_HOST'], PGPORT='5432',
                       PGDATABASE=s['DATABASE_DBNAME'], PGUSER=s['DATABASE_USER'],
                       PGPASSWORD=s['DATABASE_PASSWORD'], PGSSLMODE='verify-full',
                       PGSSLROOTCERT=s['DATABASE_SSLROOTCERT'], PGCONNECT_TIMEOUT='5')

    def sql(self, statement):
        result = subprocess.run(['psql', '-X', '-qAt', '-v', 'ON_ERROR_STOP=1'],
                                input=statement, env=self.pg, capture_output=True,
                                text=True, timeout=20)
        if result.returncode:
            raise RuntimeError('integration SQL failed; sensitive details withheld')
        return result.stdout.strip()

    def http(self, port, path, method='GET', payload=None, token=None, headers=None):
        if port not in (18080, 18081, 18082, 18083):
            raise RuntimeError('unexpected integration port')
        request = urllib.request.Request(f'http://127.0.0.1:{port}{path}',
            data=json.dumps(payload).encode() if payload is not None else None,
            method=method, headers={'Content-Type': 'application/json',
                                    'User-Agent': 'authorized-oauth-integration/1'})
        if token:
            request.add_header('Authorization', 'Bearer '+token)
        for key, value in (headers or {}).items():
            request.add_header(key, value)
        try:
            with urllib.request.urlopen(request, timeout=90) as response:
                return response.status, json.loads(response.read(2_000_000))
        except urllib.error.HTTPError as error:
            try:
                data = json.loads(error.read(65536))
            except ValueError:
                data = {}
            return error.code, data

    def require(self, status, data, stage):
        if status != 200:
            raise RuntimeError(stage+' failed: HTTP '+str(status))
        return data.get('data', {})

    def hash_password(self, password, directory):
        helper = Path(directory)/'hash-password.go'
        helper.write_text('package main\nimport("fmt";"io";"os";"golang.org/x/crypto/bcrypt")\nfunc main(){b,_:=io.ReadAll(os.Stdin);h,e:=bcrypt.GenerateFromPassword(b,bcrypt.DefaultCost);if e!=nil{panic(e)};fmt.Print(string(h))}\n')
        helper.chmod(0o600)
        result = subprocess.run(['/root/.local/bin/go', 'run', str(helper)],
            cwd=Path(__file__).resolve().parents[1]/'backend', input=password,
            capture_output=True, text=True, timeout=60,
            env=dict(os.environ, GOMAXPROCS='2', GOMEMLIMIT='1400MiB', GOTOOLCHAIN='local'))
        if result.returncode:
            raise RuntimeError('password hash generation failed')
        return result.stdout

    def login(self, email, password):
        status, data = self.http(18080, '/api/v1/auth/login', 'POST',
                                 {'email': email, 'password': password})
        return self.require(status, data, 'login')['access_token']
