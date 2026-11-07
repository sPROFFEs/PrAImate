#!/usr/bin/env python3
"""Stage edition archives plus one standalone copy of every shared tool."""
import argparse
from pathlib import Path
import shutil

def stage(root, edition, target):
    root=Path(root);output=root/'dist/release';output.mkdir(parents=True,exist_ok=True)
    extension='.zip' if target.startswith('windows-') else '.tar.gz'
    editions=['desktop','server'] if edition=='all' else [edition]
    for item in editions:
        directory=root/('dist/server' if item=='server' else 'dist')
        prefix='praimate-server' if item=='server' else 'praimate'
        source=directory/(prefix+'-'+target+extension)
        if not source.is_file():raise ValueError('Missing mandatory '+source.name)
        shutil.copy2(source,output/source.name)
    shared=root/('dist/server' if edition=='server' else 'dist')
    suffix='.exe' if target.startswith('windows-') else ''
    names=['praimate-cli-'+target+suffix,'praimate-code-'+target+suffix,
           'praimate-code-'+target+'-baseline'+suffix,'praimate-graphify-'+target+suffix]
    for name in names:
        source=shared/name
        if source.is_file():shutil.copy2(source,output/name)
        elif name.startswith('praimate-cli-'):raise ValueError('Missing shared native CLI '+name)
    if edition=='all':
        left=root/'dist'/('praimate-cli-'+target+suffix)
        right=root/'dist/server'/('praimate-cli-'+target+suffix)
        if not right.is_file() or left.read_bytes()!=right.read_bytes():
            raise ValueError('Desktop and Server must use the same shared native CLI')
    return output

if __name__=='__main__':
    parser=argparse.ArgumentParser();parser.add_argument('--edition',choices=['all','desktop','server'],required=True);parser.add_argument('--target',choices=['linux-amd64','windows-amd64','windows-arm64','darwin-amd64','darwin-arm64'],required=True)
    args=parser.parse_args();stage(Path(__file__).resolve().parent.parent,args.edition,args.target)
