#!/usr/bin/env python3
"""Build the public offline package. All runtime and source inputs are local."""
import argparse, gzip, hashlib, io, json, tarfile
from pathlib import Path
from integrate import integrate, packed

ROOT=Path(__file__).resolve().parents[1]
PREFIX='singbox-fresh-1.11.2'
MANAGER='58309aaecc8972122da818190732c715d05f10f7f4f1f79f248593ad935d9358'
CORE='25a6e34ac9783adaa1d4576c3f76360291c274506fbb53b1fb8fb43452bb925d'

def sha(b):
    return hashlib.sha256(b).hexdigest()

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output',type=Path,default=ROOT/'dist')
    out=parser.parse_args().output
    managed=integrate((ROOT/'src/manager.sh').read_text(),'universal')
    core=(ROOT/'assets/sing-box-accounting.gz').read_bytes()
    if sha(managed)!=MANAGER or sha(gzip.decompress(core))!=CORE:
        raise ValueError('Component digest differs from its reviewed version')
    payload={'fresh.sh':(ROOT/'src/fresh.sh').read_bytes(),'install.sh':(ROOT/'src/standalone.sh').read_bytes(),
             'manager.gz':packed(managed),'core.gz':core,'LICENSE':(ROOT/'LICENSE').read_bytes(),
             'LICENSE.sing-box':(ROOT/'core-source/LICENSE.sing-box').read_bytes(),'NOTICE':(ROOT/'NOTICE').read_bytes()}
    notices=io.BytesIO()
    for group in ['THIRD_PARTY_LICENSES','shared_webui/licenses']:
        for path in sorted((ROOT/group).rglob('*')):
            if path.is_file():
                notices.write(b'\n== '+path.relative_to(ROOT).as_posix().encode()+b' ==\n')
                notices.write(path.read_bytes());notices.write(b'\n')
    payload['THIRD-PARTY-NOTICES.txt']=notices.getvalue()
    payload['manifest.json']=(json.dumps({'name':'singbox Fresh Public','version':'1.11.2','repository':'sam-ci-cloud/singbox','release':'v1.11.2-public.1','manager_sha256':MANAGER,'core_sha256':CORE},indent=2)+'\n').encode()
    payload['SHA256SUMS']=''.join(sha(data)+'  '+n+'\n' for n,data in sorted(payload.items())).encode()
    out.mkdir(parents=True,exist_ok=True)
    path=out/(PREFIX+'.tar.gz')
    with path.open('wb') as raw,gzip.GzipFile(filename='',fileobj=raw,mode='wb',compresslevel=1,mtime=0) as gz,tarfile.open(fileobj=gz,mode='w|',format=tarfile.USTAR_FORMAT) as tar:
        for name,data in sorted(payload.items()):
            m=tarfile.TarInfo(PREFIX+'/'+name);m.mode=0o755 if name in ('install.sh','fresh.sh') else 0o644
            m.size=len(data);tar.addfile(m,io.BytesIO(data))
    print(sha(path.read_bytes())+'  '+path.name)
if __name__=='__main__':
    main()
