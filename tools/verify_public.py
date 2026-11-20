#!/usr/bin/env python3
"""Verify every local release asset against the independently trusted signing key."""
import argparse,hashlib,json,re,subprocess
from pathlib import Path
NAMES={'sb.sh','sb.sh.sig','singbox-fresh-1.11.2.tar.gz','singbox-fresh-1.11.2.tar.gz.sig',
       'singbox-source-1.11.2.tar.gz','singbox-source-1.11.2.tar.gz.sig','RELEASE-PUBLIC.pem',
       'SBOM.json','NOTICES.txt','RELEASE-NOTES.md','RELEASE-MANIFEST.json','RELEASE-MANIFEST.json.sig','SHA256SUMS','SHA256SUMS.sig'}
def sha(p):
    with p.open('rb') as stream:return hashlib.file_digest(stream,'sha256').hexdigest()
def verify(key,path,sig):
    subprocess.run(['openssl','pkeyutl','-verify','-rawin','-pubin','-inkey',key,'-in',path,'-sigfile',sig],check=True,stdout=subprocess.DEVNULL)
def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('assets',type=Path);parser.add_argument('--key',type=Path,required=True)
    args=parser.parse_args();root=args.assets;key=args.key
    if {f.name for f in root.iterdir()}!=NAMES or any(f.is_symlink() or not f.is_file() for f in root.iterdir()):raise ValueError('Invalid asset set')
    if key.read_bytes()!=(root/'RELEASE-PUBLIC.pem').read_bytes():raise ValueError('Wrong publisher')
    for n in ['SHA256SUMS','RELEASE-MANIFEST.json','singbox-fresh-1.11.2.tar.gz','singbox-source-1.11.2.tar.gz','sb.sh']:
        verify(key,root/n,root/(n+'.sig'))
    sums={}
    for line in (root/'SHA256SUMS').read_text().splitlines():
        h,n=line.split('  ')
        if n not in NAMES or n in sums or not re.fullmatch('[a-f0-9]{64}',h) or sha(root/n)!=h:raise ValueError('Asset mismatch')
        sums[n]=h
    if sums.keys()!=NAMES-{'SHA256SUMS','SHA256SUMS.sig'}:raise ValueError('Incomplete manifest')
    m=json.loads((root/'RELEASE-MANIFEST.json').read_bytes())
    if m['repository']!='sam-ci-cloud/singbox' or m['tag']!='v1.11.2-public.1' or not re.fullmatch('[a-f0-9]{40}',m['commit']):raise ValueError('Wrong release')
    if set(m['assets'])!=NAMES-{'RELEASE-MANIFEST.json','RELEASE-MANIFEST.json.sig','SHA256SUMS','SHA256SUMS.sig'}:raise ValueError('Incomplete release metadata')
    for n,entry in m['assets'].items():
        if entry['bytes']!=(root/n).stat().st_size or sums[n]!=entry['sha256']:raise ValueError('Release manifest mismatch')
    print(json.dumps({'status':'PASS','tag':m['tag'],'commit':m['commit'],'assets':len(NAMES)}))
if __name__=='__main__':main()
