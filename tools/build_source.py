#!/usr/bin/env python3
"""Rebuild a byte-identical source TAR from either Git or an extracted release."""
import argparse,gzip,hashlib,io,json,stat,subprocess,tarfile
from pathlib import Path,PurePosixPath
ROOT=Path(__file__).resolve().parents[1]
INDEX='release/SOURCE-INVENTORY.json'
PREFIX='singbox-source-1.11.2'

def sha(data):
    return hashlib.sha256(data).hexdigest()

def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--output',type=Path,default=ROOT/'dist')
    p.add_argument('--index',action='store_true',help='Regenerate from staged tracked paths in a release checkout')
    args=p.parse_args()
    if args.index:
        members=subprocess.check_output(['git','-C',ROOT,'ls-files','-z']).decode().strip('\0').split('\0')
        rows={}
        for name in sorted(set(members)-{INDEX}):
            src=ROOT/name
            if not src.is_file() or src.is_symlink():raise ValueError('Invalid source path '+name)
            rows[name]={'sha256':sha(src.read_bytes()),'mode':0o755 if src.stat().st_mode&0o111 else 0o644}
        (ROOT/INDEX).write_text(json.dumps(rows,indent=2,sort_keys=True)+'\n')
    rows=json.loads((ROOT/INDEX).read_bytes())
    rows[INDEX]={'sha256':sha((ROOT/INDEX).read_bytes()),'mode':0o644}
    args.output.mkdir(parents=True,exist_ok=True)
    out=args.output/(PREFIX+'.tar.gz')
    with out.open('wb') as raw,gzip.GzipFile(filename='',fileobj=raw,mode='wb',mtime=0,compresslevel=1) as gz,tarfile.open(mode='w|',fileobj=gz,format=tarfile.GNU_FORMAT) as t:
        for name,record in sorted(rows.items()):
            safe=PurePosixPath(name);src=ROOT/name
            if safe.is_absolute() or '..' in safe.parts or '.git' in safe.parts or not src.resolve().is_relative_to(ROOT) or src.is_symlink():raise ValueError('Unsafe source path')
            data=src.read_bytes()
            if sha(data)!=record['sha256'] or record['mode'] not in (0o644,0o755):raise ValueError('Source mismatch '+name)
            m=tarfile.TarInfo(PREFIX+'/'+name);m.mode=record['mode'];m.size=len(data)
            t.addfile(m,io.BytesIO(data))
    print(sha(out.read_bytes())+'  '+out.name)
if __name__=='__main__':
    main()
